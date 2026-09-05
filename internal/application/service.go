package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/outbound"
)

type Service struct {
	catalog           TrackerCatalog
	engine            TorrentEngine
	engineRoutePrefix string
	repo              Repository
	settings          *config.Store
	subtitles         []SubtitleProvider
	locks             sync.Map
	metadata          MetadataProvider
	mediaProbe        MediaProbe
	freeSpace         func(string) (int64, error)
	metaQueue         chan metadataRequest
	eventMu           sync.Mutex
	eventSubscribers  map[chan domain.Event]struct{}
	syncMu            sync.Mutex
	refreshQueue      chan titleRefreshRequest
	searchQueue       chan trackerSearchRequest
	jobSlots          chan struct{}
	trackerSlots      chan struct{}
	pendingMu         sync.Mutex
	pendingMetadata   map[string]bool
	mediaInfoMu       sync.Mutex
	mediaInfoCache    map[string]cachedMediaInfo
	baseCtx           context.Context
	cancelBase        context.CancelFunc
	stopping          chan struct{}
	closeOnce         sync.Once
	closeErr          error
	wg                sync.WaitGroup
}

type cachedMediaInfo struct {
	identity string
	info     domain.MediaInfo
}

type metadataRequest struct {
	TitleID string
	IMDbID  string
	Kind    domain.MediaKind
}

type (
	titleRefreshRequest  struct{ TitleID, Query string }
	trackerSearchRequest struct{ Query string }
)

func NewService(c TrackerCatalog, e TorrentEngine, r Repository, s *config.Store, subtitles ...SubtitleProvider) *Service {
	limit := s.Get().MaxConcurrentJobs
	if limit < 1 {
		limit = 10
	}
	base, cancel := context.WithCancel(context.Background())
	service := &Service{catalog: c, engine: e, repo: r, settings: s, subtitles: subtitles, eventSubscribers: map[chan domain.Event]struct{}{}, refreshQueue: make(chan titleRefreshRequest, 256), searchQueue: make(chan trackerSearchRequest, 256), jobSlots: make(chan struct{}, limit), trackerSlots: make(chan struct{}, 1), pendingMetadata: map[string]bool{}, mediaInfoCache: map[string]cachedMediaInfo{}, freeSpace: freeDiskBytes, baseCtx: base, cancelBase: cancel, stopping: make(chan struct{})}
	service.wg.Add(2)
	go service.titleRefreshWorker()
	go service.trackerSearchWorker()
	return service
}

func (s *Service) SetMetadataProvider(provider MetadataProvider) {
	if provider == nil || s.metadata != nil {
		return
	}
	s.metadata = provider
	s.metaQueue = make(chan metadataRequest, 256)
	for i := 0; i < cap(s.jobSlots); i++ {
		s.wg.Add(1)
		go s.metadataWorker()
	}
}

func (s *Service) SetMediaProbe(probe MediaProbe) { s.mediaProbe = probe }

func (s *Service) acquire(ctx context.Context, tracker bool) error {
	if tracker {
		select {
		case s.trackerSlots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case s.jobSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		if tracker {
			<-s.trackerSlots
		}
		return ctx.Err()
	}
}

func (s *Service) release(tracker bool) {
	<-s.jobSlots
	if tracker {
		<-s.trackerSlots
	}
}

func (s *Service) jobLog(job domain.Job, level, phase, message string, fields map[string]any) {
	select {
	case <-s.stopping:
		return
	default:
	}
	entry, err := s.repo.AppendJobLog(s.baseCtx, domain.JobLog{JobID: job.ID, Attempt: job.Attempt, Level: level, Phase: phase, Message: message, Context: fields})
	if err == nil {
		s.publish("job.log", entry)
	}
}

func (s *Service) metadataWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopping:
			return
		case request, ok := <-s.metaQueue:
			if !ok {
				return
			}
			s.runMetadataRequest(request)
		}
	}
}

func (s *Service) runMetadataRequest(request metadataRequest) {
	if err := s.acquire(s.baseCtx, false); err != nil {
		return
	}
	job, _ := s.repo.GetJob(context.Background(), "metadata:"+request.TitleID)
	job.State = "running"
	job.Progress = .1
	job.Error = ""
	job.NextAttemptAt = nil
	job.UpdatedAt = time.Now().UTC()
	if job.Attempt < 1 {
		job.Attempt = 1
	}
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "metadata", "Metadata lookup started", map[string]any{"provider": "tmdb", "imdbId": request.IMDbID, "requestedKind": request.Kind})
	ctx, cancel := context.WithTimeout(s.baseCtx, 30*time.Second)
	settings := s.settings.Get()
	metadata, err := s.metadata.Lookup(ctx, request.IMDbID, request.Kind, settings.MetadataLanguage, settings.MetadataFallbackLanguage)
	now := time.Now().UTC()
	metadata.TitleID = request.TitleID
	if err != nil {
		metadata = domain.CatalogMetadata{TitleID: request.TitleID, Provider: "tmdb", FetchedAt: now, ExpiresAt: now.Add(10 * time.Minute), LastError: err.Error()}
		s.jobLog(job, "error", "metadata-match", "TMDB metadata lookup did not produce a usable media match", map[string]any{"provider": "tmdb", "imdbId": request.IMDbID, "requestedKind": request.Kind, "error": err.Error()})
		s.failOrWait(&job, err, "metadata")
	} else {
		job.State, job.Progress, job.Retryable, job.NextAttemptAt = "completed", 1, false, nil
	}
	_ = s.repo.SaveCatalogMetadata(ctx, metadata)
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(context.Background(), job)
	if err == nil {
		s.jobLog(job, "info", "complete", "Metadata lookup completed", nil)
	}
	payload := map[string]any{"titleId": request.TitleID, "job": job}
	if sources, sourceErr := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{request.TitleID}); sourceErr == nil && len(sources) > 0 {
		title := groupCatalog(sources, false)[0]
		applyMetadata(&title, metadata)
		payload["title"] = title
	}
	s.publish("metadata.updated", payload)
	cancel()
	s.pendingMu.Lock()
	delete(s.pendingMetadata, request.TitleID)
	s.pendingMu.Unlock()
	s.release(false)
}

func (s *Service) publish(kind string, payload any) {
	select {
	case <-s.stopping:
		return
	default:
	}
	b, _ := json.Marshal(payload)
	event, err := s.repo.AppendEvent(context.Background(), kind, string(b))
	if err != nil {
		return
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	for subscriber := range s.eventSubscribers {
		select {
		case subscriber <- event:
		default:
			close(subscriber)
			delete(s.eventSubscribers, subscriber)
		}
	}
}

// PublishEvent is the exported event-sink boundary (plan S3/S6):
// composition bridges the integration hubs — the portal hub and the
// self-update coordinator — onto the exact same journal-plus-fan-out path
// as internal publishers, so integration events reach both the event
// journal (SSE replay) and connected SSE subscribers (live delivery).
// It is the private publish verbatim; there is no second bus.
func (s *Service) PublishEvent(kind string, payload any) {
	s.publish(kind, payload)
}

func (s *Service) SubscribeEvents() (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, 32)
	s.eventMu.Lock()
	s.eventSubscribers[ch] = struct{}{}
	s.eventMu.Unlock()
	return ch, func() {
		s.eventMu.Lock()
		if _, ok := s.eventSubscribers[ch]; ok {
			delete(s.eventSubscribers, ch)
			close(ch)
		}
		s.eventMu.Unlock()
	}
}

func (s *Service) Events(ctx context.Context, after int64, limit int) ([]domain.Event, error) {
	return s.repo.ListEvents(ctx, after, limit)
}

func (s *Service) EnsureMetadata(ctx context.Context, titleIDs []string) int {
	return s.ensureMetadata(ctx, titleIDs, false)
}

func (s *Service) ensureMetadata(ctx context.Context, titleIDs []string, force bool) int {
	if len(titleIDs) > 24 {
		titleIDs = titleIDs[:24]
	}
	sources, err := s.repo.ListCatalogSourcesByTitleIDs(ctx, titleIDs)
	if err != nil {
		return 0
	}
	byID := map[string]domain.CatalogTitle{}
	for _, title := range groupCatalog(sources, false) {
		byID[title.ID] = title
	}
	queued := 0
	for _, id := range titleIDs {
		title, ok := byID[id]
		if !ok || title.IMDbID == "" {
			continue
		}
		if !force {
			if metadata, err := s.repo.GetCatalogMetadata(ctx, id); err == nil && metadata.ExpiresAt.After(time.Now()) && metadata.RatingVotes > 0 {
				continue
			}
		}
		s.pendingMu.Lock()
		if s.pendingMetadata[id] {
			s.pendingMu.Unlock()
			continue
		}
		s.pendingMetadata[id] = true
		s.pendingMu.Unlock()
		existing, _ := s.repo.GetJob(ctx, "metadata:"+id)
		attempt := existing.Attempt
		if force {
			attempt++
		}
		if attempt < 1 {
			attempt = 1
		}
		job := domain.Job{ID: "metadata:" + id, Kind: "metadata", State: "queued", Label: "Fetch metadata for " + title.Title, DedupeKey: "metadata:" + id, Attempt: attempt, UpdatedAt: time.Now().UTC()}
		if err := s.repo.SaveJob(ctx, job); err != nil {
			s.pendingMu.Lock()
			delete(s.pendingMetadata, id)
			s.pendingMu.Unlock()
			continue
		}
		s.jobLog(job, "info", "queue", "Metadata job queued", map[string]any{"forced": force})
		select {
		case s.metaQueue <- metadataRequest{TitleID: id, IMDbID: title.IMDbID, Kind: title.Kind}:
			queued++
		default:
			s.pendingMu.Lock()
			delete(s.pendingMetadata, id)
			s.pendingMu.Unlock()
			job.State = "failed"
			job.Error = "metadata queue is full"
			job.UpdatedAt = time.Now().UTC()
			_ = s.repo.SaveJob(ctx, job)
			s.jobLog(job, "error", "queue", job.Error, nil)
		}
	}
	return queued
}

