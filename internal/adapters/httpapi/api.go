package httpapi

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/portal"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

//go:embed static/*
var static embed.FS

// webFS is the embedded web build behind the app shell. A variable so tests
// can mount a fake build: a fresh checkout embeds only the placeholder until
// `make frontend` fills static/.
var webFS fs.FS = embeddedWebFS()

func embeddedWebFS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

type API struct {
	service  *application.Service
	settings *config.Store
	log      *slog.Logger
	version  string
	// portal is the integration hub and updates the self-update
	// coordinator. Both are optional: composition wires them, and the
	// integration routes are only mounted when present.
	portal  *portal.Hub
	updates Coordinator
}

func New(service *application.Service, settings *config.Store, log *slog.Logger, version string, opts ...Option) http.Handler {
	a := &API{service: service, settings: settings, log: log, version: version}
	for _, opt := range opts {
		opt(a)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/system/info", a.info)
	mux.HandleFunc("GET /api/v1/settings", a.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", a.putSettings)
	mux.HandleFunc("GET /api/v1/settings/schema", a.settingsSchema)
	mux.HandleFunc("POST /api/v1/dependencies/{name}/test", a.testDependency)
	mux.HandleFunc("POST /api/v1/diagnostics/client", a.clientDiagnostic)
	mux.HandleFunc("GET /api/v1/catalog/categories", a.categories)
	mux.HandleFunc("GET /api/v1/catalog/latest", a.catalog)
	mux.HandleFunc("GET /api/v1/catalog/search", a.search)
	mux.HandleFunc("POST /api/v1/catalog/search", a.searchTitles)
	mux.HandleFunc("GET /api/v1/catalog/titles", a.catalogTitles)
	mux.HandleFunc("GET /api/v1/catalog/titles/{id}", a.catalogTitle)
	mux.HandleFunc("POST /api/v1/catalog/titles/{id}/refresh", a.refreshCatalogTitle)
	mux.HandleFunc("POST /api/v1/catalog/sync", a.catalogSync)
	mux.HandleFunc("GET /api/v1/catalog/status", a.catalogStatus)
	mux.HandleFunc("POST /api/v1/metadata/ensure", a.ensureMetadata)
	mux.HandleFunc("GET /api/v1/catalog/facets", a.catalogFacets)
	mux.HandleFunc("GET /api/v1/artwork/{titleId}/{kind}", a.artwork)
	mux.HandleFunc("POST /api/v1/releases/{id}/prepare", a.prepare)
	mux.HandleFunc("POST /api/v1/releases/{id}/prepare-season", a.prepareSeason)
	mux.HandleFunc("GET /api/v1/downloads", a.downloads)
	mux.HandleFunc("GET /api/v1/downloads/{id}/media-info", a.mediaInfo)
	mux.HandleFunc("GET /api/v1/downloads/{id}/audio-anchor", a.audioAnchor)
	mux.HandleFunc("DELETE /api/v1/downloads/{id}", a.deleteDownload)
	mux.HandleFunc("POST /api/v1/downloads/{id}/next-episode", a.nextEpisode)
	mux.HandleFunc("GET /api/v1/jobs", a.jobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", a.job)
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", a.jobLogs)
	mux.HandleFunc("POST /api/v1/jobs/{id}/retry", a.retryJob)
	mux.HandleFunc("GET /api/v1/events", a.events)
	if a.portal != nil {
		mux.HandleFunc("GET /api/v1/portal/state", a.portalState)
		mux.HandleFunc("GET /api/v1/portal/promotions", a.portalPromotions)
		mux.HandleFunc("GET /api/v1/portal/promotions/{provider}/{id}/click", a.portalClick)
		mux.HandleFunc("POST /api/v1/portal/session", a.portalLogin)
		mux.HandleFunc("POST /api/v1/portal/session/register", a.portalRegister)
		mux.HandleFunc("GET /api/v1/portal/session/me", a.portalMe)
	}
	if a.updates != nil {
		mux.HandleFunc("GET /api/v1/updates/current", a.updatesCurrent)
		mux.HandleFunc("POST /api/v1/updates/check", a.updatesCheck)
		mux.HandleFunc("POST /api/v1/updates/apply", a.updatesApply)
	}
	mux.HandleFunc("GET /api/v1/downloads/{id}/subtitles", a.searchSubtitles)
	mux.HandleFunc("POST /api/v1/downloads/{id}/subtitles/prepare", a.prepareSubtitle)
	mux.HandleFunc("GET /api/v1/subtitles/{asset}", a.subtitle)
	mux.HandleFunc("POST /api/v1/downloads/{id}/{action}", a.manage)
	mux.HandleFunc("GET /api/v1/state", a.householdState)
	mux.HandleFunc("GET /api/v1/library/{section}", a.library)
	mux.HandleFunc("PUT /api/v1/library/favorites/{titleId}", a.titleFavorite)
	mux.HandleFunc("DELETE /api/v1/library/favorites/{titleId}", a.titleFavorite)
	mux.HandleFunc("PUT /api/v1/favorites/{releaseId}", a.favorite)
	mux.HandleFunc("DELETE /api/v1/favorites/{releaseId}", a.favorite)
	mux.HandleFunc("GET /api/v1/playback/{sourceId}", a.getPlayback)
	mux.HandleFunc("PUT /api/v1/playback/{sourceId}", a.putPlayback)
	mux.HandleFunc("GET /api/v1/playback/{sourceId}/preferences", a.getPlaybackPreferences)
	mux.HandleFunc("PUT /api/v1/playback/{sourceId}/preferences", a.putPlaybackPreferences)
	mux.HandleFunc("PUT /api/v1/playback/{sourceId}/watched", a.putWatched)
	mux.HandleFunc("GET /api/v1/streams/{id}", a.stream)
	mux.HandleFunc("HEAD /api/v1/streams/{id}", a.stream)
	mux.HandleFunc("GET /api/v1/streams/{id}/browser", a.browserStream)
	mux.HandleFunc("GET /api/v1/streams/{id}/snap", a.streamSnap)
	mux.Handle("/", appShell(webFS))
	return recoverer(log, access(log, trusted(settings, corsAPI(mux))))
}

// appShell serves the embedded web build. A GET outside /api/ that matches no
// asset gets index.html, so client-side routes (/library/downloads, /search?q=)
// survive refreshes and shared links; the app parses the path it was served
// under. The shell must revalidate: it references asset hashes that change
// with every frontend build.
func appShell(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
			if _, err := fs.Stat(web, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				if index, indexErr := fs.ReadFile(web, "index.html"); indexErr == nil {
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.Write(index)
					return
				}
			}
		}
		files.ServeHTTP(w, r)
	})
}

