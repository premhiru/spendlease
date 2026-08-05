package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/vault"
)

func TestPostgresMultiInstanceGuarantees(t *testing.T) {
	dsn := os.Getenv("SPENDLEASE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SPENDLEASE_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Starting two stores together exercises the migration advisory lock on a
	// fresh CI database as well as ordinary repeat startup locally.
	stores := make([]*Store, 2)
	errs := make(chan error, 2)
	var openWG sync.WaitGroup
	for i := range stores {
		openWG.Add(1)
		go func(i int) {
			defer openWG.Done()
			st, err := Open(ctx, dsn, Options{MaxOpenConns: 8})
			stores[i] = st
			errs <- err
		}(i)
	}
	openWG.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	for _, st := range stores {
		defer func(st *Store) { _ = st.Close() }(st)
	}

	operatorNow := time.Now().UTC()
	operators := make([]operator.Operator, 2)
	for i := range operators {
		_, hash := operator.NewToken()
		operators[i] = operator.Operator{
			ID: operator.NewOperatorID(), Name: fmt.Sprintf("postgres-admin-%d-%s", i, operator.NewOperatorID()),
			TokenHash: hash, Role: operator.RoleAdmin, CreatedAt: operatorNow, UpdatedAt: operatorNow,
		}
		if err := stores[i].CreateOperator(ctx, operators[i]); err != nil {
			t.Fatalf("CreateOperator: %v", err)
		}
	}
	disableErrs := make(chan error, 2)
	for i := range operators {
		go func(i int) { disableErrs <- stores[i].DisableOperator(ctx, operators[i].ID, time.Now().UTC()) }(i)
	}
	var disabled, protected int
	for range operators {
		err := <-disableErrs
		switch {
		case err == nil:
			disabled++
		case errors.Is(err, store.ErrConflict):
			protected++
		default:
			t.Fatalf("DisableOperator: %v", err)
		}
	}
	if disabled != 1 || protected != 1 {
		t.Fatalf("concurrent final-admin result disabled/protected = %d/%d", disabled, protected)
	}
	audit := operator.AuditRecord{
		ID: operator.NewAuditID(), RequestID: operator.NewAuditID(), ActorID: "postgres-test", ActorName: "postgres-test",
		Role: operator.RoleAdmin, Phase: "result", Action: "integration", Resource: "postgres",
		StatusCode: 200, CreatedAt: time.Now().UTC(),
	}
	if err := stores[0].AppendOperatorAudit(ctx, audit); err != nil {
		t.Fatalf("AppendOperatorAudit: %v", err)
	}
	if records, err := stores[1].ListOperatorAudit(ctx, operator.AuditFilter{ActorID: "postgres-test"}); err != nil || len(records) != 1 {
		t.Fatalf("ListOperatorAudit = (%d, %v)", len(records), err)
	}

	_, keyHash := store.NewPrincipalKey()
	p := store.Principal{
		ID: store.NewPrincipalID(), Name: "postgres-" + store.NewPrincipalID(),
		KeyHash: keyHash, Mode: store.ModeEnforce, CreatedAt: time.Now().UTC(),
	}
	if err := stores[0].CreatePrincipal(ctx, p); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if err := stores[1].CreatePrincipal(ctx, p); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate CreatePrincipal error = %v, want ErrConflict", err)
	}
	if _, err := stores[1].GetPrincipal(ctx, "prn_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing GetPrincipal error = %v, want ErrNotFound", err)
	}
	_, bundlePrincipalHash := store.NewPrincipalKey()
	_, bundleLeaseHash := store.NewLeaseToken()
	bundlePrincipal := store.Principal{
		ID: store.NewPrincipalID(), Name: "postgres-bundle-" + store.NewPrincipalID(),
		KeyHash: bundlePrincipalHash, Mode: store.ModeObserve, CreatedAt: time.Now().UTC(),
	}
	bundleRun := store.Run{
		ID: store.NewRunID(), PrincipalID: bundlePrincipal.ID, Budget: money.MustParseUSD("0.50"),
		Status: store.RunActive, CreatedAt: time.Now().UTC(),
	}
	bundleLease := store.Lease{
		ID: store.NewLeaseID(), RunID: bundleRun.ID, TokenHash: bundleLeaseHash, Providers: []string{"openai"},
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now().UTC(),
	}
	if err := stores[0].CreatePrincipalRunLease(ctx, bundlePrincipal, bundleRun, bundleLease); err != nil {
		t.Fatalf("CreatePrincipalRunLease: %v", err)
	}
	if got, err := stores[1].GetLease(ctx, bundleLease.ID); err != nil || got.RunID != bundleRun.ID {
		t.Fatalf("bundled lease = (%+v, %v)", got, err)
	}
	r := store.Run{
		ID: store.NewRunID(), PrincipalID: p.ID, Budget: money.MustParseUSD("1.00"),
		Status: store.RunActive, CreatedAt: time.Now().UTC(),
	}
	if err := stores[0].CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	const reservationRequests = 20
	type reservationResult struct {
		allowed bool
		id      string
	}
	allowed := make(chan reservationResult, reservationRequests)
	reserveErrs := make(chan error, reservationRequests)
	var reserveWG sync.WaitGroup
	for i := range reservationRequests {
		reserveWG.Add(1)
		go func(i int) {
			defer reserveWG.Done()
			reservation := store.Reservation{
				ID: store.NewReservationID(), RunID: r.ID,
				Amount: money.MustParseUSD("0.10"), Status: store.ReservationPending,
				ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now().UTC(),
			}
			decision, err := stores[i%len(stores)].TryReserve(ctx, reservation, true)
			if err != nil {
				reserveErrs <- err
				return
			}
			allowed <- reservationResult{allowed: decision.Allowed, id: reservation.ID}
		}(i)
	}
	reserveWG.Wait()
	close(allowed)
	close(reserveErrs)
	for err := range reserveErrs {
		t.Errorf("TryReserve: %v", err)
	}
	allowedCount := 0
	var reservationID string
	for decision := range allowed {
		if decision.allowed {
			allowedCount++
			reservationID = decision.id
		}
	}
	if allowedCount != 10 {
		t.Fatalf("allowed %d reservations, want exactly 10", allowedCount)
	}
	if held, err := stores[1].PendingReservationTotal(ctx, r.ID); err != nil || held != money.MustParseUSD("1.00") {
		t.Fatalf("pending total = (%s, %v), want 1.00", held, err)
	}

	settlement := ledger.Entry{
		RunID: r.ID, PrincipalID: p.ID, Provider: "openai", Model: "settled",
		InputTokens: 25, OutputTokens: 10, Cost: money.MustParseUSD("0.05"), CreatedAt: time.Now().UTC(),
	}
	settled := make(chan ledger.Entry, 2)
	settleErrs := make(chan error, 2)
	var settleWG sync.WaitGroup
	for i := range stores {
		settleWG.Add(1)
		go func(i int) {
			defer settleWG.Done()
			entry, err := stores[i].SettleReservation(ctx, reservationID, settlement)
			settled <- entry
			settleErrs <- err
		}(i)
	}
	settleWG.Wait()
	close(settled)
	close(settleErrs)
	for err := range settleErrs {
		if err != nil {
			t.Fatalf("SettleReservation: %v", err)
		}
	}
	var firstSettlement ledger.Entry
	for entry := range settled {
		if firstSettlement.Seq == 0 {
			firstSettlement = entry
			continue
		}
		if entry.Seq != firstSettlement.Seq || entry.Hash != firstSettlement.Hash {
			t.Fatalf("idempotent settlement returned different entries: %+v and %+v", firstSettlement, entry)
		}
	}

	const entries = 24
	appendErrs := make(chan error, entries)
	var appendWG sync.WaitGroup
	for i := range entries {
		appendWG.Add(1)
		go func(i int) {
			defer appendWG.Done()
			_, err := stores[i%len(stores)].AppendLedger(ctx, ledger.Entry{
				RunID: r.ID, PrincipalID: p.ID, Provider: "openai",
				Model: fmt.Sprintf("concurrent-%02d", i), InputTokens: 10,
				OutputTokens: 5, Cost: money.MustParseUSD("0.001"), CreatedAt: time.Now().UTC(),
				ExternalID: fmt.Sprintf("req_%02d", i), PricingRevision: "integration-test",
			})
			if err != nil {
				appendErrs <- err
			}
		}(i)
	}
	appendWG.Wait()
	close(appendErrs)
	for err := range appendErrs {
		t.Errorf("AppendLedger: %v", err)
	}
	written, err := stores[0].LedgerEntries(ctx, store.LedgerFilter{})
	if err != nil {
		t.Fatalf("LedgerEntries: %v", err)
	}
	if err := ledger.VerifyChain(written); err != nil {
		t.Fatalf("VerifyChain after cross-instance appends: %v", err)
	}
	if len(written) < entries {
		t.Fatalf("ledger has %d entries, want at least %d", len(written), entries)
	}
	foundItemized := false
	for _, entry := range written {
		if entry.Model != "concurrent-00" {
			continue
		}
		usage := entry.ItemizedUsage()
		if entry.HashVersion != ledger.CurrentHashVersion || usage[billing.UnitInputTokens] != 10 ||
			usage[billing.UnitOutputTokens] != 5 || entry.ExternalID != "req_00" ||
			entry.PricingRevision != "integration-test" {
			t.Fatalf("PostgreSQL itemized ledger round trip = %+v", entry)
		}
		foundItemized = true
	}
	if !foundItemized {
		t.Fatal("PostgreSQL itemized ledger round trip entry not found")
	}

	lease := store.Lease{
		ID: store.NewLeaseID(), RunID: r.ID, TokenHash: store.HashSecret(store.NewLeaseID()),
		Providers: []string{"openai"}, Ceiling: money.MustParseUSD("1.00"),
		ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now().UTC(),
	}
	if err := stores[0].CreateLease(ctx, lease); err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if err := stores[1].RevokeLease(ctx, lease.ID, time.Now().UTC()); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	events, err := stores[0].RecentOperationalEvents(ctx, store.OperationalEventFilter{Limit: 5}, time.Now())
	if err != nil {
		t.Fatalf("RecentOperationalEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("RecentOperationalEvents returned no rows")
	}
	if summaries, err := stores[1].PrincipalSummaries(ctx); err != nil || len(summaries) == 0 {
		t.Fatalf("PrincipalSummaries = (%d rows, %v)", len(summaries), err)
	}
	if summaries, err := stores[1].RunSummaries(ctx, p.ID); err != nil || len(summaries) != 1 {
		t.Fatalf("RunSummaries = (%d rows, %v), want one", len(summaries), err)
	}

	now := time.Now().UTC()
	credential := vault.Credential{
		Provider: "integration-" + store.NewLeaseID(), Nonce: []byte("0123456789ab"),
		Ciphertext: []byte("encrypted"), CreatedAt: now, UpdatedAt: now,
	}
	if err := stores[0].PutCredential(ctx, credential); err != nil {
		t.Fatalf("PutCredential: %v", err)
	}
	if got, err := stores[1].GetCredential(ctx, credential.Provider); err != nil || string(got.Ciphertext) != "encrypted" {
		t.Fatalf("GetCredential = (%+v, %v)", got, err)
	}
	if count, err := stores[1].RotateCredentials(ctx, func(current vault.Credential) (vault.Credential, error) {
		if current.Provider == credential.Provider {
			current.Ciphertext = []byte("rotated")
		}
		return current, nil
	}); err != nil || count == 0 {
		t.Fatalf("RotateCredentials = (%d, %v)", count, err)
	}
	if got, err := stores[0].GetCredential(ctx, credential.Provider); err != nil || string(got.Ciphertext) != "rotated" {
		t.Fatalf("rotated GetCredential = (%+v, %v)", got, err)
	}

	// The append-only guarantee must be enforced by PostgreSQL, independently
	// of the Go API.
	raw, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	defer func() { _ = raw.Close(ctx) }()
	if _, err := raw.Exec(ctx, `UPDATE ledger SET model = 'tampered' WHERE seq = $1`, written[0].Seq); err == nil {
		t.Fatal("PostgreSQL allowed a ledger UPDATE")
	}
	if _, err := raw.Exec(ctx, `DELETE FROM ledger WHERE seq = $1`, written[0].Seq); err == nil {
		t.Fatal("PostgreSQL allowed a ledger DELETE")
	}
}