func (s *Service) SyncCatalog(mode string) (domain.Job, error) {
	if mode != "latest" && mode != "rebuild" {
		return domain.Job{}, fmt.Errorf("mode must be latest or rebuild")
	}
	job := domain.Job{ID: "catalog-" + mode, Kind: "catalog-" + mode, State: "queued", Label: map[string]string{"latest": "Fetch latest FileList releases", "rebuild": "Rebuild FileList catalog"}[mode], DedupeKey: "catalog-" + mode, Attempt: 1, UpdatedAt: time.Now().UTC()}
	jobs, _ := s.repo.ListJobs(context.Background(), 200)
	for _, existing := range jobs {
		if existing.DedupeKey == job.DedupeKey && (existing.State == "queued" || existing.State == "running") {
			return existing, nil
		}
		if existing.DedupeKey == job.DedupeKey && existing.Attempt >= job.Attempt {
			job.Attempt = existing.Attempt + 1
		}
	}
	if err := s.repo.SaveJob(context.Background(), job); err != nil {
		return domain.Job{}, err
	}
	s.publish("job.updated", job)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runCatalogSync(job, mode)
	}()
	return job, nil
}

func (s *Service) runCatalogSync(job domain.Job, mode string) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err := s.acquire(s.baseCtx, true); err != nil {
		return
	}
	defer s.release(true)
	ctx := s.baseCtx
	job.State = "running"
	job.Progress = .02
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(ctx, job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "start", "Catalog synchronization started", map[string]any{"mode": mode})
	var err error
	total := 0
	if mode == "latest" {
		var items []domain.TorrentRelease
		items, err = s.catalog.Latest(ctx)
		if err == nil {
			total = len(items)
			err = s.repo.UpsertReleases(ctx, items)
		}
		_ = s.repo.RecordSync(ctx, "latest", total, err)
	} else {
		categories := []domain.Category{}
		for _, category := range domain.Categories {
			if !category.DefaultBlacklisted {
				categories = append(categories, category)
			}
		}
		for i, category := range categories {
			items, e := s.catalog.Category(ctx, category.ID)
			if e != nil {
				err = e
				break
			}
			if e = s.repo.UpsertReleases(ctx, items); e != nil {
				err = e
				break
			}
			total += len(items)
			job.Progress = float64(i+1) / float64(len(categories))
			job.UpdatedAt = time.Now().UTC()
			_ = s.repo.SaveJob(ctx, job)
			s.publish("job.updated", job)
		}
		_ = s.repo.RecordSync(ctx, "rebuild", total, err)
	}
	job.UpdatedAt = time.Now().UTC()
	if err != nil {
		s.failOrWait(&job, err, "catalog-sync")
	} else {
		job.State = "completed"
		job.Retryable = false
		job.NextAttemptAt = nil
		job.Progress = 1
		if retained, discoverable, countErr := s.repo.CatalogCounts(ctx); countErr == nil {
			job.Label = fmt.Sprintf("%s · %d refreshed · %d retained (%d discoverable)", job.Label, total, retained, discoverable)
		}
	}
	_ = s.repo.SaveJob(ctx, job)
	if err == nil {
		s.jobLog(job, "info", "complete", "Catalog synchronization completed", map[string]any{"items": total})
	}
	s.publish("catalog.updated", map[string]any{"mode": mode, "items": total, "job": job})
}

func (s *Service) RetryJob(ctx context.Context, id string) (domain.Job, error) {
	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if job.State == "queued" || job.State == "running" || job.State == "retry_wait" {
		return domain.Job{}, fmt.Errorf("active jobs cannot be retried")
	}
	switch job.Kind {
	case "catalog-latest":
		return s.SyncCatalog("latest")
	case "catalog-rebuild":
		return s.SyncCatalog("rebuild")
	case "metadata":
		titleID := strings.TrimPrefix(job.DedupeKey, "metadata:")
		if s.ensureMetadata(ctx, []string{titleID}, true) == 0 {
			return domain.Job{}, fmt.Errorf("metadata title is unavailable, lacks an IMDb id, or is already queued")
		}
		return s.repo.GetJob(ctx, id)
	case "catalog-title-refresh":
		titleID := strings.TrimPrefix(job.DedupeKey, "catalog-title-refresh:")
		sources, sourceErr := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{titleID})
		if sourceErr != nil || len(sources) == 0 {
			return domain.Job{}, fmt.Errorf("catalog title is unavailable")
		}
		return s.QueueTitleRefresh(ctx, titleID, groupCatalog(sources, false)[0].Title, true)
	case "tracker-search":
		query := strings.TrimPrefix(job.Label, "Search FileList for ")
		return s.QueueTrackerSearch(ctx, query, true)
	case retentionKind:
		return s.RunRetention()
	}
	return domain.Job{}, fmt.Errorf("job kind is not retryable")
}

func (s *Service) failOrWait(job *domain.Job, err error, phase string) {
	job.Error = err.Error()
	job.UpdatedAt = time.Now().UTC()
	job.Retryable = isTransient(err)
	var rate *outbound.RateLimitError
	if errors.As(err, &rate) {
		at := rate.RetryAt
		if at.IsZero() {
			at = time.Now().Add(time.Hour)
		}
		job.State = "retry_wait"
		job.NextAttemptAt = &at
		s.jobLog(*job, "warn", "rate-limit", "Provider rate limit reached", map[string]any{"retryAt": at, "phase": phase, "detail": rate.Detail})
		return
	}
	job.State = "failed"
	if job.Retryable {
		at := time.Now().Add(time.Hour)
		job.NextAttemptAt = &at
	}
	s.jobLog(*job, "error", phase, "Job attempt failed", map[string]any{"error": err.Error(), "retryable": job.Retryable})
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if outbound.IsRateLimited(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "timeout") || strings.Contains(text, "temporar") || strings.Contains(text, "connection reset") || strings.Contains(text, "http 5") || strings.Contains(text, "returned 5")
}

func (s *Service) StartScheduler() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.recoverInterruptedJobs()
		s.retryDueJobs(true)
		s.retryDueJobs(false)
		_ = s.repo.PruneJobLogs(context.Background(), time.Now().Add(-30*24*time.Hour), 500)
		_, _ = s.SyncCatalog("latest")
		_, _ = s.RunRetention()
		latest := time.NewTicker(time.Hour)
		rebuild := time.NewTicker(7 * 24 * time.Hour)
		due := time.NewTicker(time.Minute)
		defer latest.Stop()
		defer rebuild.Stop()
		defer due.Stop()
		for {
			select {
			case <-latest.C:
				s.retryDueJobs(false)
				_, _ = s.SyncCatalog("latest")
				_, _ = s.RunRetention()
			case <-rebuild.C:
				_ = s.repo.PruneJobLogs(context.Background(), time.Now().Add(-30*24*time.Hour), 500)
				_, _ = s.SyncCatalog("rebuild")
			case <-due.C:
				s.retryDueJobs(true)
			case <-s.stopping:
				return
			}
		}
	}()
}

// Close stops the title-refresh, tracker-search, and metadata workers, the
// scheduler, and every in-flight catalog/job goroutine, then closes the
// engine and finally the repository — in that order. It is idempotent: the
// first call performs the shutdown and later calls return its result. When
// the workers do not join before the context deadline (or a bounded default),
// Close aborts the handoff with an error and leaves the engine and database
// open rather than closing them under active writers. The stopping flag also
// keeps journal writes (publish, jobLog) from ever reaching a closed
// repository.
func (s *Service) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.cancelBase()
		close(s.stopping)
		s.closeErr = s.joinAndClose(ctx)
	})
	return s.closeErr
}

func (s *Service) joinAndClose(ctx context.Context) error {
	joinCtx := ctx
	cancel := func() {}
	if _, deadline := ctx.Deadline(); !deadline {
		joinCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	}
	defer cancel()
	joined := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-joinCtx.Done():
		return fmt.Errorf("service shutdown aborted: workers did not stop: %w", joinCtx.Err())
	}
	var firstErr error
	if closer, ok := s.engine.(io.Closer); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.repo.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *Service) retryDueJobs(waitingOnly bool) {
	jobs, err := s.repo.ListDueJobs(context.Background(), time.Now(), 500)
	if err != nil {
		return
	}
	now := time.Now()
	for _, job := range jobs {
		due := job.NextAttemptAt != nil && !job.NextAttemptAt.After(now)
		if !due {
			continue
		}
		if waitingOnly && job.State != "retry_wait" {
			continue
		}
		if !waitingOnly && (job.State != "failed" || !job.Retryable) {
			continue
		}
		job.State = "failed"
		job.NextAttemptAt = nil
		_ = s.repo.SaveJob(context.Background(), job)
		s.jobLog(job, "info", "automatic-retry", "Automatic retry queued", nil)
		if _, retryErr := s.RetryJob(context.Background(), job.ID); retryErr != nil {
			s.jobLog(job, "error", "automatic-retry", "Automatic retry could not be queued", map[string]any{"error": retryErr.Error()})
		}
	}
}

