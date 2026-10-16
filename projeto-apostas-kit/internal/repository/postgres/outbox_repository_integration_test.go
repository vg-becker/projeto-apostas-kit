//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/port"
)

func (f *fixture) addEvent(t *testing.T, at time.Time) event.Event {
	t.Helper()

	evt := f.newEvent(t, at)

	err := f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		return f.outbox.Add(ctx, evt)
	})
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}

	return evt
}

func outboxClaim(now time.Time, limit int) port.ClaimParams {
	return port.ClaimParams{Limit: limit, Now: now, Lease: time.Minute}
}

func eventIDs(records []port.OutboxRecord) []string {
	result := make([]string, 0, len(records))
	for _, record := range records {
		result = append(result, record.EventID.String())
	}

	sort.Strings(result)

	return result
}

func TestOutboxRoundTripPreservesIdentityAndPayload(t *testing.T) {
	f := newFixture(t)
	evt := f.addEvent(t, baseTime)

	records, err := f.outbox.Claim(context.Background(), outboxClaim(baseTime, 10))
	if err != nil || len(records) != 1 {
		t.Fatalf("claimed %d (%v), want 1", len(records), err)
	}

	record := records[0]
	if record.EventID != evt.EventID() || record.AggregateID != evt.AggregateID() ||
		record.EventType != event.TypeWalletBalanceChanged || record.Attempts != 0 {
		t.Fatalf("record = %+v", record)
	}

	var decoded map[string]any
	if err := json.Unmarshal(record.Payload, &decoded); err != nil {
		t.Fatalf("payload is not json: %v", err)
	}

	data := decoded["data"].(map[string]any)
	if decoded["eventId"] != evt.EventID().String() || data["walletVersion"] != float64(2) {
		t.Fatalf("payload = %v", decoded)
	}
}

func TestOutboxRejectsDuplicateEventID(t *testing.T) {
	f := newFixture(t)
	evt := f.addEvent(t, baseTime)

	err := f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		return f.outbox.Add(ctx, evt)
	})

	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("error = %v, want %v", err, port.ErrAlreadyExists)
	}
}

func TestOutboxEventIsInvisibleUntilCommit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rolledBack := f.newEvent(t, baseTime)

	err := f.manager.WithinTransaction(ctx, func(txContext context.Context) error {
		if err := f.outbox.Add(txContext, rolledBack); err != nil {
			return err
		}

		visible, claimErr := f.outbox.Claim(ctx, outboxClaim(baseTime, 10))
		if claimErr != nil || len(visible) != 0 {
			t.Errorf("uncommitted event visible to publisher: %d (%v)", len(visible), claimErr)
		}

		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want %v", err, errBoom)
	}

	afterRollback, err := f.outbox.Claim(ctx, outboxClaim(baseTime, 10))
	if err != nil || len(afterRollback) != 0 {
		t.Fatalf("claimed %d after rollback (%v), want 0", len(afterRollback), err)
	}

	f.addEvent(t, baseTime)

	afterCommit, err := f.outbox.Claim(ctx, outboxClaim(baseTime, 10))
	if err != nil || len(afterCommit) != 1 {
		t.Fatalf("claimed %d after commit (%v), want 1", len(afterCommit), err)
	}
}

func TestTwoPublishersCompeteWithoutOverlap(t *testing.T) {
	f := newFixture(t)
	for index := range 6 {
		f.addEvent(t, baseTime.Add(time.Duration(index)*time.Second))
	}

	var first, second []port.OutboxRecord
	locked := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)

	go func() {
		finished <- f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
			var err error
			first, err = f.outbox.Claim(ctx, outboxClaim(baseTime.Add(time.Hour), 3))
			close(locked)
			<-release

			return err
		})
	}()

	<-locked

	second, err := f.outbox.Claim(context.Background(), outboxClaim(baseTime.Add(time.Hour), 6))
	close(release)

	if firstErr := <-finished; err != nil || firstErr != nil {
		t.Fatalf("claim errors = %v, %v", err, firstErr)
	}

	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("claimed %d and %d, want 3 and 3", len(first), len(second))
	}

	union := append(eventIDs(first), eventIDs(second)...)
	seen := map[string]bool{}
	for _, id := range union {
		if seen[id] {
			t.Fatalf("%s claimed by both publishers", id)
		}

		seen[id] = true
	}
}

