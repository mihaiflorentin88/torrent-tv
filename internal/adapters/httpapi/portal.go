// Portal HTTP surfaces: the local read-only views over the integration
// hub's cached state plus the gated identity and click operations. The
// hub owns gating and failure classification; these handlers only map
// outcomes onto local statuses, cap submitted values, and never expose
// credentials in a response.
package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mihaiflorentin88/torrent-tv/internal/application/portal"
)

// maxPromotionsCount bounds the count query value a client may request.
const maxPromotionsCount = 50

// Option configures optional integration surfaces on the API handler.
// Composition wires both; settings-only embedders omit them and their
// routes are not mounted.
type Option func(*API)

// WithPortal mounts the portal routes backed by the integration hub.
func WithPortal(hub *portal.Hub) Option {
	return func(a *API) { a.portal = hub }
}

// GET /api/v1/portal/state serves the cached collapsed capabilities,
// donor state, and ordered links. It never calls upstream: the hub's
// refresh cycles own every outbound request, so this GET is answered
// from the snapshot alone.
func (a *API) portalState(w http.ResponseWriter, r *http.Request) {
	write(w, http.StatusOK, a.portal.Snapshot())
}

// GET /api/v1/portal/promotions?count=N serves up to N cached creatives
// from the hub's live delivery. A hidden or failed slot is an empty
// array, never a fabricated creative.
func (a *API) portalPromotions(w http.ResponseWriter, r *http.Request) {
	promotions := a.portal.Promotions()
	count := min(max(integer(r, "count", len(promotions)), 0), len(promotions), maxPromotionsCount)
	write(w, http.StatusOK, promotions[:count])
}

// GET /api/v1/portal/promotions/{provider}/{id}/click resolves tracking
// and redirects to a validated destination. Only an absolute HTTP(S) URL
// with a host may leave the server; an unusable upstream answer is a
// neutral problem, never a redirect fallback.
func (a *API) portalClick(w http.ResponseWriter, r *http.Request) {
	destination, err := a.portal.Click(r.Context(), r.PathValue("provider"), r.PathValue("id"))
	if err != nil {
		portalProblem(w, http.StatusBadRequest, err)
		return
	}
	if !safeDestination(destination) {
		problem(w, http.StatusBadGateway, errors.New("upstream returned an unusable click destination"))
		return
	}
	http.Redirect(w, r, destination, http.StatusFound)
}

// POST /api/v1/portal/session exchanges credentials for a session. A
// credential rejection stays a form error, never an outage.
func (a *API) portalLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("invalid session request: %w", err))
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		problem(w, http.StatusBadRequest, errors.New("email and password are required"))
		return
	}
	session, err := a.portal.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		portalProblem(w, http.StatusBadRequest, err)
		return
	}
	write(w, http.StatusOK, session)
}

// POST /api/v1/portal/session/register creates an account without
// implying login: 201 with an empty body and no token. Clients
// explicitly sign in afterwards.
func (a *API) portalRegister(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if err := decode(r, &body); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("invalid registration request: %w", err))
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		problem(w, http.StatusBadRequest, errors.New("email and password are required"))
		return
	}
	if err := a.portal.Register(r.Context(), body.Email, body.Password, strings.TrimSpace(body.DisplayName)); err != nil {
		portalProblem(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// GET /api/v1/portal/session/me resolves identity for a bearer token.
// This is the only portal endpoint that forwards the Authorization
// header upstream; an invalid or expired session answers 401 so clients
// clear their stored identity.
func (a *API) portalMe(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		problem(w, http.StatusUnauthorized, errors.New("missing bearer token"))
		return
	}
	user, err := a.portal.Me(r.Context(), token)
	if err != nil {
		portalProblem(w, http.StatusUnauthorized, err)
		return
	}
	write(w, http.StatusOK, user)
}

// portalProblem maps hub operation failures onto local problem responses.
// Credential rejection stays a form error at credentialStatus; transport
// outages and hidden capabilities answer 503; anything else is a neutral
// 502 upstream problem. Error text comes from the hub sentinels, which
// never carry credentials or upstream bodies.
func portalProblem(w http.ResponseWriter, credentialStatus int, err error) {
	switch {
	case errors.Is(err, portal.ErrCredentials):
		problem(w, credentialStatus, err)
	case errors.Is(err, portal.ErrAccountsUnavailable), errors.Is(err, portal.ErrPromotionsUnavailable), errors.Is(err, portal.ErrUnavailable):
		problem(w, http.StatusServiceUnavailable, err)
	default:
		problem(w, http.StatusBadGateway, err)
	}
}

// bearerToken extracts the Authorization: Bearer credential.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// safeDestination reports whether raw may leave the server as a redirect
// target: an absolute HTTP(S) URL with a host.
func safeDestination(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}
