package gateway

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/billing"
	"github.com/premhiru/spendlease/internal/ledger"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/pricing"
	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/store"
)

// RunHeader lets a principal-key caller attribute a request to a run it
// created. Lease authentication always uses the run attached to the lease.
// A principal-key request without this header uses its implicit run.
const RunHeader = "X-Spendlease-Run"

// StreamUsageHeader appears on a streamed response whose request spendlease
// modified to enable usage reporting.
//
// The modification is necessary — an OpenAI-compatible stream reports no
// usage unless asked, and unpriced spend is the thing this product exists to
// prevent — but changing somebody's request without telling them is not
// acceptable. This makes it visible from a single `curl -i`.
const StreamUsageHeader = "X-Spendlease-Stream-Options"

// AccountingHeader reports requests that observe mode forwarded without a
// trustworthy cost model. Enforce mode rejects the same request before egress.
const AccountingHeader = "X-Spendlease-Accounting"

// LedgerStore is the slice of the store the recorder needs.
type LedgerStore interface {
	// AppendLedger seals and persists a spend entry.
	AppendLedger(ctx context.Context, e ledger.Entry) (ledger.Entry, error)
	// GetRun resolves a run by ID.
	GetRun(ctx context.Context, id string) (store.Run, error)
	// CreateRun inserts a run.
	CreateRun(ctx context.Context, r store.Run) error
	// TryReserve makes the atomic pre-egress budget decision.
	TryReserve(ctx context.Context, r store.Reservation, enforce bool) (store.BudgetDecision, error)
	// RecordBudgetEvent makes a would-block decision visible after the request.
	RecordBudgetEvent(ctx context.Context, e store.BudgetEvent) error
	// ResolveReservation releases a failed request's hold.
	ResolveReservation(ctx context.Context, id string, status store.ReservationStatus, at time.Time) error
	// SettleReservation records actual spend and resolves the hold atomically.
	SettleReservation(ctx context.Context, id string, e ledger.Entry) (ledger.Entry, error)
}

// Recorder prices completed requests and appends them to the ledger.
type Recorder struct {
	store  LedgerStore
	book   *pricing.Book
	logger *slog.Logger

	// defaultRunBudget is given to an implicit run and enforced when the
	// principal is in enforce mode.
	defaultRunBudget money.Nanos
	reservationTTL   time.Duration
}

// DefaultReservationTTL bounds a hold left behind by a vanished request.
const DefaultReservationTTL = 15 * time.Minute

// NewRecorder returns a recorder.
func NewRecorder(
	st LedgerStore,
	book *pricing.Book,
	budget money.Nanos,
	logger *slog.Logger,
	reservationTTL ...time.Duration,
) *Recorder {
	ttl := DefaultReservationTTL
	if len(reservationTTL) > 0 && reservationTTL[0] > 0 {
		ttl = reservationTTL[0]
	}
	return &Recorder{
		store: st, book: book, logger: logger,
		defaultRunBudget: budget, reservationTTL: ttl,
	}
}

// observation is everything known about one completed request.
type observation struct {
	principal store.Principal
	provider  string
	runID     string

	request providers.RequestInfo
	usage   providers.Usage

	// usageReported is true when the vendor told us the token counts. False
	// means they were estimated locally and the entry must say so.
	usageReported bool
	// complete is false when the client disconnected before the response
	// finished.
	complete bool
	// upstreamOK is false for a non-2xx vendor response.
	upstreamOK bool
	// reservationID is the hold this completion resolves. Empty preserves the
	// direct-record path used by accounting unit tests.
	reservationID string
	// externalID is the upstream request identifier when the provider exposed
	// one in its response headers.
	externalID string
}