func TestAbandonedLeaseIsRecoveredWithTheSameEventID(t *testing.T) {
	f := newFixture(t)
	evt := f.addEvent(t, baseTime)
	ctx := context.Background()

	crashed, err := f.outbox.Claim(ctx, outboxClaim(baseTime, 10))
	if err != nil || len(crashed) != 1 {
		t.Fatalf("publisher A claimed %d (%v), want 1", len(crashed), err)
	}

	during, err := f.outbox.Claim(ctx, outboxClaim(baseTime.Add(30*time.Second), 10))
	if err != nil || len(during) != 0 {
		t.Fatalf("publisher B claimed %d during the lease (%v), want 0", len(during), err)
	}

	recovered, err := f.outbox.Claim(ctx, outboxClaim(baseTime.Add(2*time.Minute), 10))
	if err != nil || len(recovered) != 1 || recovered[0].EventID != evt.EventID() {
		t.Fatalf("recovered = %+v (%v), want the same event id", recovered, err)
	}

	if err := f.outbox.MarkPublished(ctx, evt.EventID(), baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("MarkPublished error = %v", err)
	}

	after, err := f.outbox.Claim(ctx, outboxClaim(baseTime.Add(time.Hour), 10))
	if err != nil || len(after) != 0 {
		t.Fatalf("claimed %d after publication (%v), want 0", len(after), err)
	}
}

func TestMarkFailedAppliesBackoffAndCountsAttempts(t *testing.T) {
	f := newFixture(t)
	evt := f.addEvent(t, baseTime)
	ctx := context.Background()

	if _, err := f.outbox.Claim(ctx, outboxClaim(baseTime, 10)); err != nil {
		t.Fatalf("claim error = %v", err)
	}

	retryAt := baseTime.Add(5 * time.Minute)
	if err := f.outbox.MarkFailed(ctx, evt.EventID(), retryAt); err != nil {
		t.Fatalf("MarkFailed error = %v", err)
	}

	early, err := f.outbox.Claim(ctx, outboxClaim(baseTime.Add(time.Minute), 10))
	if err != nil || len(early) != 0 {
		t.Fatalf("claimed %d before backoff expired (%v), want 0", len(early), err)
	}

	due, err := f.outbox.Claim(ctx, outboxClaim(retryAt, 10))
	if err != nil || len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("due = %+v (%v), want one event with 1 attempt", due, err)
	}
}

func TestOutboxMarksOnUnknownEventAreNotFound(t *testing.T) {
	f := newFixture(t)
	unknown := f.newEvent(t, baseTime)
	ctx := context.Background()

	published := f.outbox.MarkPublished(ctx, unknown.EventID(), baseTime)
	failed := f.outbox.MarkFailed(ctx, unknown.EventID(), baseTime)

	if !errors.Is(published, port.ErrNotFound) || !errors.Is(failed, port.ErrNotFound) {
		t.Fatalf("errors = %v, %v", published, failed)
	}
}

func TestMarkPublishedIsIdempotentAndPublishedEventsCannotFail(t *testing.T) {
	f := newFixture(t)
	evt := f.addEvent(t, baseTime)
	ctx := context.Background()

	for _, at := range []time.Time{baseTime, baseTime.Add(time.Hour)} {
		if err := f.outbox.MarkPublished(ctx, evt.EventID(), at); err != nil {
			t.Fatalf("MarkPublished error = %v", err)
		}
	}

	if err := f.outbox.MarkFailed(ctx, evt.EventID(), baseTime); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("MarkFailed after publication error = %v, want %v", err, port.ErrNotFound)
	}
}

func TestOutboxSnapshotIsImmutableInTheDatabase(t *testing.T) {
	f := newFixture(t)
	f.addEvent(t, baseTime)

	_, err := f.pool.Exec(context.Background(), `UPDATE outbox_events SET payload = '{"a":1}'`)

	if err == nil || errors.Is(err, port.ErrTransient) {
		t.Fatalf("payload update must fail permanently, error = %v", err)
	}
}

func TestOutboxClaimRejectsNonPositiveLimitOrLease(t *testing.T) {
	f := newFixture(t)

	_, limitErr := f.outbox.Claim(context.Background(), port.ClaimParams{Lease: time.Minute})
	_, leaseErr := f.outbox.Claim(context.Background(), port.ClaimParams{Limit: 1})

	if limitErr == nil || leaseErr == nil {
		t.Fatal("invalid claim params must be rejected")
	}
}

func TestClaimReturnsTheEventsInTheOrderTheyOccurred(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// inseridos fora de ordem de propósito, com instantes crescentes
	instants := []time.Duration{4, 1, 5, 2, 3, 0, 7, 6}
	for _, offset := range instants {
		f.addEvent(t, baseTime.Add(offset*time.Second))
	}

	records, err := f.outbox.Claim(ctx, outboxClaim(baseTime.Add(time.Hour), 20))
	if err != nil || len(records) != len(instants) {
		t.Fatalf("claimed %d (%v), want %d", len(records), err, len(instants))
	}

	for index := 1; index < len(records); index++ {
		if records[index].OccurredAt.Before(records[index-1].OccurredAt) {
			t.Fatalf("event %d occurred at %v, before the previous one at %v: Claim must return them in order",
				index, records[index].OccurredAt, records[index-1].OccurredAt)
		}
	}
}