func (a *API) info(w http.ResponseWriter, r *http.Request) {
	settings := a.settings.Get()
	write(w, 200, map[string]any{"name": "Torrent TV", "instanceName": settings.InstanceName, "version": a.version, "apiVersion": "v1", "configured": configured(settings), "capabilities": []string{"catalog", "canonicalCatalog", "metadata", "artworkProxy", "qbittorrent", "rangeStreaming", "mediaInfo", "audioAnchor", "settingsFile", "householdState", "canonicalFavorites", "persistentJobs", "subtitles", "serverDiscovery", "browserAudioTranscode"}})
}

func (a *API) clientDiagnostic(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var value struct {
		Level   string         `json:"level"`
		Message string         `json:"message"`
		Context map[string]any `json:"context"`
	}
	if err := decode(r, &value); err != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("invalid client diagnostic: %w", err))
		return
	}
	value.Message = strings.TrimSpace(value.Message)
	if value.Message == "" || len(value.Message) > 1000 {
		problem(w, http.StatusBadRequest, fmt.Errorf("diagnostic message must contain 1 to 1000 characters"))
		return
	}
	attributes := []any{"client", r.UserAgent(), "remote", r.RemoteAddr, "context", value.Context}
	if strings.EqualFold(value.Level, "error") {
		a.log.Error("client diagnostic: "+value.Message, attributes...)
	} else {
		a.log.Warn("client diagnostic: "+value.Message, attributes...)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	v := a.settings.Get()
	write(w, 200, RedactedSettings(v, a.settings.Path()))
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) {
	var v config.Settings
	if err := decode(r, &v); err != nil {
		problem(w, 400, err)
		return
	}
	old := a.settings.Get()
	if err := config.EnsureNativePathsWritable(v.DownloadEngine, v.DownloadRoot, v.TorrentSessionDir); err != nil {
		problem(w, 400, err)
		return
	}
	if err := a.settings.Save(v); err != nil {
		problem(w, 400, err)
		return
	}
	current := a.settings.Get()
	restart := config.RestartRequired(old, current)
	write(w, 200, map[string]any{"saved": true, "restartRequired": restart})
}

func (a *API) settingsSchema(w http.ResponseWriter, r *http.Request) {
	write(w, http.StatusOK, map[string]any{"items": SettingsSchema(a.settings)})
}

func (a *API) testDependency(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	switch r.PathValue("name") {
	case "filelist":
		n, err := a.service.TestFileList(ctx)
		if err != nil {
			problem(w, 502, err)
			return
		}
		write(w, 200, map[string]any{"success": true, "message": "Connected to FileList", "count": n})
	case "qbittorrent":
		v, err := a.service.TestEngine(ctx)
		if err != nil {
			problem(w, 502, err)
			return
		}
		engine := a.settings.Get().DownloadEngine
		if engine == "" {
			engine = "native"
		}
		write(w, 200, map[string]any{"success": true, "message": "Connected to " + engine + " torrent engine: " + v})
	case "storage":
		message, err := a.service.TestStorage()
		if err != nil {
			problem(w, 503, err)
			return
		}
		write(w, 200, map[string]any{"success": true, "message": message})
	case "tmdb":
		if a.settings.Get().TMDBAPIKey == "" {
			problem(w, 409, fmt.Errorf("TMDB is not configured"))
			return
		}
		write(w, 200, map[string]any{"success": true, "message": "TMDB credentials are configured; the next visible title will verify lookup access"})
	case "subtitles":
		v := a.settings.Get()
		if v.SubDLAPIKey == "" {
			problem(w, 409, fmt.Errorf("no downloadable subtitle provider is configured"))
			return
		}
		write(w, 200, map[string]any{"success": true, "message": "A downloadable subtitle provider is configured"})
	case "subdl":
		message, err := a.service.TestSubtitleProvider(ctx, r.PathValue("name"))
		if err != nil {
			problem(w, 502, err)
			return
		}
		write(w, 200, map[string]any{"success": true, "message": message})
	default:
		problem(w, 404, fmt.Errorf("unknown dependency"))
	}
}

func (a *API) catalogSync(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode string `json:"mode"`
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, err)
		return
	}
	job, err := a.service.SyncCatalog(input.Mode)
	if err != nil {
		problem(w, 409, err)
		return
	}
	write(w, http.StatusAccepted, job)
}

func (a *API) catalogStatus(w http.ResponseWriter, r *http.Request) {
	status, err := a.service.CatalogStatus(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	write(w, 200, status)
}

func (a *API) ensureMetadata(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TitleIDs []string `json:"titleIds"`
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"queued": a.service.EnsureMetadata(r.Context(), input.TitleIDs)})
}

