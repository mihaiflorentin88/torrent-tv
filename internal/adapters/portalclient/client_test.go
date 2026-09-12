package portalclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testDeadline = 5 * time.Second

// newTestServer starts an httptest server and returns a Client whose HTTP
// transport rewrites requests for the fixed upstream host onto the server.
// Every request is verified to carry a bounded per-request deadline, and the
// handler records each observed request.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.observe(t, r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("request carries no deadline")
		} else if remaining := time.Until(deadline); remaining > testDeadline+time.Second || remaining <= 0 {
			t.Errorf("request deadline out of bounds: %v", remaining)
		}
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.Host = target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	return New(client), rec
}

type recorder struct {
	requests int32
	headers  []http.Header
	paths    []string
}

func (r *recorder) observe(t *testing.T, req *http.Request) {
	t.Helper()
	atomic.AddInt32(&r.requests, 1)
	r.headers = append(r.headers, req.Header.Clone())
	r.paths = append(r.paths, req.URL.Path+"?"+req.URL.RawQuery)
}

func (r *recorder) count() int { return int(atomic.LoadInt32(&r.requests)) }

func (r *recorder) lastAuth() string {
	if len(r.headers) == 0 {
		return ""
	}
	return r.headers[len(r.headers)-1].Get("Authorization")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestSettingsDecodesNestedFlags(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/settings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"ads":{"enabled":true},"accounts":{"enabled":false},"supporter_plans":{"enabled":true}}`))
	})
	got, err := c.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if !got.AdsEnabled || got.AccountsEnabled {
		t.Errorf("unexpected settings: %+v", got)
	}
}

func TestLinksDecodesAndIsNeverNil(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(jsonBody(t, []map[string]any{
			{"id": 3, "title": "Project", "url": "https://example.com/", "description": "d"},
		})))
	})
	got, err := c.Links(context.Background())
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(got) != 1 || got[0].ID != 3 || got[0].Title != "Project" || got[0].URL != "https://example.com/" {
		t.Errorf("unexpected links: %+v", got)
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	got, err = c.Links(context.Background())
	if err != nil {
		t.Fatalf("Links empty: %v", err)
	}
	if got == nil {
		t.Error("Links returned nil slice for empty pool")
	}
}

func TestNoticeDecodeAndAbsentIsDistinct(t *testing.T) {
	payload := `{"version":"1.4.0","notes":"n","released_at":"2026-08-30T18:00:00Z","download_url":"https://example.com/dl","binaries":[{"platform":"windows-amd64","download_url":"https://example.com/b"}]}`
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/updates" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(payload))
	})
	got, err := c.Notice(context.Background())
	if err != nil {
		t.Fatalf("Notice: %v", err)
	}
	if got.Version != "1.4.0" || len(got.Binaries) != 1 || got.Binaries[0].Platform != "windows-amd64" {
		t.Errorf("unexpected notice: %+v", got)
	}
	if got.ReleasedAt.IsZero() {
		t.Error("released_at not decoded")
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "none", http.StatusNotFound) })
	_, err = c.Notice(context.Background())
	if !errors.Is(err, ErrNoticeAbsent) {
		t.Fatalf("notice absence not distinguished: %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("absence must not read as transport outage")
	}
}

func TestPromotionsMapScreenTimeAndEmptyPool(t *testing.T) {
	c, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "count=2" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		w.Write([]byte(`[{"provider":"house","id":"7","title":"t","text":"x","image":"data:image/png;base64,AA","link":"https://supporter.example/","screen_time":10}]`))
	})
	got, err := c.Promotions(context.Background(), 2)
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(got) != 1 || got[0].ScreenTime != 10 || got[0].Provider != "house" || got[0].ID != "7" || got[0].Image == "" || got[0].Link != "https://supporter.example/" {
		t.Errorf("unexpected promotions: %+v", got)
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "none", http.StatusNotFound) })
	got, err = c.Promotions(context.Background(), 1)
	if err != nil {
		t.Fatalf("empty pool: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("empty pool must be an empty non-nil slice: %#v", got)
	}

	c, rec = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	if _, err = c.Promotions(context.Background(), 0); err != nil {
		t.Fatalf("Promotions count=0: %v", err)
	}
	if !strings.Contains(rec.paths[0], "count=1") {
		t.Errorf("non-positive count not clamped: %q", rec.paths[0])
	}
}

func TestPromotionAvailabilityUsesWeightsNotDelivery(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ads/weights" {
			t.Errorf("availability must use the non-impression route, got %q", r.URL.Path)
		}
		w.Write([]byte(`[{"title":"t","provider":"house","id":"1","weight":1,"screen_time":10}]`))
	})
	available, err := c.PromotionAvailability(context.Background())
	if err != nil || !available {
		t.Fatalf("PromotionAvailability: %v %v", available, err)
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	available, err = c.PromotionAvailability(context.Background())
	if err != nil || available {
		t.Errorf("empty pool must be unavailable: %v %v", available, err)
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "none", http.StatusNotFound) })
	available, err = c.PromotionAvailability(context.Background())
	if err != nil || available {
		t.Errorf("404 pool must be unavailable without error: %v %v", available, err)
	}
}

func TestClickValidatesAndDoesNotFollowRedirect(t *testing.T) {
	var followed int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("GET /api/v1/ads/house/7/click", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/never-followed", http.StatusFound)
	})
	mux.HandleFunc("GET /never-followed", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&followed, 1)
	})
	target, _ := url.Parse(srv.URL)
	c := New(&http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.Host = target.Host
		return http.DefaultTransport.RoundTrip(r)
	})})

	dest, err := c.Click(context.Background(), "house", "7")
	if err != nil {
		t.Fatalf("Click: %v", err)
	}
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != target.Scheme || u.Host != target.Host || u.Path != "/never-followed" {
		t.Errorf("unexpected destination %q (%v)", dest, err)
	}
	if followed != 0 {
		t.Error("click followed the upstream redirect")
	}

	for _, bad := range []struct{ provider, id string }{
		{"", "7"}, {"house", ""}, {"house", "7/../../x"}, {"ho/use", "7"},
	} {
		if _, err := c.Click(context.Background(), bad.provider, bad.id); err == nil {
			t.Errorf("Click(%q,%q) accepted invalid segments", bad.provider, bad.id)
		}
	}

	// Unsafe destinations are rejected: non-http scheme and missing host.
	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "javascript:alert(1)")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := c.Click(context.Background(), "house", "7"); err == nil {
		t.Error("javascript destination accepted")
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := c.Click(context.Background(), "house", "7"); err == nil {
		t.Error("destination without host accepted")
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) })
	if _, err := c.Click(context.Background(), "house", "7"); err == nil {
		t.Error("missing destination accepted")
	}
}

func TestAccountStatusAuthenticatesWithAPIKeyAndMapsDonor(t *testing.T) {
	c, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/account/status" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fla_key123" {
			t.Errorf("status must authenticate with the API key, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"donor":true,"donor_until":"2026-10-01T12:00:00Z","display_name":"Alice"}`))
	})
	got, err := c.AccountStatus(context.Background(), "fla_key123")
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	if !got.Donor || got.DonorUntil == nil || !got.DonorUntil.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("unexpected status: %+v", got)
	}

	// Null or absent donor_until yields a nil pointer, not a parse error.
	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"donor":false,"donor_until":null,"display_name":"A"}`))
	})
	got, err = c.AccountStatus(context.Background(), "fla_key123")
	if err != nil || got.DonorUntil != nil {
		t.Errorf("null donor_until: %+v %v", got.DonorUntil, err)
	}
	_ = rec
}

func TestIdentityOperationsAuthenticateWithJWT(t *testing.T) {
	c, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer jwt-token" {
				t.Errorf("me must authenticate with the JWT, got %q", r.Header.Get("Authorization"))
			}
			w.Write([]byte(`{"id":1,"email":"a@b.c","display_name":"A","role":"user"}`))
		case "/api/v1/auth/login":
			if r.Header.Get("Authorization") != "" {
				t.Errorf("login must not carry an authorization header, got %q", r.Header.Get("Authorization"))
			}
			var body struct{ Email, Password string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email != "a@b.c" || body.Password != "pw" {
				t.Errorf("bad login body: %v %+v", err, body)
			}
			w.Write([]byte(`{"token":"jwt-token","expires_at":"2026-09-06T10:00:00Z"}`))
		case "/api/v1/auth/register":
			if r.Header.Get("Authorization") != "" {
				t.Errorf("register must not carry an authorization header, got %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	user, err := c.Me(context.Background(), "jwt-token")
	if err != nil || user.ID != 1 || user.Email != "a@b.c" || user.Role != "user" {
		t.Errorf("Me: %+v %v", user, err)
	}

	session, err := c.Login(context.Background(), "a@b.c", "pw")
	if err != nil || session.Token != "jwt-token" || session.ExpiresAt.IsZero() {
		t.Errorf("Login: %+v %v", session, err)
	}

	if err := c.Register(context.Background(), "a@b.c", "pw", "Alice"); err != nil {
		t.Errorf("Register: %v", err)
	}
	if rec.lastAuth() != "" {
		t.Errorf("register leaked an authorization header: %q", rec.lastAuth())
	}
}

func TestCredentialRejectionIsTypedSeparatelyFromOutage(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		call       func(*Client) error
	}{
		{"login", "/api/v1/auth/login", func(c *Client) error { _, err := c.Login(context.Background(), "a@b.c", "pw"); return err }},
		{"status", "/api/v1/account/status", func(c *Client) error { _, err := c.AccountStatus(context.Background(), "k"); return err }},
	} {
		t.Run(tc.name+"/401", func(t *testing.T) {
			c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) })
			err := tc.call(c)
			if !errors.Is(err, ErrCredentials) {
				t.Fatalf("expected credential rejection, got %v", err)
			}
			if errors.Is(err, ErrUnavailable) {
				t.Error("credential rejection must not read as an outage")
			}
			if strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), "fla_key") {
				t.Error("error leaks credentials")
			}
		})
		t.Run(tc.name+"/503", func(t *testing.T) {
			c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
			err := tc.call(c)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("expected outage, got %v", err)
			}
			if errors.Is(err, ErrCredentials) {
				t.Error("outage must not read as credential rejection")
			}
		})
	}
}

func TestMalformedAndOversizedBodiesAreRejected(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ads":`)) })
	if _, err := c.Settings(context.Background()); err == nil {
		t.Error("malformed JSON accepted")
	}

	c, _ = newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"padding":"` + strings.Repeat("x", maxBodyBytes+64) + `"}`))
	})
	if _, err := c.Settings(context.Background()); err == nil {
		t.Error("oversized body accepted")
	}
}

func TestContextCancellationAndTransportFailurePropagate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := c.Settings(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context not propagated: %v", err)
	}

	c = New(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})})
	if _, err := c.Settings(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("transport failure not typed as outage: %v", err)
	}
}
