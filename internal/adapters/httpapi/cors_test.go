package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func passThrough(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestCORSPreflightForAPIPaths(t *testing.T) {
	handler := corsAPI(http.HandlerFunc(passThrough))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/v1/settings", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Fatalf("Allow-Headers = %q, want Content-Type", got)
	}
}

func TestCORSHeadersOnAPIResponses(t *testing.T) {
	handler := corsAPI(http.HandlerFunc(passThrough))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
}

func TestCORSLeavesNonAPIPathsUntouched(t *testing.T) {
	handler := corsAPI(http.HandlerFunc(passThrough))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want empty", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 pass-through", rec.Code)
	}
}
