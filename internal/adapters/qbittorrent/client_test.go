package qbittorrent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInfoHashUsesExactInfoDictionary(t *testing.T) {
	torrent := []byte("d8:announce13:https://test/4:infod6:lengthi5e4:name9:video.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	hash, err := infoHash(torrent)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "52e0ec3afc6723a6be6a2dad955dc4027babc55c" {
		t.Fatalf("unexpected hash %s", hash)
	}
}

func TestPrepareFileEnablesAndVerifiesStreamingPriorities(t *testing.T) {
	sequential, firstLast, firstLastToggles := false, false, 0
	priorities := map[string]string{}
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/api/v2/auth/login":
			body = "Ok."
		case "/api/v2/torrents/info":
			body = fmt.Sprintf(`[{"state":"downloading","total_size":100,"amount_left":50,"save_path":"/srv/downloads","seq_dl":%t,"f_l_piece_prio":%t}]`, sequential, firstLast)
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		case "/api/v2/torrents/files":
			body = `[{"index":0,"name":"movie.mkv","size":90,"priority":7},{"index":1,"name":"movie.srt","size":5,"priority":7},{"index":2,"name":"sample.mkv","size":5,"priority":1}]`
		case "/api/v2/torrents/toggleSequentialDownload":
			sequential = true
		case "/api/v2/torrents/toggleFirstLastPiecePrio":
			firstLast = !firstLast
			firstLastToggles++
		case "/api/v2/torrents/filePrio":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			priorities[r.Form.Get("id")] = r.Form.Get("priority")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := client.PrepareFile(t.Context(), "abc", 0, []int{1}); err != nil {
		t.Fatal(err)
	}
	if !sequential || !firstLast || firstLastToggles != 1 || priorities["0|1"] != "1" || priorities["2"] != "0" {
		t.Fatalf("unexpected preparation: sequential=%v firstLast=%v priorities=%v", sequential, firstLast, priorities)
	}
}

func TestPrepareFilesSelectsEverySeasonEpisode(t *testing.T) {
	sequential, firstLast := false, false
	priorities := map[string]string{}
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/api/v2/auth/login":
			body = "Ok."
		case "/api/v2/torrents/info":
			body = fmt.Sprintf(`[{"state":"downloading","size":300,"total_size":305,"amount_left":250,"save_path":"/srv/downloads","seq_dl":%t,"f_l_piece_prio":%t}]`, sequential, firstLast)
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		case "/api/v2/torrents/files":
			body = `[{"index":0,"name":"Show.S01E01.mkv","size":100,"priority":0},{"index":1,"name":"sample.mkv","size":5,"priority":1},{"index":2,"name":"Show.S01E02.mkv","size":100,"priority":0},{"index":3,"name":"Show.S01.srt","size":1,"priority":0}]`
		case "/api/v2/torrents/toggleSequentialDownload":
			sequential = true
		case "/api/v2/torrents/toggleFirstLastPiecePrio":
			firstLast = true
		case "/api/v2/torrents/filePrio":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			priorities[r.Form.Get("id")] = r.Form.Get("priority")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := client.PrepareFiles(t.Context(), "season", []int{0, 2}, []int{3}); err != nil {
		t.Fatal(err)
	}
	if !sequential || !firstLast || priorities["0|2|3"] != "1" || priorities["1"] != "0" {
		t.Fatalf("season priorities were not preserved: sequential=%v firstLast=%v priorities=%v", sequential, firstLast, priorities)
	}
}