// Reserve estimates the request's upper bound and asks the store for one
// atomic budget decision.
func (r *Recorder) Reserve(
	ctx context.Context,
	p store.Principal,
	provider string,
	request providers.RequestInfo,
) (store.Reservation, store.BudgetDecision, error) {
	price, _ := r.book.Lookup(provider, request.Model, time.Now())
	input := pricing.ReservationInputTokens(request.RequestBytes)
	output := int64(0)
	if !request.NoOutput {
		output = pricing.ReservationTokens(request.MaxTokens, price)
	}
	amount := price.Cost(pricing.Usage{InputTokens: input, OutputTokens: output})
	now := time.Now()
	reservation := store.Reservation{
		ID:        store.NewReservationID(),
		RunID:     runIDFrom(ctx),
		LeaseID:   leaseIDFrom(ctx),
		Amount:    amount,
		Status:    store.ReservationPending,
		ExpiresAt: now.Add(r.reservationTTL),
		CreatedAt: now,
	}
	decision, err := r.store.TryReserve(ctx, reservation, p.Mode == store.ModeEnforce)
	if err == nil && decision.WouldBlock {
		event := store.BudgetEvent{
			PrincipalID: p.ID,
			RunID:       decision.RunID,
			LeaseID:     reservation.LeaseID,
			Provider:    provider,
			Model:       request.Model,
			Enforced:    !decision.Allowed,
			Requested:   decision.Requested,
			Remaining:   decision.Remaining,
			Shortfall:   decision.Shortfall,
			CreatedAt:   now,
		}
		if recordErr := r.store.RecordBudgetEvent(ctx, event); recordErr != nil {
			r.logger.Error("could not record budget decision",
				"principal", p.ID, "run", decision.RunID, "error", recordErr)
		}
	}
	return reservation, decision, err
}

// Release drops the full hold after a provider or transport failure.
func (r *Recorder) Release(ctx context.Context, reservationID string) {
	if reservationID == "" {
		return
	}
	if err := r.store.ResolveReservation(
		ctx, reservationID, store.ReservationReleased, time.Now(),
	); err != nil && !errors.Is(err, store.ErrConflict) {
		r.logger.Error("could not release reservation", "reservation", reservationID, "error", err)
	}
}

// Record prices an observation and appends a ledger entry.
//
// It never fails a request. Accounting runs after the response has been
// delivered, so an error here is logged and swallowed: refusing to serve
// traffic because the ledger is unavailable would make spendlease a liability
// rather than a safeguard. Enforcement, which does have to fail closed, is a
// separate concern that runs before the request.
func (r *Recorder) Record(ctx context.Context, obs observation) {
	// A vendor error is not spend. Providers do not charge for a failed
	// request, so neither does the ledger.
	if !obs.upstreamOK {
		r.logger.Debug("not charging a failed upstream request",
			"principal", obs.principal.ID, "provider", obs.provider, "model", obs.request.Model)
		return
	}

	model := obs.request.Model
	if model == "" {
		// There is no honest ledger entry to build without a model. Release the
		// reservation rather than leaving a hold behind until its TTL.
		r.Release(ctx, obs.reservationID)
		r.logger.Debug("not recording a request with no identifiable model",
			"principal", obs.principal.ID, "provider", obs.provider)
		return
	}

	now := time.Now()
	price, known := r.book.Lookup(obs.provider, model, now)
	priceBook := r.book.Metadata(now)

	usage := obs.usage
	estimated := !known || !obs.usageReported || !obs.complete

	if !obs.usageReported {
		// The vendor did not report usage. Estimate from what was sent and
		// what was allowed, which is an upper bound rather than a guess.
		usage = r.estimate(obs, price)
	}

	cost := price.Cost(pricing.Usage{
		InputTokens:        usage.InputTokens,
		CachedInputTokens:  usage.CachedInputTokens,
		CacheWrite5mTokens: usage.CacheWrite5mTokens,
		CacheWrite1hTokens: usage.CacheWrite1hTokens,
		OutputTokens:       usage.OutputTokens,
	})

	toAppend := ledger.Entry{
		RunID:        obs.runID,
		PrincipalID:  obs.principal.ID,
		Provider:     obs.provider,
		Model:        model,
		InputTokens:  usage.TotalInputTokens(),
		OutputTokens: usage.OutputTokens,
		Usage: billing.TokenUsage(
			usage.InputTokens,
			usage.CachedInputTokens,
			usage.CacheWrite5mTokens,
			usage.CacheWrite1hTokens,
			usage.OutputTokens,
		),
		ExternalID:      obs.externalID,
		PricingRevision: priceBook.Revision,
		PriceEffective:  price.Effective,
		Cost:            cost,
		Estimated:       estimated,
		CreatedAt:       now,
	}
	var entry ledger.Entry
	var err error
	if obs.reservationID != "" {
		entry, err = r.store.SettleReservation(ctx, obs.reservationID, toAppend)
	} else {
		entry, err = r.store.AppendLedger(ctx, toAppend)
	}
	if err != nil {
		r.logger.Error("could not record spend",
			"principal", obs.principal.ID, "run", obs.runID,
			"provider", obs.provider, "model", model, "error", err)
		return
	}

	attrs := []any{
		"seq", entry.Seq,
		"principal", obs.principal.ID,
		"run", obs.runID,
		"provider", obs.provider,
		"model", model,
		"input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens,
		"cost", cost.String(),
		"mode", string(obs.principal.Mode),
	}
	if estimated {
		attrs = append(attrs, "estimated", true, "why", estimateReason(known, obs))
	}
	r.logger.Info("recorded spend", attrs...)
}