func (a *API) categories(w http.ResponseWriter, r *http.Request) {
	items := []domain.Category{}
	for _, x := range domain.Categories {
		if !x.DefaultBlacklisted {
			items = append(items, x)
		}
	}
	write(w, 200, map[string]any{"items": items, "nextCursor": nil, "total": len(items)})
}

func (a *API) catalog(w http.ResponseWriter, r *http.Request) {
	limit := integer(r, "pageSize", 24)
	offset, err := sqlite.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		problem(w, 400, err)
		return
	}
	page, err := a.service.Browse(r.Context(), r.URL.Query().Get("search"), r.URL.Query().Get("category"), limit, offset)
	if err != nil {
		problem(w, 502, err)
		return
	}
	write(w, 200, page)
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	page, err := a.service.Search(r.Context(), strings.TrimSpace(r.URL.Query().Get("query")))
	if err != nil {
		problem(w, 502, err)
		return
	}
	write(w, 200, page)
}

func (a *API) searchTitles(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Query string `json:"query"`
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, err)
		return
	}
	query := strings.TrimSpace(input.Query)
	page, err := a.service.SearchTitles(r.Context(), query)
	if err != nil {
		problem(w, 400, err)
		return
	}
	job, err := a.service.QueueTrackerSearch(r.Context(), query, false)
	if err != nil {
		problem(w, 409, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"items": page.Items, "nextCursor": page.NextCursor, "total": page.Total, "job": job})
}