func (s *Service) recoverInterruptedJobs() {
	ctx := context.Background()
	jobs, err := s.repo.ListJobs(ctx, 500)
	if err != nil {
		return
	}
	for _, job := range jobs {
		if job.State != "queued" && job.State != "running" {
			continue
		}
		if job.Kind == "catalog-title-refresh" {
			query := strings.TrimPrefix(job.Label, "Refresh all versions of ")
			if query == job.Label || len([]rune(strings.TrimSpace(query))) < 3 {
				continue
			}
			if sources, sourceErr := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{strings.TrimPrefix(job.DedupeKey, "catalog-title-refresh:")}); sourceErr == nil && len(sources) > 0 {
				blacklisted := defaultBlacklistedCategories()
				allowed := false
				for _, source := range sources {
					allowed = allowed || !blacklisted[source.Release.Category]
				}
				if !allowed {
					job.State, job.Error, job.UpdatedAt = "failed", "category is excluded from discovery", time.Now().UTC()
					_ = s.repo.SaveJob(ctx, job)
					continue
				}
			}
			job.State, job.Progress, job.Error, job.UpdatedAt = "queued", 0, "", time.Now().UTC()
			_ = s.repo.SaveJob(ctx, job)
			select {
			case s.refreshQueue <- titleRefreshRequest{TitleID: strings.TrimPrefix(job.DedupeKey, "catalog-title-refresh:"), Query: query}:
			default:
			}
			continue
		}
		job.State = "failed"
		job.Error = "interrupted by server restart; safe to retry"
		job.Retryable = true
		next := time.Now().UTC().Add(time.Hour)
		job.NextAttemptAt = &next
		job.UpdatedAt = time.Now().UTC()
		_ = s.repo.SaveJob(ctx, job)
	}
}

func (s *Service) Jobs(ctx context.Context, limit int) ([]domain.Job, error) {
	return s.repo.ListJobs(ctx, limit)
}

func (s *Service) CatalogStatus(ctx context.Context) (map[string]any, error) {
	total, discoverable, err := s.repo.CatalogCounts(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": "append-only observed cache", "observedReleases": total, "discoverableReleases": discoverable, "hiddenZeroSeeders": total - discoverable, "fileListLatestWindowLimit": 100, "historicalPagination": false}, nil
}

func (s *Service) QueryJobs(ctx context.Context, search, state, kind, retryable string, updatedSince int64, limit, offset int) (domain.Page[domain.Job], error) {
	return s.repo.QueryJobs(ctx, search, state, kind, retryable, updatedSince, limit, offset)
}

func (s *Service) Job(ctx context.Context, id string) (domain.Job, error) {
	return s.repo.GetJob(ctx, id)
}

func (s *Service) JobLogs(ctx context.Context, id string, before int64, limit int) (domain.Page[domain.JobLog], error) {
	if _, err := s.repo.GetJob(ctx, id); err != nil {
		return domain.Page[domain.JobLog]{}, err
	}
	return s.repo.ListJobLogs(ctx, id, before, limit)
}

func (s *Service) Browse(ctx context.Context, search, category string, limit, offset int) (domain.Page[domain.TorrentRelease], error) {
	key := "latest"
	age, err := s.repo.SyncAge(ctx, key)
	if err != nil {
		return domain.Page[domain.TorrentRelease]{}, err
	}
	stale := age < 0 || time.Duration(age)*time.Second > s.settings.CatalogMaxAge()
	page, err := s.repo.ListReleases(ctx, search, category, limit, offset)
	page.Stale = stale
	return page, err
}

func (s *Service) Search(ctx context.Context, q string) (domain.Page[domain.TorrentRelease], error) {
	if len([]rune(strings.TrimSpace(q))) < 3 {
		return domain.Page[domain.TorrentRelease]{Items: []domain.TorrentRelease{}}, nil
	}
	key := "search:" + strings.ToLower(strings.TrimSpace(q))
	items, err := s.catalog.Search(ctx, q)
	_ = s.repo.RecordSync(ctx, key, len(items), err)
	if err != nil {
		return domain.Page[domain.TorrentRelease]{}, err
	}
	if err = s.repo.UpsertReleases(ctx, items); err != nil {
		return domain.Page[domain.TorrentRelease]{}, err
	}
	seen := map[string]bool{}
	blacklisted := defaultBlacklistedCategories()
	for _, release := range items {
		if blacklisted[release.Category] {
			continue
		}
		parsed := domain.ParseRelease(release)
		id := domain.CatalogTitleID(release, parsed)
		if id == "" || seen[id] || len([]rune(strings.TrimSpace(parsed.Title))) < 3 {
			continue
		}
		seen[id] = true
		_, _ = s.QueueTitleRefresh(context.Background(), id, parsed.Title, false)
	}
	s.publish("catalog.search.completed", map[string]any{"query": q, "items": len(items), "titleCount": len(seen)})
	return domain.Page[domain.TorrentRelease]{Items: items, Total: len(items)}, nil
}

func defaultBlacklistedCategories() map[string]bool {
	blacklisted := map[string]bool{}
	for _, category := range domain.Categories {
		if category.DefaultBlacklisted {
			blacklisted[category.Name] = true
		}
	}
	return blacklisted
}

func (s *Service) SearchTitles(ctx context.Context, q string) (domain.Page[domain.CatalogTitle], error) {
	if len([]rune(strings.TrimSpace(q))) < 3 {
		return domain.Page[domain.CatalogTitle]{}, fmt.Errorf("search query must contain at least three characters")
	}
	return s.CatalogTitles(ctx, domain.CatalogQuery{Search: q, Sort: "seeders", Limit: 100})
}

func (s *Service) QueueTrackerSearch(ctx context.Context, q string, force bool) (domain.Job, error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) < 3 {
		return domain.Job{}, fmt.Errorf("search query must contain at least three characters")
	}
	sum := sha256.Sum256([]byte(strings.ToLower(q)))
	key := "tracker-search:" + base64.RawURLEncoding.EncodeToString(sum[:12])
	if existing, err := s.repo.GetJob(ctx, key); err == nil && (existing.State == "queued" || existing.State == "running" || existing.State == "retry_wait") {
		return existing, nil
	}
	attempt := 1
	if existing, err := s.repo.GetJob(ctx, key); err == nil {
		attempt = existing.Attempt
		if force || existing.State == "completed" || existing.State == "failed" {
			attempt++
		}
	}
	job := domain.Job{ID: key, Kind: "tracker-search", State: "queued", Label: "Search FileList for " + q, DedupeKey: key, Attempt: attempt, UpdatedAt: time.Now().UTC()}
	if err := s.repo.SaveJob(ctx, job); err != nil {
		return domain.Job{}, err
	}
	select {
	case s.searchQueue <- trackerSearchRequest{Query: q}:
		s.jobLog(job, "info", "queue", "Tracker search queued", map[string]any{"query": q})
		s.publish("job.updated", job)
		return job, nil
	default:
		job.State = "failed"
		job.Error = "tracker search queue is full"
		job.UpdatedAt = time.Now().UTC()
		_ = s.repo.SaveJob(ctx, job)
		s.jobLog(job, "error", "queue", job.Error, nil)
		return job, errors.New(job.Error)
	}
}

func (s *Service) trackerSearchWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopping:
			return
		case request, ok := <-s.searchQueue:
			if !ok {
				return
			}
			s.runTrackerSearch(request)
		}
	}
}

func (s *Service) runTrackerSearch(request trackerSearchRequest) {
	sum := sha256.Sum256([]byte(strings.ToLower(request.Query)))
	key := "tracker-search:" + base64.RawURLEncoding.EncodeToString(sum[:12])
	if err := s.acquire(s.baseCtx, true); err != nil {
		return
	}
	job, _ := s.repo.GetJob(context.Background(), key)
	job.State = "running"
	job.Progress = .1
	job.Error = ""
	job.NextAttemptAt = nil
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "tracker-search", "Submitted FileList search started", map[string]any{"query": request.Query})
	ctx, cancel := context.WithTimeout(s.baseCtx, time.Duration(s.settings.Get().TitleRefreshTimeoutMinutes)*time.Minute)
	page, err := s.Search(ctx, request.Query)
	job.UpdatedAt = time.Now().UTC()
	if err != nil {
		s.failOrWait(&job, err, "tracker-search")
	} else {
		job.State = "completed"
		job.Progress = 1
		job.Retryable = false
		job.NextAttemptAt = nil
		s.jobLog(job, "info", "complete", "Tracker search completed", map[string]any{"releases": len(page.Items)})
	}
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
	cancel()
	s.release(true)
}

