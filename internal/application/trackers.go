package application

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

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

func searchJobKey(q string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(q))))
	return "tracker-search:" + base64.RawURLEncoding.EncodeToString(sum[:12])
}
