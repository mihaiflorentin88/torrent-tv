package filelist

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(func() (string, string, string) { return server.URL, "user", "pass" })
}

func TestOpenTorrentPassesBencodeThrough(t *testing.T) {
	torrent := "d4:infod6:lengthi1e4:name4:teste6:lengthi0ee"
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(torrent))
	})
	r, err := c.OpenTorrent(context.Background(), "972366")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != torrent {
		t.Fatalf("torrent bytes must pass through unchanged, got %q", got)
	}
}

func TestOpenTorrentDetectsMissingTorrentPage(t *testing.T) {
	// FileList answers deleted torrents with HTTP 200 and an HTML error page
	// whose body begins with a newline; seen verbatim for a removed release.
	page := "\n<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Transitional//EN\" \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd\">\n<html><body>Eroare interna Nu pot gasi fisierul .torrent</body></html>"
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(page))
	})
	_, err := c.OpenTorrent(context.Background(), "972366")
	if err == nil {
		t.Fatal("an HTML error page must not be handed to the torrent engine")
	}
	if !errors.Is(err, domain.ErrTorrentRemoved) {
		t.Fatalf("missing-torrent page must map to ErrTorrentRemoved, got %v", err)
	}
	if !strings.Contains(err.Error(), "no longer hosts the .torrent") {
		t.Fatalf("error must be actionable for the user, got %v", err)
	}
}

func TestOpenTorrentFlagsGenericHtmlErrorPage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("\n<html><body>Something else entirely</body></html>"))
	})
	_, err := c.OpenTorrent(context.Background(), "972366")
	if err == nil {
		t.Fatal("a non-torrent response must produce an error")
	}
	if errors.Is(err, domain.ErrTorrentRemoved) {
		t.Fatal("only the definitive missing-torrent page may claim removal")
	}
	if !strings.Contains(err.Error(), "non-torrent response") {
		t.Fatalf("error must describe the bad response, got %v", err)
	}
}

func TestOpenTorrentSurfacesHTTPStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.OpenTorrent(context.Background(), "972366")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("HTTP failures must keep their status in the error, got %v", err)
	}
}