// estimate produces token counts when the vendor reported none.
//
// Input comes from the prompt size actually sent. Output uses the request's
// own ceiling, or the model's default when it set none — an upper bound, on
// the principle that over-counting an unmeasured request is safer than
// under-counting it.
func (r *Recorder) estimate(obs observation, price pricing.Price) providers.Usage {
	in := pricing.EstimateFromChars(obs.request.PromptChars)
	out := pricing.ReservationTokens(obs.request.MaxTokens, price)

	// A disconnected stream produced some output, but how much is unknowable.
	// Charging the full ceiling for a call that was cut short would overstate
	// spend, so half is used and the entry is marked estimated.
	if !obs.complete {
		out /= 2
	}
	return providers.Usage{InputTokens: in.Tokens, OutputTokens: out}
}

// estimateReason explains why an entry is not exact, so the dashboard and the
// logs can say more than "estimated".
func estimateReason(priceKnown bool, obs observation) string {
	switch {
	case !priceKnown:
		return "model is not in the price book"
	case !obs.complete:
		return "client disconnected before the response finished"
	case !obs.usageReported && obs.request.Stream:
		return "vendor did not report usage on a streamed response"
	case !obs.usageReported:
		return "vendor did not report usage"
	default:
		return "unknown"
	}
}

// resolveRun returns the run a request should be charged to.
//
// A caller may name one with the run header. Otherwise the principal's
// implicit run is used, created on first use.
func (r *Recorder) resolveRun(ctx context.Context, p store.Principal, requested string) (string, error) {
	if requested != "" {
		run, err := r.store.GetRun(ctx, requested)
		if err != nil {
			return "", err
		}
		if run.PrincipalID != p.ID {
			// Charging one principal's spend to another principal's run would
			// corrupt attribution, which is the one thing this must not do.
			return "", errors.New("that run belongs to a different principal")
		}
		if run.Status != store.RunActive {
			return "", errors.New("that run is closed")
		}
		return run.ID, nil
	}
	return r.implicitRun(ctx, p)
}

// implicitRun returns the principal's default run, creating it if needed.
//
// The identifier is derived from the principal's, so it is stable without a
// lookup and obviously related when it appears in a log line or a bug report.
func (r *Recorder) implicitRun(ctx context.Context, p store.Principal) (string, error) {
	id := implicitRunID(p.ID)

	if existing, err := r.store.GetRun(ctx, id); err == nil {
		if existing.Status != store.RunActive {
			return "", errors.New("the principal's implicit run is closed")
		}
		return id, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	err := r.store.CreateRun(ctx, store.Run{
		ID:          id,
		PrincipalID: p.ID,
		Budget:      r.defaultRunBudget,
		Status:      store.RunActive,
		CreatedAt:   time.Now(),
	})
	// Two concurrent first requests race here. The loser sees a conflict,
	// which means the run now exists, which is all the caller needed.
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return "", err
	}
	return id, nil
}

// implicitRunID derives a principal's default run identifier.
func implicitRunID(principalID string) string {
	return store.RunPrefix + strings.TrimPrefix(principalID, store.PrincipalPrefix)
}
