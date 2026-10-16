//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

func assertConstraint(t *testing.T, err error, want string) {
	t.Helper()

	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.ConstraintName != want {
		t.Fatalf("error = %v, want constraint %s", err, want)
	}
}

func (f *fixture) newBetFor(t *testing.T, target *wallet.Wallet, externalID string) *wager.Transaction {
	t.Helper()

	return f.newExternal(t, f.externalParams(t, target, wager.Bet, externalID, 2500, baseTime))
}

func (f *fixture) newReversalFor(
	t *testing.T,
	target *wallet.Wallet,
	kind wager.Kind,
	externalID string,
	reference string,
) *wager.Transaction {
	t.Helper()

	params := f.externalParams(t, target, kind, externalID, 2500, baseTime)
	params.ReferenceExternalTransactionID = reference

	return f.newExternal(t, params)
}

func TestOpeningTransactionRoundTrip(t *testing.T) {
	f := newFixture(t)
	created := f.openWallet(t, 100000)

	var opening *wager.Transaction
	rows, err := f.pool.Query(
		context.Background(),
		`SELECT id::text FROM wager_transactions WHERE wallet_id = $1`,
		created.ID().String(),
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("opening transaction not found")
	}

	var id string
	if err := rows.Scan(&id); err != nil {
		t.Fatalf("scan: %v", err)
	}

	rows.Close()

	opening, err = f.transactions.FindByID(context.Background(), identity.TransactionID(id))
	if err != nil {
		t.Fatalf("FindByID error = %v", err)
	}

	snapshot := opening.Snapshot()
	if snapshot.Kind != wager.Opening || snapshot.Status != wager.Processed {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	if snapshot.ProviderID != "" || snapshot.ResultBalance.Amount() != "1000.00" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestExternalTransactionRoundTripAndLookups(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	refund := f.newReversalFor(t, target, wager.Refund, "refund-1", "bet-1")
	f.insertTransaction(t, refund)
	ctx := context.Background()

	byID, err := f.transactions.FindByID(ctx, refund.ID())
	if err != nil {
		t.Fatalf("FindByID error = %v", err)
	}

	byExternal, externalErr := f.transactions.FindByProviderExternalID(ctx, "provider-a", "refund-1")
	byKey, keyErr := f.transactions.FindByIdempotencyKey(ctx, "provider-a", "provider-a:refund-1")
	if externalErr != nil || keyErr != nil {
		t.Fatalf("lookup errors = %v, %v", externalErr, keyErr)
	}

	want := refund.Snapshot()
	for _, got := range []*wager.Transaction{byID, byExternal, byKey} {
		snapshot := got.Snapshot()
		if snapshot.ID != want.ID || snapshot.ReferenceExternalTransactionID != "bet-1" ||
			snapshot.Status != wager.Pending || !snapshot.Money.Equal(want.Money) ||
			snapshot.PayloadHash != want.PayloadHash {
			t.Fatalf("snapshot = %+v, want %+v", snapshot, want)
		}
	}
}

func TestTransactionLookupsNotFound(t *testing.T) {
	f := newFixture(t)
	missing, _ := f.ids.NewTransactionID()
	ctx := context.Background()

	_, byID := f.transactions.FindByID(ctx, missing)
	_, byExternal := f.transactions.FindByProviderExternalID(ctx, "provider-a", "nope")
	_, byKey := f.transactions.FindByIdempotencyKey(ctx, "provider-a", "nope")

	for _, err := range []error{byID, byExternal, byKey} {
		if !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("error = %v, want %v", err, port.ErrNotFound)
		}
	}
}

func TestDuplicateIdempotencyKeyAndExternalID(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	f.insertTransaction(t, f.newBetFor(t, target, "bet-1"))
	ctx := context.Background()

	sameKey := f.newBetFor(t, target, "bet-2")
	sameKeyParams := sameKey.Snapshot()
	sameKeyParams.IdempotencyKey = "provider-a:bet-1"
	rebuilt, err := wager.Rehydrate(sameKeyParams)
	if err != nil {
		t.Fatalf("rehydrate error = %v", err)
	}

	keyErr := f.transactions.Insert(ctx, rebuilt)
	externalErr := f.transactions.Insert(ctx, f.newBetForKey(t, target, "bet-1", "provider-a:other"))

	if !errors.Is(keyErr, port.ErrAlreadyExists) || !errors.Is(externalErr, port.ErrAlreadyExists) {
		t.Fatalf("errors = %v, %v", keyErr, externalErr)
	}

	assertConstraint(t, keyErr, "wager_transactions_idempotency_key_unique")
	assertConstraint(t, externalErr, "wager_transactions_external_id_unique")
}

func (f *fixture) newBetForKey(
	t *testing.T,
	target *wallet.Wallet,
	externalID string,
	key string,
) *wager.Transaction {
	t.Helper()

	params := f.externalParams(t, target, wager.Bet, externalID, 2500, baseTime)
	params.IdempotencyKey = key

	return f.newExternal(t, params)
}

func TestUpdatePersistsOutcomeAndTerminalRowIsFrozen(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	bet := f.newBetFor(t, target, "bet-1")
	f.insertTransaction(t, bet)
	ctx := context.Background()

	later := baseTime.Add(time.Minute)
	if err := bet.MarkProcessed(wager.ProcessedResult{Balance: brl(t, 97500)}, later); err != nil {
		t.Fatalf("MarkProcessed error = %v", err)
	}

	if err := f.transactions.Update(ctx, bet); err != nil {
		t.Fatalf("Update error = %v", err)
	}

	stored, _ := f.transactions.FindByID(ctx, bet.ID())
	if stored.Status() != wager.Processed || stored.ResultBalance().Amount() != "975.00" {
		t.Fatalf("stored = %+v", stored.Snapshot())
	}

	_, err := f.pool.Exec(
		ctx,
		`UPDATE wager_transactions SET failure_code = 'AMOUNT_MISMATCH' WHERE id = $1`,
		bet.ID().String(),
	)
	if err == nil || errors.Is(err, port.ErrTransient) {
		t.Fatalf("terminal row must be immutable and permanent, error = %v", err)
	}
}

func TestUpdateOfUnknownTransactionIsNotFound(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)

	err := f.transactions.Update(context.Background(), f.newBetFor(t, target, "never-inserted"))

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, port.ErrNotFound)
	}
}