func (s *Service) QueueTitleRefresh(ctx context.Context, titleID, query string, force bool) (domain.Job, error) {
	query = strings.TrimSpace(query)
	if titleID == "" || len([]rune(query)) < 3 {
		return domain.Job{}, fmt.Errorf("title and refresh query are required")
	}
	key := "catalog-title-refresh:" + titleID
	if !force {
		if age, err := s.repo.SyncAge(ctx, key); err == nil && age >= 0 && age < int64(time.Hour/time.Second) {
			return domain.Job{ID: key, Kind: "catalog-title-refresh", State: "completed", Label: "Title was refreshed less than one hour ago", DedupeKey: key, Progress: 1, UpdatedAt: time.Now().UTC()}, nil
		}
	}
	if existing, err := s.repo.GetJob(ctx, key); err == nil {
		if existing.State == "queued" || existing.State == "running" || (!force && existing.State == "failed" && time.Since(existing.UpdatedAt) < 5*time.Minute) {
			return existing, nil
		}
	}
	attempt := 0
	if existing, err := s.repo.GetJob(ctx, key); err == nil {
		attempt = existing.Attempt
	}
	if force {
		attempt++
	}
	if attempt < 1 {
		attempt = 1
	}
	job := domain.Job{ID: key, Kind: "catalog-title-refresh", State: "queued", Label: "Refresh all versions of " + query, DedupeKey: key, Attempt: attempt, UpdatedAt: time.Now().UTC()}
	if err := s.repo.SaveJob(ctx, job); err != nil {
		return domain.Job{}, err
	}
	select {
	case s.refreshQueue <- titleRefreshRequest{TitleID: titleID, Query: query}:
		s.jobLog(job, "info", "queue", "Title refresh queued", map[string]any{"forced": force})
		s.publish("job.updated", job)
		return job, nil
	default:
		job.State = "failed"
		job.Error = "title refresh queue is full"
		job.UpdatedAt = time.Now().UTC()
		_ = s.repo.SaveJob(ctx, job)
		return job, errors.New(job.Error)
	}
}

func (s *Service) titleRefreshWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopping:
			return
		case request, ok := <-s.refreshQueue:
			if !ok {
				return
			}
			s.runTitleRefresh(request)
		}
	}
}

func (s *Service) runTitleRefresh(request titleRefreshRequest) {
	key := "catalog-title-refresh:" + request.TitleID
	if err := s.acquire(s.baseCtx, true); err != nil {
		return
	}
	job, _ := s.repo.GetJob(context.Background(), key)
	job.State = "running"
	job.Progress = .05
	job.Error = ""
	job.NextAttemptAt = nil
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "tracker-search", "Searching FileList for title versions", map[string]any{"query": request.Query})
	timeout := time.Duration(s.settings.Get().TitleRefreshTimeoutMinutes) * time.Minute
	ctx, cancel := context.WithTimeout(s.baseCtx, timeout)
	items, err := s.catalog.Search(ctx, request.Query)
	if err == nil {
		s.jobLog(job, "info", "tracker-search", "FileList title search completed", map[string]any{"releases": len(items)})
		job.Progress = .2
		job.UpdatedAt = time.Now().UTC()
		_ = s.repo.SaveJob(context.Background(), job)
		s.publish("job.updated", job)
	}
	if err == nil {
		err = s.repo.UpsertReleases(ctx, items)
	}
	if err == nil {
		for index, release := range items {
			parsed := domain.ParseRelease(release)
			if release.FileCount > 1 && (domain.CatalogTitleID(release, parsed) == request.TitleID) {
				if _, manifestErr := s.torrentManifest(ctx, release); manifestErr != nil {
					s.jobLog(job, "warn", "torrent-manifest", "Could not inspect a multi-file torrent", map[string]any{"releaseId": release.ID, "release": release.Name, "error": manifestErr.Error()})
					if errors.Is(manifestErr, context.DeadlineExceeded) || errors.Is(manifestErr, context.Canceled) {
						err = manifestErr
						break
					}
				}
			}
			job.Progress = .2 + .7*float64(index+1)/float64(max(1, len(items)))
			job.UpdatedAt = time.Now().UTC()
			_ = s.repo.SaveJob(context.Background(), job)
		}
	}
	_ = s.repo.RecordSync(context.Background(), key, len(items), err)
	job.UpdatedAt = time.Now().UTC()
	if err != nil {
		s.failOrWait(&job, err, "title-refresh")
	} else {
		job.State = "completed"
		job.Retryable = false
		job.NextAttemptAt = nil
		job.Progress = 1
		job.Label = fmt.Sprintf("Refreshed %s · %d releases", request.Query, len(items))
		_ = s.EnsureMetadata(context.Background(), []string{request.TitleID})
	}
	_ = s.repo.SaveJob(context.Background(), job)
	if err == nil {
		s.jobLog(job, "info", "complete", "Title refresh completed", map[string]any{"releases": len(items)})
	}
	s.publish("job.updated", job)
	s.publish("catalog.updated", map[string]any{"mode": "title", "titleId": request.TitleID, "items": len(items), "job": job})
	cancel()
	s.release(true)
}

func (s *Service) TestFileList(ctx context.Context) (int, error) {
	items, err := s.catalog.Latest(ctx)
	return len(items), err
}
func (s *Service) TestEngine(ctx context.Context) (string, error) { return s.engine.Test(ctx) }

// ensureAllocationRoom is the starvation path of ADR-0004: before a new
// torrent is added, the Allocation must be able to hold it. Stored bytes come
// from the same survey the retention job runs, and the fit check is
// retentionDeficit itself — the incoming torrent is added to the surveyed
// total, so the cap math exists in exactly one place. When the Allocation
// would overflow, unprotected torrents are evicted one at a time (same hook
// the retention job uses) and the survey re-run; only when nothing evictable
// remains does the download fail with a visible Allocation problem. A zero
// Allocation disables the whole check.
func (s *Service) ensureAllocationRoom(ctx context.Context, release domain.TorrentRelease) error {
	settings := s.settings.Get()
	if settings.AllocationGB <= 0 {
		return nil
	}
	incoming := s.incomingTorrentBytes(ctx, release)
	plan, err := s.retentionSurvey(ctx)
	if err != nil {
		return err
	}
	plan.storedBytes += incoming
	reason, tripped := retentionDeficit(plan, settings)
	rules := config.NormalizeEvictionRules(settings.EvictionRules)
	for tripped {
		_, evicted, evictErr := s.evictNext(ctx, plan, reason, settings, rules)
		if evictErr != nil {
			return evictErr
		}
		if !evicted {
			if reason == "cap" {
				return &domain.AllocationError{
					Release:       release.Name,
					RequiredBytes: incoming,
					FreeBytes:     gigabytesToBytes(settings.AllocationGB) - (plan.storedBytes - incoming),
					CapacityBytes: gigabytesToBytes(settings.AllocationGB),
				}
			}
			// A reserve breach nothing evictable can fix is not the incoming
			// download's fault: proceed and let the hourly retention job keep
			// enforcing the reserve.
			break
		}
		if plan, err = s.retentionSurvey(ctx); err != nil {
			return err
		}
		plan.storedBytes += incoming
		reason, tripped = retentionDeficit(plan, settings)
	}
	return nil
}

// incomingTorrentBytes reports how much storage the Release's torrent will
// claim: the parsed torrent manifest when it is (or can be) cached, falling
// back to the tracker-reported Release size.
func (s *Service) incomingTorrentBytes(ctx context.Context, release domain.TorrentRelease) int64 {
	if manifest, err := s.torrentManifest(ctx, release); err == nil {
		var total int64
		for _, file := range manifest.Files {
			total += file.SizeBytes
		}
		if total > 0 {
			return total
		}
	}
	return release.SizeBytes
}

