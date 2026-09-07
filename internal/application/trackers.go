package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

type TrackerRegistration struct {
	Adapter    Tracker
	Enabled    func() bool
	Configured func() bool
}

type TrackerStatus struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Enabled      bool                `json:"enabled"`
	Configured   bool                `json:"configured"`
	Capabilities TrackerCapabilities `json:"capabilities"`
}

type TrackerRegistry struct {
	mu            sync.RWMutex
	order         []string
	registrations map[string]TrackerRegistration
}

func NewTrackerRegistry(registrations []TrackerRegistration) (*TrackerRegistry, error) {
	reg := &TrackerRegistry{
		order:         make([]string, 0, len(registrations)),
		registrations: make(map[string]TrackerRegistration, len(registrations)),
	}
	for _, r := range registrations {
		if r.Adapter == nil {
			return nil, errors.New("tracker adapter cannot be nil")
		}
		id := strings.TrimSpace(r.Adapter.ID())
		if id == "" {
			return nil, errors.New("tracker ID cannot be empty")
		}
		if _, exists := reg.registrations[id]; exists {
			return nil, fmt.Errorf("duplicate tracker ID %q", id)
		}
		if r.Enabled == nil {
			return nil, fmt.Errorf("tracker %q: Enabled callback cannot be nil", id)
		}
		if r.Configured == nil {
			return nil, fmt.Errorf("tracker %q: Configured callback cannot be nil", id)
		}
		reg.order = append(reg.order, id)
		reg.registrations[id] = r
	}
	return reg, nil
}

func (r *TrackerRegistry) Lookup(id string) (Tracker, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.registrations[id]
	if !ok {
		return nil, false
	}
	return entry.Adapter, true
}

func (r *TrackerRegistry) Eligible(id string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.registrations[id]
	if !ok {
		return false
	}
	return entry.Enabled() && entry.Configured()
}

func (r *TrackerRegistry) Configured(id string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.registrations[id]
	if !ok {
		return false
	}
	return entry.Configured()
}

func (r *TrackerRegistry) EligibleIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.order))
	for _, id := range r.order {
		entry := r.registrations[id]
		if entry.Enabled() && entry.Configured() {
			out = append(out, id)
		}
	}
	return out
}

func (r *TrackerRegistry) RequireEligible(id string) (Tracker, error) {
	if r == nil {
		return nil, domain.ErrTrackerDisabled
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.registrations[id]
	if !ok {
		return nil, fmt.Errorf("unknown tracker %q", id)
	}
	if !entry.Enabled() {
		return nil, fmt.Errorf("%w: tracker %q is disabled", domain.ErrTrackerDisabled, entry.Adapter.Name())
	}
	if !entry.Configured() {
		return nil, fmt.Errorf("%w: tracker %q is not configured", domain.ErrTrackerUnconfigured, entry.Adapter.Name())
	}
	return entry.Adapter, nil
}

func (r *TrackerRegistry) Status() []TrackerStatus {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TrackerStatus, len(r.order))
	for i, id := range r.order {
		entry := r.registrations[id]
		out[i] = TrackerStatus{
			ID:           id,
			Name:         entry.Adapter.Name(),
			Enabled:      entry.Enabled(),
			Configured:   entry.Configured(),
			Capabilities: entry.Adapter.Capabilities(),
		}
	}
	return out
}

// TrackerStatuses reports the registry's ordered statuses for clients.
func (s *Service) TrackerStatuses() []TrackerStatus {
	if s == nil || s.trackers == nil {
		return nil
	}
	return s.trackers.Status()
}

// LookupTracker finds a registered tracker by ID, regardless of eligibility.
func (s *Service) LookupTracker(id string) (Tracker, bool) {
	if s == nil || s.trackers == nil {
		return nil, false
	}
	return s.trackers.Lookup(id)
}

// TestTracker probes one registered tracker with a bounded, read-only
// request. Eligibility is deliberately not required: a deliberate
// connection test may contact a disabled-but-configured provider, and it
// never ingests or upserts releases.
func (s *Service) TestTracker(ctx context.Context, id string) (int, error) {
	tracker, ok := s.LookupTracker(id)
	if !ok {
		return 0, fmt.Errorf("unknown tracker %q", id)
	}
	if !s.trackers.Configured(id) {
		return 0, fmt.Errorf("%w: tracker %q is not configured", domain.ErrTrackerUnconfigured, tracker.Name())
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	items, err := tracker.Latest(ctx)
	return len(items), err
}

// RefreshTrackers cancels the service's owned provider work — discovery
// searches, title refreshes, and catalog sync — so the next round re-reads
// the registry's live settings callbacks after a settings save. It is the
// single notification both the HTTP settings handler and the desktop
// bindings call.
func (s *Service) RefreshTrackers() {
	if s == nil {
		return
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.providerCancel == nil {
		return
	}
	s.providerCancel()
	s.providerCtx, s.providerCancel = context.WithCancel(s.baseCtx)
}

func (s *Service) providerContext() context.Context {
	if s == nil {
		return context.Background()
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.providerCtx == nil {
		return s.baseCtx
	}
	return s.providerCtx
}

func searchJobKey(q string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(q))))
	return "tracker-search:" + base64.RawURLEncoding.EncodeToString(sum[:12])
}