func (f *fixture) processReversal(
	t *testing.T,
	reversal *wager.Transaction,
	reference *wager.Transaction,
) error {
	t.Helper()

	err := reversal.MarkProcessed(
		wager.ProcessedResult{Balance: brl(t, 100000), ResolvedReferenceID: reference.ID()},
		baseTime.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("MarkProcessed error = %v", err)
	}

	return f.transactions.Update(context.Background(), reversal)
}

func TestOneProcessedReversalPerReference(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	bet := f.newBetFor(t, target, "bet-1")
	refund := f.newReversalFor(t, target, wager.Refund, "refund-1", "bet-1")
	rollback := f.newReversalFor(t, target, wager.Rollback, "rollback-1", "bet-1")
	for _, transaction := range []*wager.Transaction{bet, refund, rollback} {
		f.insertTransaction(t, transaction)
	}

	ctx := context.Background()
	before, err := f.transactions.HasProcessedReversal(ctx, bet.ID())
	if err != nil || before {
		t.Fatalf("before = %v (%v), want false", before, err)
	}

	if err := f.processReversal(t, refund, bet); err != nil {
		t.Fatalf("first reversal error = %v", err)
	}

	after, err := f.transactions.HasProcessedReversal(ctx, bet.ID())
	if err != nil || !after {
		t.Fatalf("after = %v (%v), want true", after, err)
	}

	second := f.processReversal(t, rollback, bet)

	if !errors.Is(second, port.ErrAlreadyExists) {
		t.Fatalf("second reversal error = %v, want %v", second, port.ErrAlreadyExists)
	}

	assertConstraint(t, second, "wager_transactions_one_reversal_per_reference")
}

func ids(transactions []*wager.Transaction) []string {
	result := make([]string, 0, len(transactions))
	for _, transaction := range transactions {
		result = append(result, transaction.ID().String())
	}

	sort.Strings(result)

	return result
}

func TestClaimPendingSkipsLockedRowsAndRecoversExpiredLeases(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	for _, name := range []string{"b1", "b2", "b3", "b4", "b5"} {
		f.insertTransaction(t, f.newBetFor(t, target, name))
	}

	params := port.ClaimParams{Limit: 5, Now: baseTime.Add(time.Hour), Lease: time.Minute}
	first, second := f.claimConcurrently(t, params)

	if len(first) != 3 || len(second) != 2 {
		t.Fatalf("claimed %d and %d, want 3 and 2 disjoint", len(first), len(second))
	}

	seen := map[string]bool{}
	for _, id := range append(ids(first), ids(second)...) {
		if seen[id] {
			t.Fatalf("%s claimed twice", id)
		}

		seen[id] = true
	}

	f.assertClaimWindows(t, params)
}

// claimConcurrently mantém a primeira transação aberta (linhas travadas) enquanto a
// segunda reivindica; devolve o que cada uma recebeu.
func (f *fixture) claimConcurrently(
	t *testing.T,
	params port.ClaimParams,
) ([]*wager.Transaction, []*wager.Transaction) {
	t.Helper()

	var first, second []*wager.Transaction
	locked := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan error, 1)

	go func() {
		finished <- f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
			firstParams := params
			firstParams.Limit = 3

			var err error
			first, err = f.transactions.ClaimPending(ctx, firstParams)
			close(locked)
			<-done

			return err
		})
	}()

	<-locked
	err := f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		var err error
		second, err = f.transactions.ClaimPending(ctx, params)

		return err
	})
	close(done)

	if firstErr := <-finished; err != nil || firstErr != nil {
		t.Fatalf("claim errors = %v, %v", err, firstErr)
	}

	return first, second
}