func (a *API) refreshCatalogTitle(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Query string `json:"query"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &input); err != nil {
			problem(w, 400, err)
			return
		}
	}
	if strings.TrimSpace(input.Query) == "" {
		detail, err := a.service.CatalogDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			problem(w, 404, err)
			return
		}
		input.Query = detail.Title.Title
	}
	job, err := a.service.QueueTitleRefresh(r.Context(), r.PathValue("id"), input.Query, false)
	if err != nil {
		problem(w, 409, err)
		return
	}
	write(w, http.StatusAccepted, job)
}

func (a *API) catalogTitles(w http.ResponseWriter, r *http.Request) {
	offset, err := sqlite.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	q := domain.CatalogQuery{
		Search: strings.TrimSpace(r.URL.Query().Get("search")), Category: r.URL.Query().Get("category"),
		Kind: domain.MediaKind(r.URL.Query().Get("kind")), Resolution: r.URL.Query().Get("resolution"), HDR: r.URL.Query().Get("hdr"),
		Quality: r.URL.Query().Get("quality"), Codec: r.URL.Query().Get("codec"), MinSeeders: integer(r, "minSeeders", 0),
		Sort: r.URL.Query().Get("sort"), Limit: integer(r, "pageSize", 24), Offset: offset,
	}
	if q.Kind != "" && q.Kind != domain.MediaMovie && q.Kind != domain.MediaSeries {
		problem(w, http.StatusBadRequest, fmt.Errorf("kind must be movie or series"))
		return
	}
	for name, target := range map[string]**bool{"freeleech": &q.Freeleech, "internal": &q.Internal, "moderated": &q.Moderated} {
		if raw, ok := r.URL.Query()[name]; ok {
			value, parseErr := strconv.ParseBool(raw[len(raw)-1])
			if parseErr != nil {
				problem(w, http.StatusBadRequest, fmt.Errorf("%s must be true or false", name))
				return
			}
			*target = &value
		}
	}
	page, err := a.service.CatalogTitles(r.Context(), q)
	if err != nil {
		problem(w, http.StatusBadGateway, err)
		return
	}
	write(w, http.StatusOK, page)
}

func (a *API) catalogTitle(w http.ResponseWriter, r *http.Request) {
	detail, err := a.service.CatalogDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	write(w, http.StatusOK, detail)
}

func (a *API) catalogFacets(w http.ResponseWriter, r *http.Request) {
	facets, err := a.service.CatalogFacets(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusOK, facets)
}

func (a *API) artwork(w http.ResponseWriter, r *http.Request) {
	path, contentType, err := a.service.Artwork(r.Context(), r.PathValue("titleId"), r.PathValue("kind"))
	if err != nil {
		problem(w, http.StatusNotFound, err)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	http.ServeFile(w, r, path)
}

func (a *API) prepare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FileIndex int `json:"fileIndex"`
	}
	body.FileIndex = -1
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			problem(w, 400, err)
			return
		}
	}
	d, err := a.service.Prepare(r.Context(), r.PathValue("id"), body.FileIndex)
	if err != nil {
		var fit *domain.AllocationError
		if errors.As(err, &fit) {
			// The starvation path of ADR-0004: a conflict, not a gateway
			// failure — the detail names the Allocation and the space required.
			problem(w, http.StatusConflict, err)
			return
		}
		problem(w, 502, err)
		return
	}
	write(w, 202, downloadDTO(d))
}

func (a *API) prepareSeason(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Season int `json:"season"`
	}
	if err := decode(r, &body); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	if body.Season <= 0 {
		problem(w, http.StatusBadRequest, fmt.Errorf("season must be greater than zero"))
		return
	}
	items, err := a.service.PrepareSeason(r.Context(), r.PathValue("id"), body.Season)
	if err != nil {
		var fit *domain.AllocationError
		if errors.As(err, &fit) {
			problem(w, http.StatusConflict, err)
			return
		}
		problem(w, http.StatusBadGateway, err)
		return
	}
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = downloadDTO(item)
	}
	write(w, http.StatusAccepted, map[string]any{"items": out, "nextCursor": nil, "total": len(out)})
}

func (a *API) downloads(w http.ResponseWriter, r *http.Request) {
	items, err := a.service.Downloads(r.Context())
	if err != nil {
		problem(w, 502, err)
		return
	}
	out := make([]any, len(items))
	for i, x := range items {
		out[i] = downloadDTO(x)
	}
	write(w, 200, map[string]any{"items": out, "nextCursor": nil, "total": len(out)})
}

func (a *API) nextEpisode(w http.ResponseWriter, r *http.Request) {
	next, err := a.service.NextEpisode(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, http.StatusConflict, err)
		return
	}
	if next == nil {
		write(w, http.StatusOK, nil)
		return
	}
	write(w, http.StatusAccepted, downloadDTO(*next))
}

func (a *API) jobs(w http.ResponseWriter, r *http.Request) {
	offset, decodeErr := sqlite.DecodeCursor(r.URL.Query().Get("cursor"))
	if decodeErr != nil {
		problem(w, 400, decodeErr)
		return
	}
	updatedSince := int64(0)
	if hours := integer(r, "updatedHours", 0); hours > 0 && hours <= 24*365 {
		updatedSince = time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	}
	page, err := a.service.QueryJobs(r.Context(), strings.TrimSpace(r.URL.Query().Get("search")), r.URL.Query().Get("state"), r.URL.Query().Get("kind"), r.URL.Query().Get("retryable"), updatedSince, integer(r, "pageSize", 24), offset)
	if err != nil {
		problem(w, 500, err)
		return
	}
	write(w, 200, page)
}

func (a *API) job(w http.ResponseWriter, r *http.Request) {
	job, err := a.service.Job(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, fmt.Errorf("job not found"))
		} else {
			problem(w, 500, err)
		}
		return
	}
	logs, _ := a.service.JobLogs(r.Context(), job.ID, 0, 100)
	rows := make([]map[string]any, 0, len(logs.Items))
	for i := len(logs.Items) - 1; i >= 0; i-- {
		entry := logs.Items[i]
		rows = append(rows, map[string]any{"id": entry.ID, "at": entry.CreatedAt, "attempt": entry.Attempt, "level": entry.Level, "phase": entry.Phase, "message": entry.Message, "context": entry.Context})
	}
	write(w, 200, map[string]any{"job": job, "logs": rows})
}

func (a *API) jobLogs(w http.ResponseWriter, r *http.Request) {
	before := int64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		offset, err := sqlite.DecodeCursor(raw)
		if err != nil {
			problem(w, 400, err)
			return
		}
		before = int64(offset)
	}
	page, err := a.service.JobLogs(r.Context(), r.PathValue("id"), before, integer(r, "pageSize", 100))
	if err != nil {
		problem(w, 500, err)
		return
	}
	write(w, 200, page)
}

func (a *API) retryJob(w http.ResponseWriter, r *http.Request) {
	job, err := a.service.RetryJob(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 409, err)
		return
	}
	write(w, http.StatusAccepted, job)
}

func (a *API) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	replay := r.URL.Query().Has("after")
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		replay = true
		if n, e := strconv.ParseInt(header, 10, 64); e == nil && n > after {
			after = n
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	send := func(event domain.Event) {
		b, _ := json.Marshal(event)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Kind, b)
		flusher.Flush()
	}
	if replay {
		if events, err := a.service.Events(r.Context(), after, 200); err == nil {
			for _, event := range events {
				send(event)
			}
		}
	}
	stream, cancel := a.service.SubscribeEvents()
	defer cancel()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-stream:
			if !open {
				return
			}
			send(event)
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (a *API) manage(w http.ResponseWriter, r *http.Request) {
	deleteFiles := r.URL.Query().Get("deleteFiles") == "true"
	if r.PathValue("action") == "remove" && !deleteFiles {
		problem(w, http.StatusBadRequest, fmt.Errorf("torrent removal must also delete downloaded files; use DELETE /downloads/{id}"))
		return
	}
	if err := a.service.Manage(r.Context(), r.PathValue("id"), r.PathValue("action"), deleteFiles); err != nil {
		problem(w, 409, err)
		return
	}
	w.WriteHeader(204)
}

func (a *API) deleteDownload(w http.ResponseWriter, r *http.Request) {
	if err := a.service.Manage(r.Context(), r.PathValue("id"), "remove", true); err != nil {
		problem(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) searchSubtitles(w http.ResponseWriter, r *http.Request) {
	language := strings.TrimSpace(r.URL.Query().Get("language"))
	if language == "" {
		language = a.settings.Get().PreferredSubtitleLanguage
	}
	scope, scopeErr := application.ParseSubtitleSearchScope(r.URL.Query().Get("scope"))
	if scopeErr != nil {
		problem(w, 400, scopeErr)
		return
	}
	items, warnings, err := a.service.SearchSubtitles(r.Context(), r.PathValue("id"), language, scope)
	if err != nil {
		problem(w, 502, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "warnings": warnings, "nextCursor": nil, "total": len(items)})
}

func (a *API) prepareSubtitle(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
		Format   string `json:"format"`
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, err)
		return
	}
	if input.Provider == "" || input.ID == "" {
		problem(w, 400, fmt.Errorf("provider and id are required"))
		return
	}
	asset, err := a.service.PrepareSubtitle(r.Context(), r.PathValue("id"), strings.ToLower(input.Provider), input.ID, input.Format)
	if err != nil {
		problem(w, 502, err)
		return
	}
	write(w, 201, asset)
}

func (a *API) subtitle(w http.ResponseWriter, r *http.Request) {
	asset := r.PathValue("asset")
	ext := filepathExt(asset)
	if ext != ".smi" && ext != ".vtt" {
		problem(w, 404, fmt.Errorf("subtitle asset not found"))
		return
	}
	id := strings.TrimSuffix(asset, ext)
	format := "sami"
	if ext == ".vtt" {
		format = "vtt"
	}
	path, err := a.service.SubtitlePath(id, format)
	if err != nil {
		problem(w, 404, err)
		return
	}
	if ext == ".vtt" {
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/x-sami; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

func (a *API) householdState(w http.ResponseWriter, r *http.Request) {
	state, err := a.service.HouseholdState(r.Context())
	if err != nil {
		problem(w, 500, err)
		return
	}
	write(w, 200, state)
}

func (a *API) library(w http.ResponseWriter, r *http.Request) {
	state, err := a.service.HouseholdState(r.Context())
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	var items []domain.HouseholdItem
	switch r.PathValue("section") {
	case "continue-watching":
		items = state.ContinueWatching
	case "favorites":
		items = state.Favorites
	case "watched":
		items = state.Watched
	case "recent":
		items = state.Recent
	case "dashboard":
		write(w, http.StatusOK, state)
		return
	case "categories":
		bySource := map[string]domain.HouseholdItem{}
		for _, group := range [][]domain.HouseholdItem{state.Favorites, state.ContinueWatching, state.Watched, state.Recent} {
			for _, item := range group {
				titleID := item.TitleID
				if titleID == "" && item.Catalog != nil {
					titleID = item.Catalog.ID
				}
				key := ""
				if titleID != "" {
					key = "title:" + titleID + "|category:" + strings.ToLower(item.Release.Category)
				} else if item.SourceID != "" {
					key = "source:" + item.SourceID
				} else {
					key = "release:" + item.Release.ID
				}
				if current, exists := bySource[key]; !exists || item.UpdatedAt.After(current.UpdatedAt) {
					bySource[key] = item
				}
			}
		}
		selected := strings.TrimSpace(r.URL.Query().Get("category"))
		if selected != "" {
			items = []domain.HouseholdItem{}
			for _, item := range bySource {
				if strings.EqualFold(item.Release.Category, selected) {
					items = append(items, item)
				}
			}
			sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
			write(w, http.StatusOK, map[string]any{"items": items, "nextCursor": nil, "total": len(items)})
			return
		}
		counts := map[string]int{}
		for _, item := range bySource {
			if item.Release.Category != "" {
				counts[item.Release.Category]++
			}
		}
		categories := make([]domain.LibraryCategory, 0, len(counts))
		for name, count := range counts {
			categories = append(categories, domain.LibraryCategory{Name: name, Count: count})
		}
		sort.Slice(categories, func(i, j int) bool { return categories[i].Name < categories[j].Name })
		write(w, http.StatusOK, map[string]any{"items": categories, "nextCursor": nil, "total": len(categories)})
		return
	default:
		problem(w, http.StatusNotFound, fmt.Errorf("unknown library section"))
		return
	}
	write(w, http.StatusOK, map[string]any{"items": items, "nextCursor": nil, "total": len(items)})
}

func (a *API) titleFavorite(w http.ResponseWriter, r *http.Request) {
	err := a.service.SetTitleFavorite(r.Context(), r.PathValue("titleId"), r.Method == http.MethodPut)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, err)
		} else {
			problem(w, http.StatusInternalServerError, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) favorite(w http.ResponseWriter, r *http.Request) {
	if err := a.service.SetFavorite(r.Context(), r.PathValue("releaseId"), r.Method == http.MethodPut); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, err)
		} else {
			problem(w, 500, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getPlayback(w http.ResponseWriter, r *http.Request) {
	p, err := a.service.Playback(r.Context(), r.PathValue("sourceId"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, err)
		} else {
			problem(w, 500, err)
		}
		return
	}
	write(w, 200, p)
}

func (a *API) putPlayback(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PositionMS int64 `json:"positionMs"`
		DurationMS int64 `json:"durationMs"`
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, err)
		return
	}
	if input.PositionMS < 0 || input.DurationMS < 0 {
		problem(w, 400, fmt.Errorf("positionMs and durationMs cannot be negative"))
		return
	}
	p, err := a.service.UpdatePlayback(r.Context(), r.PathValue("sourceId"), input.PositionMS, input.DurationMS)
	if err != nil {
		// Mirror the stream handler: a missing source is permanent (404), every
		// other persistence failure is transient (503) so clients keep saving
		// instead of dropping playback persistence on a server restart.
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, err)
		} else {
			w.Header().Set("Retry-After", "2")
			problem(w, http.StatusServiceUnavailable, err)
		}
		return
	}
	write(w, 200, p)
}

func (a *API) getPlaybackPreferences(w http.ResponseWriter, r *http.Request) {
	p, err := a.service.PlaybackPreferences(r.Context(), r.PathValue("sourceId"))
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	write(w, http.StatusOK, p)
}

func (a *API) putPlaybackPreferences(w http.ResponseWriter, r *http.Request) {
	var input domain.PlaybackPreferences
	if err := decode(r, &input); err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	p, err := a.service.UpdatePlaybackPreferences(r.Context(), r.PathValue("sourceId"), input)
	if err != nil {
		problem(w, http.StatusConflict, err)
		return
	}
	write(w, http.StatusOK, p)
}

func (a *API) putWatched(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Watched bool `json:"watched"`
	}
	if err := decode(r, &input); err != nil {
		problem(w, 400, err)
		return
	}
	p, err := a.service.SetWatched(r.Context(), r.PathValue("sourceId"), input.Watched)
	if err != nil {
		problem(w, 409, err)
		return
	}
	write(w, 200, p)
}

// audioAnchor measures the decoded-audio content span of one fetch window
// (ADR-0002): the client anchors decoded audio by these measured timestamps
// instead of average-bitrate guesses.
func (a *API) audioAnchor(w http.ResponseWriter, r *http.Request) {
	startByte, startErr := strconv.ParseInt(r.URL.Query().Get("startByte"), 10, 64)
	lengthBytes, lengthErr := strconv.ParseInt(r.URL.Query().Get("lengthBytes"), 10, 64)
	streamIndex, streamErr := strconv.Atoi(r.URL.Query().Get("streamIndex"))
	if startErr != nil || lengthErr != nil || streamErr != nil {
		problem(w, http.StatusBadRequest, fmt.Errorf("startByte, lengthBytes, and streamIndex are required integers"))
		return
	}
	span, retryable, err := a.service.AudioSpan(r.Context(), r.PathValue("id"), startByte, lengthBytes, streamIndex)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, err)
			return
		}
		var invalid *application.InvalidAudioWindowError
		if errors.As(err, &invalid) {
			problem(w, http.StatusUnprocessableEntity, err)
			return
		}
		if retryable {
			w.Header().Set("Retry-After", "2")
			problem(w, http.StatusServiceUnavailable, err)
			return
		}
		problem(w, http.StatusInternalServerError, fmt.Errorf("measure audio span: %w", err))
		return
	}
	write(w, http.StatusOK, span)
}

func (a *API) mediaInfo(w http.ResponseWriter, r *http.Request) {
	info, complete, err := a.service.MediaInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, err)
			return
		}
		if !complete {
			w.Header().Set("Retry-After", "2")
			problem(w, http.StatusServiceUnavailable, err)
			return
		}
		problem(w, http.StatusUnprocessableEntity, fmt.Errorf("read original media details: %w", err))
		return
	}
	write(w, http.StatusOK, info)
}

func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	d, err := a.service.Acquire(r.Context(), r.PathValue("id"))
	if err != nil {
		// A removed source is gone for good; anything else (restart in progress,
		// locked database, engine hiccup) is transient — tell clients to retry
		// instead of letting them treat the stream as unavailable forever.
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusNotFound, err)
		} else {
			w.Header().Set("Retry-After", "2")
			problem(w, http.StatusServiceUnavailable, err)
		}
		return
	}
	defer a.service.Release(d.ID)
	start, end, partial, ok := parseRange(r.Header.Get("Range"), d.SizeBytes)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", d.SizeBytes))
		problem(w, 416, fmt.Errorf("invalid or multiple byte range"))
		return
	}
	settings := a.settings.Get()
	// Gate the response on a small leading slice, not the whole startup
	// buffer: a player's request patience (~10s) is far shorter than the
	// time a slow swarm needs to fetch 128 MiB, so waiting for the full
	// window made play-while-downloading impossible. Later chunks grow
	// adaptively so fast swarms keep large reads without extra qBittorrent
	// round-trips.
	firstChunk := settings.StreamStartBytes
	if remaining := end - start + 1; firstChunk > remaining {
		firstChunk = remaining
	}
	if err := a.service.ValidateSourcePath(d); err != nil {
		problem(w, 409, err)
		return
	}
	currentPath := d.AbsolutePath
	if r.Method != http.MethodHead {
		waitStarted := time.Now()
		currentPath, err = a.service.ReadableRangePath(r.Context(), d, start, firstChunk)
		if err != nil {
			a.log.Warn("stream preflight failed", "sourceId", d.ID, "rangeStart", start, "rangeBytes", firstChunk, "waitMs", time.Since(waitStarted).Milliseconds(), "error", err)
			w.Header().Set("Retry-After", "2")
			problem(w, 503, err)
			return
		}
		a.log.Info("stream range ready", "sourceId", d.ID, "rangeStart", start, "rangeBytes", firstChunk, "waitMs", time.Since(waitStarted).Milliseconds())
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType(d.FilePath))
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, d.SizeBytes))
		w.WriteHeader(206)
	} else {
		w.WriteHeader(200)
	}
	if r.Method == http.MethodHead {
		return
	}
	position := start
	chunkSize := firstChunk
	for position <= end {
		chunk := chunkSize
		if chunk > settings.ReadAheadBytes {
			chunk = settings.ReadAheadBytes
		}
		if remaining := end - position + 1; chunk > remaining {
			chunk = remaining
		}
		if position != start {
			currentPath, err = a.service.ReadableRangePath(r.Context(), d, position, chunk)
			if err != nil {
				a.log.Warn("stream range wait stopped", "sourceId", d.ID, "rangeStart", position, "rangeBytes", chunk, "error", err)
				return
			}
		}
		if err := copyRange(r.Context(), w, currentPath, position, chunk); err != nil {
			if !errors.Is(err, context.Canceled) {
				a.log.Warn("stream read stopped", "sourceId", d.ID, "error", err)
			}
			return
		}
		position += chunk
		chunkSize = chunk * 2
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func copyRange(ctx context.Context, w io.Writer, path string, offset, count int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	remaining := count
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		want := int64(len(buf))
		if remaining < want {
			want = remaining
		}
		n, err := io.ReadFull(f, buf[:want])
		if n > 0 {
			if _, e := w.Write(buf[:n]); e != nil {
				return e
			}
			remaining -= int64(n)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// streamSnap reports the effective compatibility-stream start for a seek
// target: the video keyframe the route will actually start on. Clients use it
// to keep their clock and subtitle offsets aligned with the real content
// start instead of the requested position.
func (a *API) streamSnap(w http.ResponseWriter, r *http.Request) {
	settings := a.settings.Get()
	_, port, err := net.SplitHostPort(settings.ListenAddress)
	if err != nil || port == "" {
		problem(w, http.StatusInternalServerError, fmt.Errorf("invalid listen address for browser audio compatibility"))
		return
	}
	requested := parseStartQuery(r.URL.Query().Get("startMs"), 0)
	input := "http://127.0.0.1:" + port + "/api/v1/streams/" + r.PathValue("id")
	start, probed := snapStartToVideoKeyframe(r.Context(), settings.FFprobePath, input, requested, a.log)
	write(w, 200, map[string]any{"requested": requested, "startMs": start, "snapped": probed})
}

// browserStream preserves the original video while converting browser-hostile
// audio (for example E-AC-3 in an MKV release) to AAC in a fragmented MP4.
// FFmpeg reads through the ordinary range-aware stream, so the same local vs.
// progressive playback strategy remains authoritative while a torrent grows.
func (a *API) browserStream(w http.ResponseWriter, r *http.Request) {
	settings := a.settings.Get()
	_, port, err := net.SplitHostPort(settings.ListenAddress)
	if err != nil || port == "" {
		problem(w, http.StatusInternalServerError, fmt.Errorf("invalid listen address for browser audio compatibility"))
		return
	}
	info, complete, err := a.service.MediaInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		if !complete {
			w.Header().Set("Retry-After", "2")
			problem(w, http.StatusServiceUnavailable, err)
		} else {
			problem(w, http.StatusUnprocessableEntity, err)
		}
		return
	}
	input := "http://127.0.0.1:" + port + "/api/v1/streams/" + r.PathValue("id")
	// Stream-copied video resumes on the previous keyframe while re-encoded
	// audio resumes exactly at the target, so a raw seek leaves the audio
	// leading the picture by up to one GOP. Snap the target onto the
	// keyframe: both streams then start at the same content point. Clients
	// that already resolved the snap (snapped=1) reuse it and spare the Pi
	// a second probe per seek.
	startMs := parseStartQuery(r.URL.Query().Get("startMs"), info.DurationMS)
	if r.URL.Query().Get("snapped") != "1" {
		probed, _ := snapStartToVideoKeyframe(r.Context(), settings.FFprobePath, input, startMs, a.log)
		startMs = probed
	}
	args, _, err := browserStreamArgs(input, info, r.URL.Query().Get("audioTrack"), strconv.FormatInt(startMs, 10))
	if err != nil {
		problem(w, http.StatusBadRequest, err)
		return
	}
	cmd := exec.CommandContext(r.Context(), settings.FFmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		problem(w, http.StatusInternalServerError, err)
		return
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		problem(w, http.StatusServiceUnavailable, fmt.Errorf("start browser audio compatibility stream: %w", err))
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, copyErr := io.Copy(w, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil && !errors.Is(copyErr, context.Canceled) {
		a.log.Warn("browser compatibility stream stopped", "sourceId", r.PathValue("id"), "error", copyErr)
	} else if waitErr != nil && r.Context().Err() == nil {
		a.log.Warn("browser compatibility transcode stopped", "sourceId", r.PathValue("id"), "error", waitErr)
	}
}

// browserStreamArgs selects the requested (default, then English) audio track
// and builds the transcode argument vector. Video is always copied: the
// Raspberry Pi never re-encodes video, only the selected audio stream.
func browserStreamArgs(input string, info domain.MediaInfo, requestedTrack, requestedStart string) ([]string, domain.MediaAudioTrack, error) {
	if len(info.AudioTracks) == 0 {
		return nil, domain.MediaAudioTrack{}, fmt.Errorf("the original media has no audio track")
	}
	track := info.AudioTracks[0]
	for _, candidate := range info.AudioTracks {
		if candidate.Default {
			track = candidate
			break
		}
	}
	for _, candidate := range info.AudioTracks {
		language := strings.ToLower(candidate.Language)
		if strings.HasPrefix(language, "en") || strings.HasPrefix(language, "eng") {
			track = candidate
			break
		}
	}
	if requestedTrack != "" {
		index, err := strconv.Atoi(requestedTrack)
		if err != nil || index < 0 {
			return nil, domain.MediaAudioTrack{}, fmt.Errorf("audioTrack must be an original audio stream index")
		}
		found := false
		for _, candidate := range info.AudioTracks {
			if candidate.Index == index {
				track, found = candidate, true
				break
			}
		}
		if !found {
			return nil, domain.MediaAudioTrack{}, fmt.Errorf("audioTrack %d is not an audio stream in the original media", index)
		}
	}
	startMS := int64(0)
	if requestedStart != "" {
		value, err := strconv.ParseInt(requestedStart, 10, 64)
		if err != nil || value < 0 || info.DurationMS <= 0 || value >= info.DurationMS {
			return nil, domain.MediaAudioTrack{}, fmt.Errorf("startMs must be between 0 and the original duration")
		}
		startMS = value
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if startMS > 0 {
		args = append(args, "-ss", strconv.FormatFloat(float64(startMS)/1000, 'f', 3, 64))
	}
	args = append(
		args,
		"-i", input,
		"-map", "0:v:0", "-map", "0:"+strconv.Itoa(track.Index),
		// Raspberry Pi safety invariant: video is always copied. Only the selected
		// audio stream is transcoded for browser compatibility.
		"-c:v", "copy", "-c:a", "aac", "-ac", "2", "-b:a", "192k",
		// Without this, copied video keeps the GOP before the seek point while
		// the re-encoded audio starts exactly there: the picture led the sound
		// by up to one GOP. Discard prior frames so both streams start at the
		// seek point. Requires FFmpeg >= 6.1.
		"-copypriorss", "0",
		"-map_metadata", "-1", "-avoid_negative_ts", "make_zero",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)
	return args, track, nil
}

func downloadDTO(d domain.Download) map[string]any {
	playbackMode := "progressive"
	// Seeding means the selected payload finished downloading (deselected
	// season-pack files skew progress below one). Queued relies on progress
	// alone: complete packs serve from disk, in-flight ones stream. Legacy
	// rows carry raw qBittorrent strings where every *UP state has the same
	// meaning.
	state := strings.ToLower(d.State)
	if d.Progress >= 1 || state == domain.StateSeeding || strings.HasSuffix(state, "up") || state == "completed" {
		playbackMode = "local"
	}
	return map[string]any{"id": d.ID, "releaseId": d.ReleaseID, "titleId": d.TitleID, "displayTitle": d.DisplayTitle, "releaseName": d.ReleaseName, "category": d.Category, "releaseSizeBytes": d.ReleaseSizeBytes, "trackerSeeders": d.TrackerSeeders, "rating": d.Rating, "ratingVotes": d.RatingVotes, "ratingProvider": d.RatingProvider, "parsed": d.Parsed, "engineId": d.EngineID, "fileIndex": d.FileIndex, "filePath": d.FilePath, "mimeType": contentType(d.FilePath), "sizeBytes": d.SizeBytes, "state": d.State, "progress": d.Progress, "playbackMode": playbackMode, "downloadedBytes": d.DownloadedBytes, "speedBytesPerSecond": d.SpeedBytesPerSecond, "uploadSpeedBytesPerSecond": d.UploadSpeedBytesPerSecond, "etaSeconds": d.ETASeconds, "peers": d.Peers, "seeds": d.Seeds, "bufferedBytes": d.BufferedBytes, "leased": d.Leased, "error": d.Error, "createdAt": d.CreatedAt, "updatedAt": d.UpdatedAt, "streamUrl": "/api/v1/streams/" + d.ID, "browserStreamUrl": "/api/v1/streams/" + d.ID + "/browser"}
}

func parseRange(h string, length int64) (int64, int64, bool, bool) {
	if length <= 0 {
		return 0, 0, false, false
	}
	if h == "" {
		return 0, length - 1, false, true
	}
	if !strings.HasPrefix(strings.ToLower(h), "bytes=") || strings.Contains(h, ",") {
		return 0, 0, true, false
	}
	p := strings.SplitN(h[6:], "-", 2)
	if len(p) != 2 {
		return 0, 0, true, false
	}
	if p[0] == "" {
		n, e := strconv.ParseInt(p[1], 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, true, false
		}
		if n > length {
			n = length
		}
		return length - n, length - 1, true, true
	}
	start, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil || start < 0 || start >= length {
		return 0, 0, true, false
	}
	end := length - 1
	if p[1] != "" {
		end, e = strconv.ParseInt(p[1], 10, 64)
		if e != nil || end < start {
			return 0, 0, true, false
		}
		if end >= length {
			end = length - 1
		}
	}
	return start, end, true, true
}

func configured(v config.Settings) bool {
	if v.FileListUsername == "" || v.FileListPasskey == "" {
		return false
	}
	bothSet := v.QBittorrentUsername != "" && v.QBittorrentPassword != ""
	bothEmpty := v.QBittorrentUsername == "" && v.QBittorrentPassword == ""
	return bothSet || bothEmpty
}

func contentType(p string) string {
	switch strings.ToLower(filepathExt(p)) {
	case ".mkv":
		return "video/matroska"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".ts", ".m2ts":
		return "video/mp2t"
	}
	if v := mime.TypeByExtension(strings.ToLower(filepathExt(p))); v != "" {
		return v
	}
	return "application/octet-stream"
}

func filepathExt(p string) string {
	i := strings.LastIndexByte(p, '.')
	if i < 0 {
		return ""
	}
	return p[i:]
}

func integer(r *http.Request, k string, d int) int {
	n, e := strconv.Atoi(r.URL.Query().Get(k))
	if e != nil {
		return d
	}
	return n
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, err error) {
	write(w, status, map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": err.Error()})
}

// corsAPI lets locally-packaged TV clients (Tizen's widget runtime bypasses
// CORS; a WebView does not) call the API the server advertises on the home
// LAN. Preflights are answered here because the method-specific mux routes
// would otherwise drop OPTIONS on the SPA handler.
func corsAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, HEAD, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func trusted(s *config.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			problem(w, 403, fmt.Errorf("untrusted client"))
			return
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			problem(w, 403, fmt.Errorf("untrusted client"))
			return
		}
		for _, p := range s.TrustedPrefixes() {
			if p.Contains(addr) {
				next.ServeHTTP(w, r)
				return
			}
		}
		problem(w, 403, fmt.Errorf("client address is outside trusted CIDRs"))
	})
}

// statusRecorder captures the response status for the access log without
// disturbing the handler chain.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush forwards so SSE and range-streaming handlers keep their direct
// http.Flusher assertions working through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// access logs one DEBUG line per request with method, path, status, and
// duration. Per-request traffic is noise at Info — the GUI log viewer
// renders the attributes pretty ("GET /api/v1/jobs 200 12ms") while the
// file stays readable for everything that actually needs attention.
func access(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "durationMs", time.Since(started).Milliseconds())
	})
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("request panic", "panic", v)
				problem(w, 500, fmt.Errorf("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