func (s *Service) Prepare(ctx context.Context, releaseID string, fileIndex int) (domain.Download, error) {
	key := releaseID + ":" + fmt.Sprint(fileIndex)
	lockAny, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.Download{}, err
	}
	if existing, existingErr := s.repo.FindDownload(ctx, releaseID, fileIndex); existingErr == nil {
		s.enrichDownload(ctx, &existing, release)
		return s.prepareManagedDownload(ctx, existing)
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return domain.Download{}, existingErr
	}
	if existing, reuseErr := s.prepareExistingTorrentFile(ctx, release, fileIndex); reuseErr == nil {
		return existing, nil
	} else if !errors.Is(reuseErr, sql.ErrNoRows) {
		return domain.Download{}, reuseErr
	}
	if err := s.ensureAllocationRoom(ctx, release); err != nil {
		return domain.Download{}, err
	}
	torrent, err := s.catalog.OpenTorrent(ctx, release.ID)
	if err != nil {
		if errors.Is(err, domain.ErrTorrentRemoved) {
			return domain.Download{}, s.removeDeadRelease(ctx, release)
		}
		return domain.Download{}, err
	}
	defer torrent.Close()
	settings := s.settings.Get()
	hash, err := s.engine.Add(ctx, torrent, settings.DownloadRoot)
	if err != nil {
		return domain.Download{}, err
	}
	var files []domain.TorrentFile
	deadline := time.Now().Add(30 * time.Second)
	for {
		files, err = s.engine.Files(ctx, hash)
		if err == nil && len(files) > 0 {
			break
		}
		if time.Now().After(deadline) {
			return domain.Download{}, fmt.Errorf("qBittorrent metadata unavailable: %w", err)
		}
		select {
		case <-ctx.Done():
			return domain.Download{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	var selected *domain.TorrentFile
	subs := []int{}
	for i := range files {
		if files[i].Index == fileIndex {
			selected = &files[i]
		}
		if fileIndex < 0 && files[i].Playable && (selected == nil || files[i].SizeBytes > selected.SizeBytes) {
			selected = &files[i]
		}
		if subtitle(files[i].Path) {
			subs = append(subs, files[i].Index)
		}
	}
	if selected == nil || !selected.Playable {
		return domain.Download{}, fmt.Errorf("selected file is not playable")
	}
	fileIndex = selected.Index
	indices := s.managedTorrentIndices(ctx, s.enginePrefix()+hash, files, fileIndex)
	if err = s.engine.PrepareFiles(ctx, hash, indices, subs); err != nil {
		return domain.Download{}, err
	}
	status, err := s.engine.Status(ctx, hash)
	if err != nil {
		return domain.Download{}, err
	}
	abs, err := safeQBPath(settings.DownloadRoot, status.SavePath, selected.Path)
	if err != nil {
		return domain.Download{}, err
	}
	id := sourceID(releaseID, selected.Path)
	now := time.Now().UTC()
	d := domain.Download{ID: id, ReleaseID: releaseID, EngineID: s.enginePrefix() + hash, FileIndex: fileIndex, FilePath: selected.Path, AbsolutePath: abs, SizeBytes: selected.SizeBytes, FileOffset: selected.Offset, CreatedAt: now, UpdatedAt: now}
	applyDownloadStatus(&d, status, selected)
	if old, e := s.repo.GetDownload(ctx, id); e == nil {
		d.CreatedAt = old.CreatedAt
	}
	if err = s.repo.SaveDownload(ctx, d); err != nil {
		return domain.Download{}, err
	}
	s.enrichDownload(ctx, &d, release)
	return d, nil
}

func (s *Service) prepareExistingTorrentFile(ctx context.Context, release domain.TorrentRelease, fileIndex int) (domain.Download, error) {
	downloads, err := s.repo.ListDownloads(ctx)
	if err != nil {
		return domain.Download{}, err
	}
	for _, managed := range downloads {
		if managed.ReleaseID != release.ID {
			continue
		}
		hash, ok := s.route(managed.EngineID)
		if !ok {
			continue
		}
		files, filesErr := s.engine.Files(ctx, hash)
		if filesErr != nil {
			return domain.Download{}, filesErr
		}
		var selected *domain.TorrentFile
		for i := range files {
			if files[i].Index == fileIndex {
				selected = &files[i]
			}
			if fileIndex < 0 && files[i].Playable && (selected == nil || files[i].SizeBytes > selected.SizeBytes) {
				selected = &files[i]
			}
		}
		if selected == nil || !selected.Playable {
			return domain.Download{}, fmt.Errorf("selected file is not playable")
		}
		status, statusErr := s.engine.Status(ctx, hash)
		if statusErr != nil {
			return domain.Download{}, statusErr
		}
		abs, pathErr := safeQBPath(s.settings.Get().DownloadRoot, status.SavePath, selected.Path)
		if pathErr != nil {
			return domain.Download{}, pathErr
		}
		now := time.Now().UTC()
		download := domain.Download{ID: sourceID(release.ID, selected.Path), ReleaseID: release.ID, EngineID: managed.EngineID, FileIndex: selected.Index, FilePath: selected.Path, AbsolutePath: abs, SizeBytes: selected.SizeBytes, FileOffset: selected.Offset, CreatedAt: now, UpdatedAt: now}
		applyDownloadStatus(&download, status, selected)
		if old, oldErr := s.repo.GetDownload(ctx, download.ID); oldErr == nil {
			download.CreatedAt = old.CreatedAt
		}
		if saveErr := s.repo.SaveDownload(ctx, download); saveErr != nil {
			return domain.Download{}, saveErr
		}
		s.enrichDownload(ctx, &download, release)
		return s.prepareManagedDownload(ctx, download)
	}
	return domain.Download{}, sql.ErrNoRows
}

func (s *Service) NextEpisode(ctx context.Context, sourceID string) (*domain.Download, error) {
	current, err := s.repo.GetDownload(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	release, err := s.repo.GetRelease(ctx, current.ReleaseID)
	if err != nil {
		return nil, err
	}
	parsed := domain.ParseRelease(release)
	if parsed.Kind != domain.MediaSeries {
		return nil, nil
	}
	detail, err := s.CatalogDetail(ctx, domain.CatalogTitleID(release, parsed))
	if err != nil {
		return nil, err
	}
	type episodeKey struct{ season, episode int }
	currentKey := episodeKey{parsed.SeasonStart, parsed.EpisodeStart}
	for _, season := range detail.Seasons {
		for _, episode := range season.Episodes {
			for _, source := range episode.Sources {
				if source.Release.ID == current.ReleaseID && ((source.FileIndex != nil && *source.FileIndex == current.FileIndex) || (source.FileIndex == nil && release.FileCount <= 1)) {
					currentKey = episodeKey{season.Number, episode.Number}
				}
			}
		}
	}
	if currentKey.episode == 0 {
		return nil, nil
	}
	var candidates []domain.CatalogSource
	nextKey := episodeKey{}
	for _, season := range detail.Seasons {
		for _, episode := range season.Episodes {
			key := episodeKey{season.Number, episode.Number}
			if key.season < currentKey.season || (key.season == currentKey.season && key.episode <= currentKey.episode) {
				continue
			}
			if nextKey.episode == 0 || key.season < nextKey.season || (key.season == nextKey.season && key.episode < nextKey.episode) {
				nextKey, candidates = key, append([]domain.CatalogSource(nil), episode.Sources...)
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		iSame, jSame := candidates[i].Release.ID == current.ReleaseID, candidates[j].Release.ID == current.ReleaseID
		if iSame != jSame {
			return iSame
		}
		return candidates[i].Release.Seeders > candidates[j].Release.Seeders
	})
	fileIndex := -1
	if candidates[0].FileIndex != nil {
		fileIndex = *candidates[0].FileIndex
	}
	next, err := s.Prepare(ctx, candidates[0].Release.ID, fileIndex)
	if err != nil {
		return nil, err
	}
	return &next, nil
}

func (s *Service) PrepareSeason(ctx context.Context, releaseID string, season int) ([]domain.Download, error) {
	key := releaseID + ":season:" + fmt.Sprint(season)
	lockAny, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	parsed := domain.ParseRelease(release)
	if parsed.Kind != domain.MediaSeries || release.FileCount <= 1 {
		return nil, fmt.Errorf("release is not a multi-file series pack")
	}
	if season <= 0 {
		season = parsed.SeasonStart
	}
	if season <= 0 {
		season = 1
	}

	engineID, hash := "", ""
	if managed, listErr := s.repo.ListDownloads(ctx); listErr == nil {
		for _, download := range managed {
			if download.ReleaseID == releaseID {
				if existingHash, ok := s.route(download.EngineID); ok {
					engineID, hash = download.EngineID, existingHash
					break
				}
			}
		}
	}
	settings := s.settings.Get()
	if hash == "" {
		if fitErr := s.ensureAllocationRoom(ctx, release); fitErr != nil {
			return nil, fitErr
		}
		torrent, openErr := s.catalog.OpenTorrent(ctx, release.ID)
		if openErr != nil {
			if errors.Is(openErr, domain.ErrTorrentRemoved) {
				return nil, s.removeDeadRelease(ctx, release)
			}
			return nil, openErr
		}
		defer torrent.Close()
		hash, err = s.engine.Add(ctx, torrent, settings.DownloadRoot)
		if err != nil {
			return nil, err
		}
		engineID = s.enginePrefix() + hash
	}

	var files []domain.TorrentFile
	deadline := time.Now().Add(30 * time.Second)
	for {
		files, err = s.engine.Files(ctx, hash)
		if err == nil && len(files) > 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("qBittorrent metadata unavailable: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	base := domain.CatalogSource{Release: release, Parsed: parsed}
	selected := make([]domain.TorrentFile, 0)
	subs := make([]int, 0)
	for _, file := range files {
		if subtitle(file.Path) {
			subs = append(subs, file.Index)
		}
		if !file.Playable {
			continue
		}
		virtual, ok := episodeSource(base, file)
		if ok && virtual.Parsed.SeasonStart == season {
			selected = append(selected, file)
		}
	}
	if len(selected) == 0 && (parsed.SeasonStart == 0 || (parsed.SeasonStart <= season && season <= max(parsed.SeasonStart, parsed.SeasonEnd))) {
		for _, file := range files {
			if file.Playable {
				selected = append(selected, file)
			}
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("season %d has no playable episode files", season)
	}
	extra := make([]int, 0, len(selected))
	for _, file := range selected {
		extra = append(extra, file.Index)
	}
	indices := s.managedTorrentIndices(ctx, engineID, files, extra...)
	if err = s.engine.PrepareFiles(ctx, hash, indices, subs); err != nil {
		return nil, err
	}
	status, err := s.engine.Status(ctx, hash)
	if err != nil {
		return nil, err
	}
	manifest := domain.TorrentManifest{ReleaseID: releaseID, Files: files, FetchedAt: time.Now().UTC()}
	_ = s.repo.SaveTorrentManifest(ctx, manifest)
	now := time.Now().UTC()
	out := make([]domain.Download, 0, len(selected))
	for i := range selected {
		file := &selected[i]
		abs, pathErr := safeQBPath(settings.DownloadRoot, status.SavePath, file.Path)
		if pathErr != nil {
			return nil, pathErr
		}
		download := domain.Download{ID: sourceID(releaseID, file.Path), ReleaseID: releaseID, EngineID: engineID, FileIndex: file.Index, FilePath: file.Path, AbsolutePath: abs, SizeBytes: file.SizeBytes, FileOffset: file.Offset, CreatedAt: now, UpdatedAt: now}
		applyDownloadStatus(&download, status, file)
		if old, oldErr := s.repo.GetDownload(ctx, download.ID); oldErr == nil {
			download.CreatedAt = old.CreatedAt
		}
		if saveErr := s.repo.SaveDownload(ctx, download); saveErr != nil {
			return nil, saveErr
		}
		s.enrichDownload(ctx, &download, release)
		out = append(out, download)
	}
	return out, nil
}

func (s *Service) managedTorrentIndices(ctx context.Context, engineID string, files []domain.TorrentFile, extra ...int) []int {
	available := map[int]bool{}
	for _, file := range files {
		if file.Playable {
			available[file.Index] = true
		}
	}
	wanted := map[int]bool{}
	for _, index := range extra {
		if available[index] {
			wanted[index] = true
		}
	}
	if downloads, err := s.repo.ListDownloads(ctx); err == nil {
		for _, download := range downloads {
			if download.EngineID == engineID && available[download.FileIndex] {
				wanted[download.FileIndex] = true
			}
		}
	}
	indices := make([]int, 0, len(wanted))
	for index := range wanted {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func (s *Service) prepareManagedDownload(ctx context.Context, d domain.Download) (domain.Download, error) {
	if completedLocalFile(d) {
		return d, nil
	}
	hash, ok := s.route(d.EngineID)
	if !ok {
		return d, fmt.Errorf("unsupported engine route")
	}
	lockAny, _ := s.locks.LoadOrStore("stream:"+hash, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	status, err := s.engine.Status(ctx, hash)
	if err != nil {
		// A completed local file remains playable even when qBittorrent is down.
		if completedLocalFile(d) {
			return d, nil
		}
		return d, err
	}
	if domain.IsPaused(status.State) {
		if err = s.engine.Resume(ctx, hash); err != nil {
			return d, fmt.Errorf("resume torrent for playback: %w", err)
		}
	}
	files, err := s.engine.Files(ctx, hash)
	if err != nil {
		return d, err
	}
	subs := make([]int, 0)
	var selected *domain.TorrentFile
	for _, file := range files {
		if file.Index == d.FileIndex {
			copy := file
			selected = &copy
		}
		if subtitle(file.Path) {
			subs = append(subs, file.Index)
		}
	}
	if selected == nil {
		return d, fmt.Errorf("selected torrent file is no longer available")
	}
	indices := s.managedTorrentIndices(ctx, d.EngineID, files, d.FileIndex)
	if err = s.engine.PrepareFiles(ctx, hash, indices, subs); err != nil {
		return d, err
	}
	status, err = s.engine.Status(ctx, hash)
	if err != nil {
		return d, err
	}
	if !status.Sequential || !status.FirstLastPriority {
		return d, fmt.Errorf("qBittorrent did not enable progressive streaming priorities")
	}
	applyDownloadStatus(&d, status, selected)
	d.UpdatedAt = time.Now().UTC()
	if err = s.repo.SaveDownload(ctx, d); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Service) Downloads(ctx context.Context) ([]domain.Download, error) {
	items, err := s.repo.ListDownloads(ctx)
	if err != nil {
		return nil, err
	}
	items = dedupeManagedDownloads(items)
	for i := range items {
		if release, releaseErr := s.repo.GetRelease(ctx, items[i].ReleaseID); releaseErr == nil {
			s.enrichDownload(ctx, &items[i], release)
		}
		hash, ok := s.route(items[i].EngineID)
		var st domain.DownloadStatus
		statusErr := error(nil)
		if ok {
			st, statusErr = s.engine.Status(ctx, hash)
		} else {
			// The active engine cannot describe a row issued under a foreign
			// Engine prefix; it surfaces as unavailable rather than serving
			// stale persisted state.
			statusErr = fmt.Errorf("unsupported engine route")
		}
		if statusErr != nil {
			items[i].Error = statusErr.Error()
			items[i].State = "unavailable"
		} else {
			var selected *domain.TorrentFile
			if files, filesErr := s.engine.Files(ctx, hash); filesErr == nil {
				for _, file := range files {
					if file.Index == items[i].FileIndex {
						copy := file
						selected = &copy
						break
					}
				}
			}
			applyDownloadStatus(&items[i], st, selected)
		}
		// Telemetry refreshes must not make an existing row look newly added.
		// Keep UpdatedAt stable so clients can patch progress in place without
		// the server changing the collection order on every poll.
		_ = s.repo.SaveDownload(ctx, items[i])
	}
	return items, nil
}

func dedupeManagedDownloads(items []domain.Download) []domain.Download {
	out := make([]domain.Download, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		key := item.ID
		if item.EngineID != "" {
			// A qBittorrent file is uniquely identified by its torrent plus file
			// index. Keep separate season episodes, but collapse legacy rows that
			// point at the exact same underlying file. ListDownloads is newest-first,
			// so the current row wins.
			key = strings.ToLower(item.EngineID) + ":" + fmt.Sprint(item.FileIndex)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func applyDownloadStatus(download *domain.Download, status domain.DownloadStatus, selected *domain.TorrentFile) {
	download.State = status.State
	download.PieceSize = status.PieceSize
	download.SpeedBytesPerSecond = status.SpeedBytesPerSecond
	download.UploadSpeedBytesPerSecond = status.UploadSpeedBytesPerSecond
	download.ETASeconds = status.ETASeconds
	download.Peers = status.Peers
	download.Seeds = status.Seeds
	download.Error = trackerError(status)
	progress := status.Progress
	if selected != nil {
		progress = selected.Progress
		if selected.SizeBytes > 0 {
			download.SizeBytes = selected.SizeBytes
		}
		download.FileOffset = selected.Offset
	}
	progress = max(0, min(1, progress))
	download.Progress = progress
	if download.SizeBytes > 0 {
		download.DownloadedBytes = int64(float64(download.SizeBytes) * progress)
	} else {
		download.DownloadedBytes = status.DownloadedBytes
	}
}

func (s *Service) enrichDownload(ctx context.Context, download *domain.Download, release domain.TorrentRelease) {
	baseParsed := domain.ParseRelease(release)
	download.Parsed = baseParsed
	download.TitleID = domain.CatalogTitleID(release, baseParsed)
	download.DisplayTitle = baseParsed.Title
	if baseParsed.Kind == domain.MediaSeries && download.FilePath != "" {
		base := domain.CatalogSource{Release: release, Parsed: baseParsed}
		file := domain.TorrentFile{Index: download.FileIndex, Path: download.FilePath, SizeBytes: download.SizeBytes, Playable: true}
		if episode, ok := episodeSource(base, file); ok {
			download.Parsed = episode.Parsed
			label := fmt.Sprintf("S%02dE%02d", episode.Parsed.SeasonStart, episode.Parsed.EpisodeStart)
			if episode.Parsed.EpisodeEnd > episode.Parsed.EpisodeStart {
				label += fmt.Sprintf("-E%02d", episode.Parsed.EpisodeEnd)
			}
			download.DisplayTitle = baseParsed.Title + " · " + label
			if episode.Parsed.EpisodeTitle != "" {
				download.DisplayTitle += " · " + episode.Parsed.EpisodeTitle
			}
		}
	}
	download.ReleaseName = release.Name
	download.Category = release.Category
	download.ReleaseSizeBytes = release.SizeBytes
	download.TrackerSeeders = release.Seeders
	if metadata, err := s.repo.GetCatalogMetadata(ctx, download.TitleID); err == nil {
		download.Rating, download.RatingVotes, download.RatingProvider = metadata.Rating, metadata.RatingVotes, metadata.RatingProvider
	}
}

func (s *Service) Manage(ctx context.Context, id, action string, _ bool) error {
	d, err := s.repo.GetDownload(ctx, id)
	if err != nil {
		return err
	}
	hash, ok := s.route(d.EngineID)
	if !ok {
		return fmt.Errorf("unsupported engine route")
	}
	switch action {
	case "pause":
		err = s.engine.Pause(ctx, hash)
	case "resume", "retry":
		err = s.engine.Resume(ctx, hash)
		if action == "retry" && errors.Is(err, domain.ErrTorrentNotFound) {
			// The torrent vanished from the engine; forget the rows pinned to
			// it and re-prepare from the cached release, the same path a
			// fresh prepare uses when no download row exists.
			if forgetErr := s.forgetEngineRows(ctx, d.EngineID); forgetErr != nil {
				return forgetErr
			}
			if _, err = s.Prepare(ctx, d.ReleaseID, d.FileIndex); err == nil {
				return nil
			}
		}
	case "remove":
		managed, listErr := s.repo.ListDownloads(ctx)
		if listErr != nil {
			return listErr
		}
		for _, item := range managed {
			if item.EngineID == d.EngineID && item.Leased {
				return fmt.Errorf("cannot remove an actively streamed download")
			}
		}
		return s.removeTorrent(ctx, d.EngineID)
	default:
		return fmt.Errorf("unknown download action")
	}
	if err == nil {
		managed, listErr := s.repo.ListDownloads(ctx)
		if listErr != nil {
			return listErr
		}
		for _, item := range managed {
			if item.EngineID != d.EngineID {
				continue
			}
			item.State = action
			item.UpdatedAt = time.Now().UTC()
			if saveErr := s.repo.SaveDownload(ctx, item); saveErr != nil {
				return saveErr
			}
		}
	}
	return err
}

// removeTorrent deletes the torrent behind an Engine route — files included —
// and forgets every Managed download row pinned to that route. The manual
// remove action and retention eviction share this exact path (ADR-0004).
func (s *Service) removeTorrent(ctx context.Context, engineID string) error {
	hash, ok := s.route(engineID)
	if !ok {
		return fmt.Errorf("unsupported engine route")
	}
	if err := s.engine.Remove(ctx, hash, true); err != nil && !errors.Is(err, domain.ErrTorrentNotFound) {
		return err
	}
	return s.forgetEngineRows(ctx, engineID)
}

func (s *Service) forgetEngineRows(ctx context.Context, engineID string) error {
	managed, err := s.repo.ListDownloads(ctx)
	if err != nil {
		return err
	}
	for _, item := range managed {
		if item.EngineID != engineID {
			continue
		}
		if deleteErr := s.repo.DeleteDownload(ctx, item.ID); deleteErr != nil {
			return deleteErr
		}
	}
	return nil
}

const householdProfile = "household"

func (s *Service) Playback(ctx context.Context, sourceID string) (domain.PlaybackState, error) {
	return s.repo.GetPlayback(ctx, householdProfile, sourceID)
}

func (s *Service) PlaybackPreferences(ctx context.Context, sourceID string) (domain.PlaybackPreferences, error) {
	p, err := s.repo.GetPlaybackPreferences(ctx, householdProfile, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		settings := s.settings.Get()
		return domain.PlaybackPreferences{ProfileID: householdProfile, SourceID: sourceID, AudioLanguage: settings.PreferredAudioLanguage, AudioTrackIndex: -1, SubtitleLanguage: settings.PreferredSubtitleLanguage, SubtitleMode: "auto"}, nil
	}
	return p, err
}

func (s *Service) UpdatePlaybackPreferences(ctx context.Context, sourceID string, value domain.PlaybackPreferences) (domain.PlaybackPreferences, error) {
	if _, err := s.repo.GetDownload(ctx, sourceID); err != nil {
		return domain.PlaybackPreferences{}, err
	}
	value.AudioLanguage = strings.ToLower(strings.TrimSpace(value.AudioLanguage))
	value.SubtitleLanguage = strings.ToLower(strings.TrimSpace(value.SubtitleLanguage))
	value.SubtitleProvider = strings.ToLower(strings.TrimSpace(value.SubtitleProvider))
	value.SubtitleCandidateID = strings.TrimSpace(value.SubtitleCandidateID)
	value.SubtitleMode = strings.ToLower(strings.TrimSpace(value.SubtitleMode))
	if value.AudioLanguage == "" {
		value.AudioLanguage = s.settings.Get().PreferredAudioLanguage
	}
	if value.SubtitleLanguage == "" {
		value.SubtitleLanguage = s.settings.Get().PreferredSubtitleLanguage
	}
	if value.AudioTrackIndex < -1 {
		return domain.PlaybackPreferences{}, fmt.Errorf("audioTrackIndex cannot be below -1")
	}
	if value.SubtitleMode == "" {
		value.SubtitleMode = "auto"
	}
	if value.SubtitleMode != "auto" && value.SubtitleMode != "off" && value.SubtitleMode != "selected" {
		return domain.PlaybackPreferences{}, fmt.Errorf("subtitleMode must be auto, off, or selected")
	}
	if value.SubtitleMode == "selected" && (value.SubtitleProvider == "" || value.SubtitleCandidateID == "") {
		return domain.PlaybackPreferences{}, fmt.Errorf("selected subtitles require provider and candidate id")
	}
	value.ProfileID, value.SourceID, value.UpdatedAt = householdProfile, sourceID, time.Now().UTC()
	if err := s.repo.SavePlaybackPreferences(ctx, value); err != nil {
		return domain.PlaybackPreferences{}, err
	}
	return value, nil
}

func (s *Service) UpdatePlayback(ctx context.Context, sourceID string, positionMS, durationMS int64) (domain.PlaybackState, error) {
	if positionMS < 0 || durationMS < 0 {
		return domain.PlaybackState{}, fmt.Errorf("positionMs and durationMs cannot be negative")
	}
	if durationMS > 0 && positionMS > durationMS {
		positionMS = durationMS
	}
	d, err := s.repo.GetDownload(ctx, sourceID)
	if err != nil {
		return domain.PlaybackState{}, err
	}
	p, err := s.repo.GetPlayback(ctx, householdProfile, sourceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.PlaybackState{}, err
	}
	p.ProfileID = householdProfile
	p.SourceID = sourceID
	p.ReleaseID = d.ReleaseID
	p.FileIndex = d.FileIndex
	p.FilePath = d.FilePath
	p.PositionMS = positionMS
	p.DurationMS = durationMS
	threshold := int64(s.settings.Get().WatchedThresholdPercent)
	if durationMS > 0 && positionMS*100 >= durationMS*threshold {
		p.Watched = true
	}
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.SavePlayback(ctx, p); err != nil {
		return domain.PlaybackState{}, err
	}
	return p, nil
}

func (s *Service) SetWatched(ctx context.Context, sourceID string, watched bool) (domain.PlaybackState, error) {
	p, err := s.repo.GetPlayback(ctx, householdProfile, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		d, getErr := s.repo.GetDownload(ctx, sourceID)
		if getErr != nil {
			return domain.PlaybackState{}, getErr
		}
		p = domain.PlaybackState{ProfileID: householdProfile, SourceID: sourceID, ReleaseID: d.ReleaseID, FileIndex: d.FileIndex, FilePath: d.FilePath}
	} else if err != nil {
		return domain.PlaybackState{}, err
	}
	p.Watched = watched
	if watched && p.DurationMS > 0 {
		p.PositionMS = p.DurationMS
	}
	if !watched {
		p.PositionMS = 0
	}
	p.UpdatedAt = time.Now().UTC()
	if err := s.repo.SavePlayback(ctx, p); err != nil {
		return domain.PlaybackState{}, err
	}
	return p, nil
}

func (s *Service) SetFavorite(ctx context.Context, releaseID string, favorite bool) error {
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return err
	}
	parsed := domain.ParseRelease(release)
	return s.repo.SetFavorite(ctx, householdProfile, domain.CatalogTitleID(release, parsed), favorite)
}

func (s *Service) SetTitleFavorite(ctx context.Context, titleID string, favorite bool) error {
	sources, err := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{titleID})
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return sql.ErrNoRows
	}
	return s.repo.SetFavorite(ctx, householdProfile, titleID, favorite)
}

func (s *Service) HouseholdState(ctx context.Context) (domain.HouseholdState, error) {
	playback, err := s.repo.ListPlayback(ctx, householdProfile)
	if err != nil {
		return domain.HouseholdState{}, err
	}
	favorites, err := s.repo.ListFavorites(ctx, householdProfile)
	if err != nil {
		return domain.HouseholdState{}, err
	}
	downloads, _ := s.repo.ListDownloads(ctx)
	state := domain.HouseholdState{Favorites: []domain.HouseholdItem{}, ContinueWatching: []domain.HouseholdItem{}, Recent: []domain.HouseholdItem{}, Watched: []domain.HouseholdItem{}}
	releaseIDs := make([]string, 0, len(playback)+len(downloads))
	for _, p := range playback {
		if p.ReleaseID != "" {
			releaseIDs = append(releaseIDs, p.ReleaseID)
		}
	}
	for _, download := range downloads {
		if download.ReleaseID != "" {
			releaseIDs = append(releaseIDs, download.ReleaseID)
		}
	}
	releaseTitle, _ := s.repo.CatalogTitleIDsForReleases(ctx, releaseIDs)
	titleIDs := make([]string, 0, len(releaseTitle)+len(favorites))
	seenTitles := map[string]bool{}
	for _, id := range releaseTitle {
		if !seenTitles[id] {
			seenTitles[id] = true
			titleIDs = append(titleIDs, id)
		}
	}
	for _, favorite := range favorites {
		if !seenTitles[favorite.TitleID] {
			seenTitles[favorite.TitleID] = true
			titleIDs = append(titleIDs, favorite.TitleID)
		}
	}
	catalogSources, _ := s.repo.ListCatalogSourcesByTitleIDs(ctx, titleIDs)
	titleSources := map[string][]domain.CatalogSource{}
	for _, source := range catalogSources {
		titleSources[domain.CatalogTitleID(source.Release, source.Parsed)] = append(titleSources[domain.CatalogTitleID(source.Release, source.Parsed)], source)
	}
	favoriteSet := map[string]bool{}
	playbackBySource := map[string]domain.PlaybackState{}
	latestByTitle := map[string]domain.PlaybackState{}
	downloadByID := map[string]domain.Download{}
	downloadByTitle := map[string]domain.Download{}
	recentTitles := map[string]bool{}
	watchedTitles := map[string]bool{}
	continueTitles := map[string]bool{}
	for _, download := range downloads {
		downloadByID[download.ID] = download
		titleID := releaseTitle[download.ReleaseID]
		if titleID == "" {
			continue
		}
		current, exists := downloadByTitle[titleID]
		if !exists || betterHouseholdDownload(download, current) {
			downloadByTitle[titleID] = download
		}
	}
	for _, p := range playback {
		playbackBySource[p.SourceID] = p
		if titleID := releaseTitle[p.ReleaseID]; titleID != "" {
			if _, exists := latestByTitle[titleID]; !exists {
				latestByTitle[titleID] = p
			}
		}
		item, ok := s.householdItem(ctx, p, false, titleSources[releaseTitle[p.ReleaseID]])
		if !ok {
			continue
		}
		key := item.TitleID
		if key == "" && item.Catalog != nil {
			key = item.Catalog.ID
		}
		if key == "" {
			key = item.SourceID
		}
		if key == "" {
			key = item.Release.ID
		}
		if len(state.Recent) < 30 && !recentTitles[key] {
			state.Recent = append(state.Recent, item)
			recentTitles[key] = true
		}
		if p.Watched && len(state.Watched) < 50 && !watchedTitles[key] {
			state.Watched = append(state.Watched, item)
			watchedTitles[key] = true
		}
		if !p.Watched && p.PositionMS > 0 && len(state.ContinueWatching) < 50 && !continueTitles[key] {
			state.ContinueWatching = append(state.ContinueWatching, item)
			continueTitles[key] = true
		}
	}
	for _, f := range favorites {
		favoriteSet[f.TitleID] = true
		sources := titleSources[f.TitleID]
		if len(sources) == 0 {
			continue
		}
		p, hasPlayback := latestByTitle[f.TitleID]
		_, playbackDownloadExists := downloadByID[p.SourceID]
		if download, exists := downloadByTitle[f.TitleID]; exists && (!hasPlayback || !playbackDownloadExists) {
			if saved, ok := playbackBySource[download.ID]; ok {
				p = saved
			} else {
				p = domain.PlaybackState{ProfileID: householdProfile, SourceID: download.ID, ReleaseID: download.ReleaseID, FileIndex: download.FileIndex, FilePath: download.FilePath}
			}
			hasPlayback = true
		}
		if !hasPlayback {
			p = domain.PlaybackState{ProfileID: householdProfile, ReleaseID: sources[0].Release.ID, FileIndex: -1}
		}
		item, ok := s.householdItem(ctx, p, true, sources)
		if ok {
			state.Favorites = append(state.Favorites, item)
		}
	}
	for i := range state.Recent {
		state.Recent[i].Favorite = favoriteSet[state.Recent[i].TitleID]
	}
	for i := range state.Watched {
		state.Watched[i].Favorite = favoriteSet[state.Watched[i].TitleID]
	}
	for i := range state.ContinueWatching {
		state.ContinueWatching[i].Favorite = favoriteSet[state.ContinueWatching[i].TitleID]
	}
	return state, nil
}

func betterHouseholdDownload(candidate, current domain.Download) bool {
	if (candidate.Progress >= 1) != (current.Progress >= 1) {
		return candidate.Progress >= 1
	}
	return candidate.UpdatedAt.After(current.UpdatedAt)
}

func (s *Service) householdItem(ctx context.Context, p domain.PlaybackState, favorite bool, sources []domain.CatalogSource) (domain.HouseholdItem, bool) {
	release, err := s.repo.GetRelease(ctx, p.ReleaseID)
	if err != nil {
		return domain.HouseholdItem{}, false
	}
	parsed := domain.ParseRelease(release)
	item := domain.HouseholdItem{Release: release, PlaybackState: p, Favorite: favorite, TitleID: domain.CatalogTitleID(release, parsed), SeasonNumber: parsed.SeasonStart, EpisodeNumber: parsed.EpisodeStart}
	if parsed.Kind == domain.MediaSeries && p.FilePath != "" {
		base := domain.CatalogSource{Release: release, Parsed: parsed}
		file := domain.TorrentFile{Index: p.FileIndex, Path: p.FilePath, Playable: true}
		if episode, ok := episodeSource(base, file); ok {
			item.SeasonNumber = episode.Parsed.SeasonStart
			item.EpisodeNumber = episode.Parsed.EpisodeStart
		}
	}
	if len(sources) > 0 {
		title := groupCatalog(sources, false)[0]
		s.applyCachedMetadata(&title)
		if state, stateErr := s.catalogState(ctx); stateErr == nil {
			title.LibraryState = state.sourcesState(sources)
		}
		item.Catalog = &title
		item.TitleID = title.ID
	}
	return item, true
}

func (s *Service) Acquire(ctx context.Context, id string) (domain.Download, error) {
	d, err := s.repo.GetDownload(ctx, id)
	if err != nil {
		return d, err
	}
	d, err = s.prepareManagedDownload(ctx, d)
	if err != nil {
		return d, err
	}
	if err = s.repo.SetLease(ctx, id, true); err != nil {
		return d, err
	}
	d.Leased = true
	return d, nil
}

func (s *Service) Release(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.repo.SetLease(ctx, id, false)
}

func (s *Service) WaitRange(ctx context.Context, d domain.Download, start, count int64) error {
	hash, ok := s.route(d.EngineID)
	if !ok {
		return fmt.Errorf("unsupported engine route")
	}
	deadline := time.NewTimer(s.settings.PieceWaitTimeout())
	defer deadline.Stop()
	for {
		pieces, err := s.engine.Pieces(ctx, hash)
		if err != nil {
			return err
		}
		pieceSize := pieces.PieceSize
		if pieceSize <= 0 {
			pieceSize = d.PieceSize
		}
		if pieceSize <= 0 {
			return fmt.Errorf("qBittorrent did not report piece size")
		}
		first := (d.FileOffset + start) / pieceSize
		last := (d.FileOffset + start + count - 1) / pieceSize
		ready := first >= 0 && last < int64(len(pieces.States))
		if ready {
			for i := first; i <= last; i++ {
				if pieces.States[i] != 2 {
					ready = false
					break
				}
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for torrent pieces %d-%d", first, last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) WaitReadableRange(ctx context.Context, d domain.Download, start, count int64) error {
	_, err := s.ReadableRangePath(ctx, d, start, count)
	return err
}

func (s *Service) ValidateSourcePath(d domain.Download) error {
	root, err := filepath.Abs(s.settings.Get().DownloadRoot)
	if err != nil {
		return err
	}
	actual, err := filepath.Abs(d.AbsolutePath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, actual)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("persisted media path is outside the configured download root")
	}
	if !strings.HasSuffix(actual, filepath.FromSlash(d.FilePath)) {
		return fmt.Errorf("persisted media path no longer matches qBittorrent file path")
	}
	return nil
}

func (s *Service) TestStorage() (string, error) {
	root := s.settings.Get().DownloadRoot
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("download root %q is unavailable: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("download root %q is not a directory", root)
	}
	f, err := os.Open(root)
	if err != nil {
		return "", fmt.Errorf("download root %q is not readable: %w", root, err)
	}
	_ = f.Close()
	return "Download root is readable", nil
}

func sourceID(release, path string) string {
	sum := sha256.Sum256([]byte(release + "\x00" + path))
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}

// SetEngineRoutePrefix selects the Engine route prefix the active engine
// issues ("qb:" or "native:"). Composition calls it at startup; the empty
// default keeps the historical qBittorrent routing.
func (s *Service) SetEngineRoutePrefix(prefix string) {
	s.engineRoutePrefix = prefix
}

func (s *Service) enginePrefix() string {
	if s.engineRoutePrefix == "" {
		return "qb:"
	}
	return s.engineRoutePrefix
}

// route splits an Engine route into its engine hash. Routes issued by another
// engine fail the prefix check and surface as unavailable downloads — the
// behavior that already exists when an engine torrent goes missing.
func (s *Service) route(engineID string) (string, bool) {
	return strings.CutPrefix(engineID, s.enginePrefix())
}

func safeJoin(root, name string) (string, error) {
	r, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p, err := filepath.Abs(filepath.Join(r, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("torrent path escapes download root")
	}
	return p, nil
}

func safeQBPath(root, savePath, name string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if savePath == "" {
		savePath = rootAbs
	}
	saveAbs, err := filepath.Abs(savePath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, saveAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("qBittorrent save path is outside the configured download root")
	}
	return safeJoin(saveAbs, name)
}

func subtitle(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".srt", ".ass", ".ssa", ".vtt":
		return true
	}
	return false
}

// removeDeadRelease handles a release whose torrent the tracker no longer
// hosts: it drops the release from the catalog, journals the removal, and
// returns the error the user should see. The release cannot come back — the
// tracker deleted the torrent — so keeping it would only offer a dead button.
func (s *Service) removeDeadRelease(ctx context.Context, release domain.TorrentRelease) error {
	if err := s.repo.RemoveRelease(ctx, release.ID); err != nil {
		s.jobLog(domain.Job{}, "error", "prepare", "Could not remove a release deleted from FileList", map[string]any{"releaseId": release.ID, "error": err.Error()})
		return fmt.Errorf("this release is no longer available on FileList, but removing it from the catalog failed: %w", err)
	}
	_, _ = s.repo.AppendEvent(ctx, "release_removed", fmt.Sprintf(`{"releaseId":%q,"release":%q}`, release.ID, release.Name))
	return fmt.Errorf("this release is no longer available on FileList and was removed from your catalog; pick another version")
}

func trackerError(s domain.DownloadStatus) string {
	if s.TrackerError != "" {
		return s.TrackerError
	}
	for _, t := range s.Trackers {
		if t.Status == 4 && t.Message != "" {
			return t.Message
		}
	}
	return ""
}

var (
	_ = io.EOF
	_ = sql.ErrNoRows
)