func (f *fixture) assertClaimWindows(t *testing.T, params port.ClaimParams) {
	t.Helper()

	ctx := context.Background()

	whileLeased, err := f.transactions.ClaimPending(ctx, params)
	if err != nil || len(whileLeased) != 0 {
		t.Fatalf("claimed %d during active lease (%v), want 0", len(whileLeased), err)
	}

	params.Now = params.Now.Add(2 * time.Minute)
	recovered, err := f.transactions.ClaimPending(ctx, params)
	if err != nil || len(recovered) != 5 {
		t.Fatalf("recovered %d after lease expiry (%v), want 5", len(recovered), err)
	}
}

func TestClaimRejectsNonPositiveLimitOrLease(t *testing.T) {
	f := newFixture(t)

	_, limitErr := f.transactions.ClaimPending(context.Background(), port.ClaimParams{Lease: time.Minute})
	_, leaseErr := f.transactions.ClaimDuePendingReferences(context.Background(), port.ClaimParams{Limit: 1})

	if limitErr == nil || leaseErr == nil {
		t.Fatal("invalid claim params must be rejected")
	}
}

func TestPendingReferenceRetryBackoffAndClaim(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	refund := f.newReversalFor(t, target, wager.Refund, "refund-1", "missing-bet")
	f.insertTransaction(t, refund)
	ctx := context.Background()

	if err := refund.AwaitReference(baseTime); err != nil {
		t.Fatalf("AwaitReference error = %v", err)
	}

	if err := f.transactions.Update(ctx, refund); err != nil {
		t.Fatalf("Update error = %v", err)
	}

	firstAttempts, err := f.transactions.ScheduleReferenceRetry(ctx, refund.ID(), baseTime.Add(time.Minute))
	if err != nil || firstAttempts != 1 {
		t.Fatalf("attempts = %d (%v), want 1", firstAttempts, err)
	}

	params := port.ClaimParams{Limit: 10, Now: baseTime, Lease: time.Minute}
	early, err := f.transactions.ClaimDuePendingReferences(ctx, params)
	if err != nil || len(early) != 0 {
		t.Fatalf("claimed %d before retry time (%v), want 0", len(early), err)
	}

	params.Now = baseTime.Add(2 * time.Minute)
	due, err := f.transactions.ClaimDuePendingReferences(ctx, params)
	if err != nil || len(due) != 1 || due[0].Status() != wager.PendingReference {
		t.Fatalf("due = %d (%v), want the pending reference", len(due), err)
	}

	secondAttempts, err := f.transactions.ScheduleReferenceRetry(ctx, refund.ID(), baseTime.Add(time.Hour))
	if err != nil || secondAttempts != 2 {
		t.Fatalf("attempts = %d (%v), want 2", secondAttempts, err)
	}
}

func TestScheduleRetryOnUnknownTransactionIsNotFound(t *testing.T) {
	f := newFixture(t)
	missing, _ := f.ids.NewTransactionID()

	_, err := f.transactions.ScheduleReferenceRetry(context.Background(), missing, baseTime)

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, port.ErrNotFound)
	}
}

func TestCorrelationMetadataRoundTrip(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	params := f.externalParams(t, target, wager.Bet, "bet-1", 2500, baseTime)
	params.CorrelationID = "correlation-1"
	params.CausationID = "message-1"
	bet := f.newExternal(t, params)
	f.insertTransaction(t, bet)

	stored, err := f.transactions.FindByID(context.Background(), bet.ID())

	if err != nil || stored.CorrelationID() != "correlation-1" || stored.CausationID() != "message-1" {
		t.Fatalf("stored = %+v (%v)", stored.Snapshot(), err)
	}
}

func TestReferenceAttemptsFollowTheSchedule(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	refund := f.newReversalFor(t, target, wager.Refund, "refund-1", "missing-bet")
	f.insertTransaction(t, refund)
	ctx := context.Background()

	before, err := f.transactions.ReferenceAttempts(ctx, refund.ID())
	if err != nil || before != 0 {
		t.Fatalf("attempts before = %d (%v), want 0", before, err)
	}

	if err := refund.AwaitReference(baseTime); err != nil {
		t.Fatalf("AwaitReference error = %v", err)
	}

	if err := f.transactions.Update(ctx, refund); err != nil {
		t.Fatalf("Update error = %v", err)
	}

	if _, err := f.transactions.ScheduleReferenceRetry(ctx, refund.ID(), baseTime); err != nil {
		t.Fatalf("Schedule error = %v", err)
	}

	after, err := f.transactions.ReferenceAttempts(ctx, refund.ID())
	if err != nil || after != 1 {
		t.Fatalf("attempts after = %d (%v), want 1", after, err)
	}

	missing, _ := f.ids.NewTransactionID()
	if _, err := f.transactions.ReferenceAttempts(ctx, missing); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("unknown error = %v, want %v", err, port.ErrNotFound)
	}
}
