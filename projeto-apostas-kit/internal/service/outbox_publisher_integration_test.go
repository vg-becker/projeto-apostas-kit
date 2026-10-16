//go:build integration

package service_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

var (
	errDestinationDown = errors.New("destination down")
	errConfirmation    = errors.New("confirmation lost")

	publishPolicy = service.PublishPolicy{
		BatchSize:      100,
		Lease:          30 * time.Second,
		InitialBackoff: 2 * time.Second,
		MaxBackoff:     8 * time.Second,
	}
)

// recordingDestination é só o destino de saída: registra o que recebeu e pode recusar.
type recordingDestination struct {
	mutex     sync.Mutex
	delivered []port.OutboxRecord
	down      bool
	afterEach func()
}

func (destination *recordingDestination) Publish(_ context.Context, record port.OutboxRecord) error {
	destination.mutex.Lock()
	defer destination.mutex.Unlock()

	if destination.down {
		return errDestinationDown
	}

	destination.delivered = append(destination.delivered, record)
	if destination.afterEach != nil {
		destination.afterEach()
	}

	return nil
}

func (destination *recordingDestination) setDown(down bool) {
	destination.mutex.Lock()
	defer destination.mutex.Unlock()

	destination.down = down
}

func (destination *recordingDestination) deliveriesOf(id identity.EventID) int {
	destination.mutex.Lock()
	defer destination.mutex.Unlock()

	total := 0
	for _, record := range destination.delivered {
		if record.EventID == id {
			total++
		}
	}

	return total
}

func (destination *recordingDestination) total() int {
	destination.mutex.Lock()
	defer destination.mutex.Unlock()

	return len(destination.delivered)
}

// lossyConfirmation entrega ao repositório real, mas perde a confirmação das primeiras
// chamadas de MarkPublished, como se o processo caísse depois de entregar.
type lossyConfirmation struct {
	port.OutboxRepository
	mutex sync.Mutex
	lose  int
}

func (outbox *lossyConfirmation) MarkPublished(
	ctx context.Context,
	id identity.EventID,
	now time.Time,
) error {
	outbox.mutex.Lock()
	defer outbox.mutex.Unlock()

	if outbox.lose > 0 {
		outbox.lose--

		return errConfirmation
	}

	return outbox.OutboxRepository.MarkPublished(ctx, id, now)
}

func (h *harness) outboxEventIDs(t *testing.T) []string {
	t.Helper()

	rows, err := h.pool.Query(context.Background(), `SELECT event_id::text FROM outbox_events ORDER BY 1`)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}

		ids = append(ids, id)
	}

	return ids
}