func TestPrepareFileDoesNotRetoggleStablePriorities(t *testing.T) {
	firstLast, sequential := true, true
	firstLastToggles, sequentialToggles := 0, 0
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/api/v2/auth/login":
			body = "Ok."
		case "/api/v2/torrents/info":
			body = fmt.Sprintf(`[{"state":"downloading","total_size":100,"amount_left":50,"save_path":"/srv/downloads","seq_dl":%t,"f_l_piece_prio":%t}]`, sequential, firstLast)
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		case "/api/v2/torrents/files":
			body = `[{"index":0,"name":"movie.mkv","size":95,"priority":1},{"index":1,"name":"sample.mkv","size":5,"priority":0}]`
		case "/api/v2/torrents/toggleFirstLastPiecePrio":
			firstLast = !firstLast
			firstLastToggles++
		case "/api/v2/torrents/toggleSequentialDownload":
			sequential = !sequential
			sequentialToggles++
		case "/api/v2/torrents/filePrio":
			t.Fatal("stable file priorities must not be rewritten")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := client.PrepareFile(t.Context(), "abc", 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.PrepareFile(t.Context(), "abc", 0, nil); err != nil {
		t.Fatal(err)
	}
	if firstLastToggles != 2 || sequentialToggles != 2 {
		t.Fatalf("stable preparation toggled first/last %d times and sequential %d times", firstLastToggles, sequentialToggles)
	}
}

func TestPrepareFileReappliesFirstLastAfterPriorityChanges(t *testing.T) {
	firstLast, firstLastToggles, sequentialToggles := true, 0, 0
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/api/v2/auth/login":
			body = "Ok."
		case "/api/v2/torrents/info":
			body = fmt.Sprintf(`[{"state":"downloading","total_size":100,"amount_left":50,"save_path":"/srv/downloads","seq_dl":true,"f_l_piece_prio":%t}]`, firstLast)
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		case "/api/v2/torrents/files":
			body = `[{"index":0,"name":"movie.mkv","size":95,"priority":1},{"index":1,"name":"sample.mkv","size":5,"priority":1}]`
		case "/api/v2/torrents/toggleFirstLastPiecePrio":
			firstLast = !firstLast
			firstLastToggles++
		case "/api/v2/torrents/toggleSequentialDownload":
			sequentialToggles++
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := client.PrepareFile(t.Context(), "abc", 0, nil); err != nil {
		t.Fatal(err)
	}
	if !firstLast || firstLastToggles != 2 || sequentialToggles != 2 {
		t.Fatalf("first/last priority state=%v toggles=%d sequential toggles=%d", firstLast, firstLastToggles, sequentialToggles)
	}
}

func TestInfoHashRejectsMissingInfo(t *testing.T) {
	if _, err := infoHash([]byte("d3:fooi1ee")); err == nil {
		t.Fatal("expected error")
	}
}

func TestPieceSizeComesFromTorrentProperties(t *testing.T) {
	responses := map[string]string{
		"/api/v2/auth/login":           "Ok.",
		"/api/v2/torrents/pieceStates": `[2,1,0]`,
		"/api/v2/torrents/properties":  `{"piece_size":2097152}`,
		"/api/v2/torrents/info":        `[{"hash":"abc","state":"downloading","total_size":100,"amount_left":40,"save_path":"/srv/downloads","content_path":"/srv/downloads/movie.mkv"}]`,
		"/api/v2/torrents/trackers":    `[]`,
	}
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := responses[r.URL.Path]
		if !ok {
			body = `{}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	pieces, err := client.Pieces(t.Context(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if pieces.PieceSize != 2097152 || len(pieces.States) != 3 {
		t.Fatalf("unexpected pieces: %+v", pieces)
	}

	status, err := client.Status(t.Context(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if status.PieceSize != 2097152 || status.SavePath != "/srv/downloads" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestStatusUsesSelectedFilesSizeInsteadOfWholeTorrentSize(t *testing.T) {
	responses := map[string]string{
		"/api/v2/auth/login":          "Ok.",
		"/api/v2/torrents/info":       `[{"hash":"season","state":"downloading","size":5000000000,"total_size":40000000000,"amount_left":1000000000,"progress":0.8,"save_path":"/srv/downloads"}]`,
		"/api/v2/torrents/properties": `{"piece_size":4194304}`,
		"/api/v2/app/preferences":     `{}`,
		"/api/v2/torrents/trackers":   `[]`,
	}
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := responses[r.URL.Path]
		if !ok {
			body = `{}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	status, err := client.Status(t.Context(), "season")
	if err != nil {
		t.Fatal(err)
	}
	if status.TotalBytes != 5_000_000_000 || status.DownloadedBytes != 4_000_000_000 {
		t.Fatalf("selected-file status used the whole torrent: %+v", status)
	}
}

func TestAddResponseCompatibility(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusOK, "", true},
		{http.StatusOK, "Ok.", true},
		{http.StatusOK, "Fails.", false},
		{http.StatusInternalServerError, "", false},
	} {
		if got := addAccepted(tt.status, []byte(tt.body)); got != tt.want {
			t.Errorf("addAccepted(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
		}
	}
}

func TestAddReusesExistingTorrentAfterDuplicateResponse(t *testing.T) {
	torrent := []byte("d8:announce13:https://test/4:infod6:lengthi5e4:name9:video.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/api/v2/auth/login":
			body = "Ok."
		case "/api/v2/torrents/add":
			body = "Fails."
		case "/api/v2/torrents/info":
			body = `[{"hash":"52e0ec3afc6723a6be6a2dad955dc4027babc55c","state":"uploading","total_size":5,"amount_left":0,"save_path":"/srv/downloads"}]`
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	hash, err := client.Add(t.Context(), strings.NewReader(string(torrent)), "/srv/downloads")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "52e0ec3afc6723a6be6a2dad955dc4027babc55c" {
		t.Fatalf("unexpected hash %q", hash)
	}
}

func TestAddReusesExistingTorrentAfter409Conflict(t *testing.T) {
	// qBittorrent >= 4.4 answers a duplicate add with 409 Conflict instead of
	// a 2xx "Fails." body; the Docker nox image (5.x) always takes this path.
	torrent := []byte("d8:announce13:https://test/4:infod6:lengthi5e4:name9:video.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		status := http.StatusOK
		switch r.URL.Path {
		case "/api/v2/auth/login":
			body = "Ok."
		case "/api/v2/torrents/add":
			status = http.StatusConflict
			body = "Torrent is already downloaded."
		case "/api/v2/torrents/info":
			body = `[{"hash":"52e0ec3afc6723a6be6a2dad955dc4027babc55c","state":"uploading","total_size":5,"amount_left":0,"save_path":"/srv/downloads"}]`
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	hash, err := client.Add(t.Context(), strings.NewReader(string(torrent)), "/srv/downloads")
	if err != nil {
		t.Fatalf("duplicate 409 add should reuse the existing torrent: %v", err)
	}
	if hash != "52e0ec3afc6723a6be6a2dad955dc4027babc55c" {
		t.Fatalf("unexpected hash %q", hash)
	}
}

func TestResumeReportsTorrentNotFoundForUnknownHash(t *testing.T) {
	client := New(func() (string, string, string) { return "http://qb.test", "user", "password" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := "Ok."
		status := http.StatusOK
		if r.URL.Path == "/api/v2/torrents/resume" {
			status = http.StatusNotFound
			body = ""
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := client.Resume(t.Context(), "vanished"); !errors.Is(err, domain.ErrTorrentNotFound) {
		t.Fatalf("resume of an unknown hash should report the torrent missing: %v", err)
	}
}
func TestCredentialFreeClientPerformsStatusAndAddWithoutLogin(t *testing.T) {
	torrent := []byte("d8:announce13:https://test/4:infod6:lengthi5e4:name9:video.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	loginCalls := 0
	client := New(func() (string, string, string) { return "http://qb.test", "", "" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v2/auth/login" {
			loginCalls++
		}
		body := "{}"
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			body = `[{"state":"downloading","total_size":100,"amount_left":50,"save_path":"/srv/downloads","piece_size":4}]`
		case "/api/v2/torrents/properties":
			body = `{"piece_size":4}`
		case "/api/v2/app/preferences":
			body = `{"temp_path_enabled":true,"temp_path":"/srv/downloads/.incomplete"}`
		case "/api/v2/torrents/trackers":
			body = `[]`
		case "/api/v2/torrents/add":
			body = "Ok."
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	if _, err := client.Status(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Add(t.Context(), strings.NewReader(string(torrent)), "/srv/downloads"); err != nil {
		t.Fatal(err)
	}
	if loginCalls != 0 {
		t.Fatalf("credential-free client called the login endpoint %d times", loginCalls)
	}
}

func TestCredentialFreeClientTreatsForbiddenAsTerminal(t *testing.T) {
	requests := 0
	client := New(func() (string, string, string) { return "http://qb.test", "", "" })
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("Forbidden")), Request: r}, nil
	})

	if _, err := client.Status(t.Context(), "abc"); err == nil {
		t.Fatal("credential-free client must fail on 403 instead of retrying into a login loop")
	}
	if requests != 1 {
		t.Fatalf("credential-free 403 must be terminal after one request, got %d requests", requests)
	}
}

func TestHalfConfiguredCredentialsAreRejected(t *testing.T) {
	for _, tt := range []struct {
		name     string
		user     string
		password string
	}{
		{"only username", "admin", ""},
		{"only password", "", "secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client := New(func() (string, string, string) { return "http://qb.test", tt.user, tt.password })
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("Ok.")), Request: r}, nil
			})

			_, err := client.Test(t.Context())
			if err == nil {
				t.Fatal("exactly one configured credential must produce a clear misconfiguration error")
			}
			if !strings.Contains(err.Error(), "both") {
				t.Fatalf("unclear misconfiguration error: %v", err)
			}
			if requests != 0 {
				t.Fatalf("misconfigured credentials must fail before any request, got %d requests", requests)
			}
		})
	}
}

func TestCanonicalState(t *testing.T) {
	cases := map[string]string{
		"downloading": domain.StateDownloading, "metaDL": domain.StateDownloading,
		"forcedDL": domain.StateDownloading, "stalledDL": domain.StateDownloading,
		"uploading": domain.StateSeeding, "stalledUP": domain.StateSeeding,
		"pausedDL": domain.StatePausedDL, "stoppedDL": domain.StatePausedDL,
		"pausedUP": domain.StatePausedUP, "stoppedUP": domain.StatePausedUP,
		"queuedDL": domain.StateQueued, "queuedUP": domain.StateQueued,
		"error": domain.StateError, "missingFiles": domain.StateError,
		"weird": "weird",
	}
	for raw, want := range cases {
		if got := canonicalState(raw); got != want {
			t.Errorf("canonicalState(%q) = %q, want %q", raw, got, want)
		}
	}
}
