// Update HTTP surfaces over the self-update coordinator: the cached
// status, fresh checks, and the accepted-apply handoff whose response
// barrier the handler releases only after the 202 body has been flushed.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
)

// Coordinator is the update surface the HTTP layer consumes: the shared
// application API contract plus the accepted-apply response barrier of
// the coordinator.
type Coordinator interface {
	updates.API
	ResponseFlushed()
}

// WithUpdates mounts the update routes backed by the coordinator.
func WithUpdates(coordinator Coordinator) Option {
	return func(a *API) { a.updates = coordinator }
}

// GET /api/v1/updates/current serves the cached status without any disk
// or network probe.
func (a *API) updatesCurrent(w http.ResponseWriter, r *http.Request) {
	write(w, http.StatusOK, a.updates.Current())
}

// POST /api/v1/updates/check performs one fresh availability fetch. A
// failed check is a neutral problem: availability was cleared, and no
// stale update is ever offered.
func (a *API) updatesCheck(w http.ResponseWriter, r *http.Request) {
	status, err := a.updates.Check(r.Context())
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	write(w, http.StatusOK, status)
}

// POST /api/v1/updates/apply requests the update. An accepted operation
// answers 202 and releases the coordinator's response barrier only after
// the body has been flushed — a client that vanished mid-write must
// never strand the installation, because the coordinator owns the
// operation from acceptance on. An already-current installation is a
// 200 no-op; busy and manual-only installations answer 409; every other
// failure is a neutral problem.
func (a *API) updatesApply(w http.ResponseWriter, r *http.Request) {
	result, err := a.updates.Apply(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, updates.ErrApplyBusy), errors.Is(err, updates.ErrManualOnly):
			problem(w, http.StatusConflict, err)
		default:
			problem(w, http.StatusBadGateway, err)
		}
		return
	}
	status := http.StatusOK
	if result.Accepted {
		status = http.StatusAccepted
	}
	write(w, status, result.Status)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if result.Accepted {
		a.updates.ResponseFlushed()
	}
}