func (h *harness) unpublished(t *testing.T) int {
	t.Helper()

	return h.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`)
}

func (h *harness) publishOnce(t *testing.T, publisher *service.OutboxPublisher) service.PublishSummary {
	t.Helper()

	summary, err := publisher.PublishPending(context.Background())
	if err != nil {
		t.Fatalf("PublishPending error = %v", err)
	}

	return summary
}

func TestEveryOutboxEventIsPublishedOnceWithItsStoredPayload(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	total := len(h.outboxEventIDs(t))
	destination := &recordingDestination{}
	publisher := h.newPublisher(destination, h.outbox, publishPolicy)

	summary := h.publishOnce(t, publisher)

	if summary.Claimed != total || summary.Published != total || destination.total() != total {
		t.Fatalf("summary = %+v, delivered = %d, want %d", summary, destination.total(), total)
	}

	if h.unpublished(t) != 0 {
		t.Fatal("every delivered event must be confirmed in the outbox")
	}

	for _, record := range destination.delivered {
		var stored string
		row := h.pool.QueryRow(
			context.Background(),
			`SELECT payload::text FROM outbox_events WHERE event_id = $1`,
			record.EventID.String(),
		)
		if err := row.Scan(&stored); err != nil || stored != string(record.Payload) {
			t.Fatalf("event %s delivered payload differs from the stored one (%v)", record.EventID, err)
		}
	}

	if again := h.publishOnce(t, publisher); again.Claimed != 0 {
		t.Fatalf("second cycle = %+v, nothing is left to publish", again)
	}
}

func TestDestinationFailuresBackOffWithoutLosingEvents(t *testing.T) {
	h := newHarness(t)
	h.openFunded(t, 100000)
	total := len(h.outboxEventIDs(t))
	destination := &recordingDestination{down: true}
	publisher := h.newPublisher(destination, h.outbox, publishPolicy)
	ctx := context.Background()

	summary, err := publisher.PublishPending(ctx)
	if err == nil || summary.Failed != total || summary.Published != 0 {
		t.Fatalf("summary = %+v (%v), want every delivery refused", summary, err)
	}

	h.assertOutboxBackoff(t, 1, 2*time.Second)

	if immediate := h.publishOnce(t, publisher); immediate.Claimed != 0 {
		t.Fatalf("summary = %+v, nothing is due before the backoff", immediate)
	}

	h.clock.Advance(2 * time.Second)
	_, _ = publisher.PublishPending(ctx)
	h.assertOutboxBackoff(t, 2, 4*time.Second)

	h.clock.Advance(4 * time.Second)
	destination.setDown(false)
	recovered := h.publishOnce(t, publisher)

	if recovered.Published != total || h.unpublished(t) != 0 {
		t.Fatalf("recovered = %+v, no event may be lost", recovered)
	}

	for _, id := range h.outboxEventIDs(t) {
		if destination.deliveriesOf(identity.EventID(id)) != 1 {
			t.Fatalf("event %s was delivered %d times", id, destination.deliveriesOf(identity.EventID(id)))
		}
	}
}

func (h *harness) assertOutboxBackoff(t *testing.T, attempts int, delay time.Duration) {
	t.Helper()

	want := h.clock.Now().Add(delay)

	matching := h.count(
		t,
		`SELECT count(*) FROM outbox_events
		  WHERE published_at IS NULL AND attempts = $1 AND next_attempt_at = $2`,
		attempts,
		want,
	)
	if matching != len(h.outboxEventIDs(t)) {
		t.Fatalf("%d events at %d attempts / %v, want all of them", matching, attempts, want)
	}
}

func TestCrashBetweenDeliveryAndConfirmationRepublishesWithTheSameEventID(t *testing.T) {
	h := newHarness(t)
	h.openFunded(t, 100000)
	total := len(h.outboxEventIDs(t))
	destination := &recordingDestination{}
	crashing := &lossyConfirmation{OutboxRepository: h.outbox, lose: total}

	summary, err := h.newPublisher(destination, crashing, publishPolicy).PublishPending(context.Background())

	if err == nil || summary.Unconfirmed != total || destination.total() != total || h.unpublished(t) != total {
		t.Fatalf("summary = %+v (%v), delivered = %d", summary, err, destination.total())
	}

	survivor := h.newPublisher(destination, h.outbox, publishPolicy)
	if during := h.publishOnce(t, survivor); during.Claimed != 0 {
		t.Fatalf("summary = %+v, the lease must protect events still in flight", during)
	}

	h.clock.Advance(31 * time.Second)
	recovered := h.publishOnce(t, survivor)

	if recovered.Published != total || h.unpublished(t) != 0 {
		t.Fatalf("recovered = %+v", recovered)
	}

	for _, id := range h.outboxEventIDs(t) {
		if destination.deliveriesOf(identity.EventID(id)) != 2 {
			t.Fatalf("event %s delivered %d times, want 2 with the same event id", id, destination.deliveriesOf(identity.EventID(id)))
		}
	}
}

// drain publica em lotes pequenos até não restar nada reivindicável.
func drain(publisher *service.OutboxPublisher) error {
	for range 30 {
		summary, err := publisher.PublishPending(context.Background())
		if err != nil || summary.Claimed == 0 {
			return err
		}
	}

	return nil
}

func TestTwoPublishersCompeteWithoutDuplicatingDeliveries(t *testing.T) {
	h := newHarness(t)
	for range 10 {
		h.openFunded(t, 1000)
	}

	total := len(h.outboxEventIDs(t))
	destination := &recordingDestination{}
	smallBatches := publishPolicy
	smallBatches.BatchSize = 3

	errs := make([]error, 2)

	var group sync.WaitGroup
	for index := range errs {
		group.Add(1)
		go func() {
			defer group.Done()
			errs[index] = drain(h.newPublisher(destination, h.outbox, smallBatches))
		}()
	}

	group.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors = %v, %v", errs[0], errs[1])
	}

	if h.unpublished(t) != 0 {
		t.Fatalf("unpublished = %d, want 0", h.unpublished(t))
	}

	assertDeliveredExactlyOnce(t, destination, h.outboxEventIDs(t), total)
}

func assertDeliveredExactlyOnce(
	t *testing.T,
	destination *recordingDestination,
	stored []string,
	total int,
) {
	t.Helper()

	delivered := make([]string, 0, total)
	for _, record := range destination.delivered {
		delivered = append(delivered, record.EventID.String())
	}

	sort.Strings(delivered)

	if len(delivered) != total {
		t.Fatalf("delivered %d of %d events", len(delivered), total)
	}

	for index := range stored {
		if delivered[index] != stored[index] {
			t.Fatalf("delivery %d = %s, want %s: an event was duplicated or lost", index, delivered[index], stored[index])
		}
	}
}

func TestCancellationStopsTheBatchWithoutLosingEvents(t *testing.T) {
	h := newHarness(t)
	for range 3 {
		h.openFunded(t, 1000)
	}

	total := len(h.outboxEventIDs(t))
	ctx, cancel := context.WithCancel(context.Background())
	destination := &recordingDestination{afterEach: cancel}
	publisher := h.newPublisher(destination, h.outbox, publishPolicy)

	summary, err := publisher.PublishPending(ctx)

	if err == nil || summary.Published != 1 || destination.total() != 1 {
		t.Fatalf("summary = %+v (%v), want a clean stop after the first delivery", summary, err)
	}

	if h.unpublished(t) != total-1 {
		t.Fatalf("unpublished = %d, want %d: the delivered event must have been confirmed", h.unpublished(t), total-1)
	}

	h.clock.Advance(31 * time.Second)
	h.publishOnce(t, h.newPublisher(destination, h.outbox, publishPolicy))

	if destination.total() != total || h.unpublished(t) != 0 {
		t.Fatalf("delivered = %d of %d", destination.total(), total)
	}

	for _, id := range h.outboxEventIDs(t) {
		if destination.deliveriesOf(identity.EventID(id)) != 1 {
			t.Fatalf("event %s delivered %d times, want exactly once", id, destination.deliveriesOf(identity.EventID(id)))
		}
	}
}
