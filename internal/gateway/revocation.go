package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/premhiru/spendlease/internal/store"
)

// RevocationSet is the in-memory fast path checked on every lease request.
type RevocationSet struct {
	mu     sync.RWMutex
	hashes map[string]struct{}
}

// NewRevocationSet returns an empty revocation set.
func NewRevocationSet() *RevocationSet { return &RevocationSet{hashes: map[string]struct{}{}} }

// Revoke makes a token hash unusable immediately.
func (s *RevocationSet) Revoke(hash string) {
	s.mu.Lock()
	s.hashes[hash] = struct{}{}
	s.mu.Unlock()
}

// Revoked reports whether a token hash was killed in this process.
func (s *RevocationSet) Revoked(hash string) bool {
	s.mu.RLock()
	_, ok := s.hashes[hash]
	s.mu.RUnlock()
	return ok
}

// RevocationStore is the persistence slice used by the kill switch.
type RevocationStore interface {
	GetLease(context.Context, string) (store.Lease, error)
	ListRunsByPrincipal(context.Context, string) ([]store.Run, error)
	ListLeasesByRun(context.Context, string) ([]store.Lease, error)
	RevokeLease(context.Context, string, time.Time) error
	RevokeLeasesForPrincipal(context.Context, string, time.Time) (int, error)
}

// KillSwitch combines immediate in-memory invalidation with durable revocation.
type KillSwitch struct {
	store RevocationStore
	set   *RevocationSet
}

// NewKillSwitch returns a principal-wide lease revoker.
func NewKillSwitch(st RevocationStore, set *RevocationSet) *KillSwitch {
	return &KillSwitch{store: st, set: set}
}

// RevokePrincipal invalidates every current lease for a principal.
func (k *KillSwitch) RevokePrincipal(ctx context.Context, principalID string) (int, error) {
	runs, err := k.store.ListRunsByPrincipal(ctx, principalID)
	if err != nil {
		return 0, err
	}
	for _, run := range runs {
		leases, err := k.store.ListLeasesByRun(ctx, run.ID)
		if err != nil {
			return 0, err
		}
		for _, lease := range leases {
			k.set.Revoke(lease.TokenHash)
		}
	}
	return k.store.RevokeLeasesForPrincipal(ctx, principalID, time.Now())
}

// RevokeLease invalidates one lease in memory before persisting the same
// revocation. If persistence fails, the current process still fails closed.
func (k *KillSwitch) RevokeLease(ctx context.Context, leaseID string) (store.Lease, error) {
	lease, err := k.store.GetLease(ctx, leaseID)
	if err != nil {
		return store.Lease{}, err
	}
	k.set.Revoke(lease.TokenHash)
	if err := k.store.RevokeLease(ctx, leaseID, time.Now()); err != nil {
		return store.Lease{}, err
	}
	return k.store.GetLease(ctx, leaseID)
}
