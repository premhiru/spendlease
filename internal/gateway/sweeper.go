package gateway

import (
	"context"
	"log/slog"
	"time"
)

// DefaultReservationSweepInterval controls how quickly abandoned holds are
// reclaimed without turning the datastore into a polling loop.
const DefaultReservationSweepInterval = 30 * time.Second

// ReservationExpirer is the store slice used by the background sweeper.
type ReservationExpirer interface {
	// ExpirePendingReservations reclaims holds whose TTL has elapsed.
	ExpirePendingReservations(ctx context.Context, now time.Time) (int, error)
}

// StartReservationSweeper expires stale holds immediately and then at every
// interval until ctx is cancelled.
func StartReservationSweeper(
	ctx context.Context,
	store ReservationExpirer,
	interval time.Duration,
	logger *slog.Logger,
) {
	if interval <= 0 {
		interval = DefaultReservationSweepInterval
	}
	go func() {
		sweep := func() {
			n, err := store.ExpirePendingReservations(ctx, time.Now())
			if err != nil {
				if ctx.Err() == nil {
					logger.Error("could not expire reservations", "error", err)
				}
				return
			}
			if n > 0 {
				logger.Info("expired abandoned reservations", "count", n)
			}
		}

		sweep()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
