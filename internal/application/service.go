package application

import (
	"bytes"
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
	trackers         *TrackerRegistry
	engines          *EngineSet
	repo             Repository
	settings         *config.Store
	subtitles        []SubtitleProvider
	locks            sync.Map
	metadata         MetadataProvider
	mediaProbe       MediaProbe
	freeSpace        func(string) (int64, error)
	metaQueue        chan metadataRequest
	eventMu          sync.Mutex
	eventSubscribers map[chan domain.Event]struct{}
	syncMu           sync.Mutex
	refreshQueue     chan titleRefreshRequest
	searchQueue      chan trackerSearchRequest
	jobSlots         chan struct{}
	providerSlotsMu  sync.Mutex
	providerSlots    map[string]chan struct{}
	pendingMu        sync.Mutex
	pendingMetadata  map[string]bool
	mediaInfoMu      sync.Mutex
	mediaInfoCache   map[string]cachedMediaInfo
	providerMu       sync.Mutex
	providerCtx      context.Context
	providerCancel   context.CancelFunc
	baseCtx          context.Context
	cancelBase       context.CancelFunc
	stopping         chan struct{}
	closeOnce        sync.Once
	closeErr         error
	wg               sync.WaitGroup
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

func NewService(trackers *TrackerRegistry, engines *EngineSet, r Repository, s *config.Store, subtitles ...SubtitleProvider) *Service {
	limit := s.Get().MaxConcurrentJobs
	if limit < 1 {
		limit = 10
	}
	base, cancel := context.WithCancel(context.Background())
	provCtx, provCancel := context.WithCancel(base)
	service := &Service{
		trackers:         trackers,
		engines:          engines,
		repo:             r,
		settings:         s,
		subtitles:        subtitles,
		eventSubscribers: map[chan domain.Event]struct{}{},
		refreshQueue:     make(chan titleRefreshRequest, 256),
		searchQueue:      make(chan trackerSearchRequest, 256),
		jobSlots:         make(chan struct{}, limit),
		providerSlots:    make(map[string]chan struct{}),
		pendingMetadata:  map[string]bool{},
		mediaInfoCache:   map[string]cachedMediaInfo{},
		freeSpace:        freeDiskBytes,
		providerCtx:      provCtx,
		providerCancel:   provCancel,
		baseCtx:          base,
		cancelBase:       cancel,
		stopping:         make(chan struct{}),
	}
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

func (s *Service) acquireJob(ctx context.Context) error {
	select {
	case s.jobSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseJob() {
	<-s.jobSlots
}

func (s *Service) providerSlot(trackerID string) chan struct{} {
	s.providerSlotsMu.Lock()
	defer s.providerSlotsMu.Unlock()
	ch, ok := s.providerSlots[trackerID]
	if !ok {
		ch = make(chan struct{}, 1)
		s.providerSlots[trackerID] = ch
	}
	return ch
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
	if err := s.acquireJob(s.baseCtx); err != nil {
		return
	}
	job, _ := s.repo.GetJob(context.Background(), "metadata:"+request.TitleID)
	if job.ID == "" {
		// Retargeted or restarted work can arrive under a key whose job row
		// lives under the previous id; rebuild the identity from the request.
		job = domain.Job{ID: "metadata:" + request.TitleID, Kind: "metadata", DedupeKey: "metadata:" + request.TitleID}
	}
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
	// Re-resolve before applying results: a concurrent reconciliation can
	// merge this title mid-lookup and retarget (or coalesce away) the
	// queued/running job's row while preserving the historical id. The
	// surviving row names the canonical title this result must land on.
	requestedTitleID := request.TitleID
	fresh, freshErr := s.repo.GetJob(context.Background(), "metadata:"+requestedTitleID)
	if freshErr != nil {
		// The row was coalesced into a surviving duplicate (or deleted);
		// that job owns the work. Drop the result instead of resurrecting
		// a stale row under a dead id.
		cancel()
		s.pendingMu.Lock()
		delete(s.pendingMetadata, requestedTitleID)
		s.pendingMu.Unlock()
		s.releaseJob()
		return
	}
	if fresh.DedupeKey != "" {
		if retargeted := strings.TrimPrefix(fresh.DedupeKey, "metadata:"); retargeted != "" && retargeted != request.TitleID {
			request.TitleID = retargeted
		}
		// Adopt the stored row's id (not the new key) so job logs and events
		// stay attached to the persisted row; only the dedupe key moved.
		job.ID, job.DedupeKey = fresh.ID, fresh.DedupeKey
	}
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
	if sources, sourceErr := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{request.TitleID}, s.eligibleTrackerIDs()); sourceErr == nil && len(sources) > 0 {
		title := groupCatalog(sources, false)[0]
		applyMetadata(&title, metadata)
		payload["title"] = title
	}
	s.publish("metadata.updated", payload)
	cancel()
	s.pendingMu.Lock()
	delete(s.pendingMetadata, requestedTitleID)
	if request.TitleID != requestedTitleID {
		delete(s.pendingMetadata, request.TitleID)
	}
	s.pendingMu.Unlock()
	s.releaseJob()
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
	sources, err := s.repo.ListCatalogSourcesByTitleIDs(ctx, titleIDs, s.eligibleTrackerIDs())
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
	job := domain.Job{ID: "catalog-" + mode, Kind: "catalog-" + mode, State: "queued", Label: map[string]string{"latest": "Fetch latest releases", "rebuild": "Rebuild catalog"}[mode], DedupeKey: "catalog-" + mode, Attempt: 1, UpdatedAt: time.Now().UTC()}
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
	ctx := s.providerContext()
	job.State = "running"
	job.Progress = .02
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(ctx, job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "start", "Catalog synchronization started", map[string]any{"mode": mode})

	eligible := s.trackers.EligibleIDs()
	total := 0
	type trackerSyncResult struct {
		id    string
		count int
		err   error
	}
	results := make([]trackerSyncResult, len(eligible))
	var wg sync.WaitGroup

	for i, id := range eligible {
		tr, ok := s.trackers.Lookup(id)
		if !ok {
			continue
		}
		wg.Add(1)
		s.wg.Add(1)
		go func(idx int, trackerID string, tracker Tracker) {
			defer s.wg.Done()
			defer wg.Done()

			if err := s.acquireJob(ctx); err != nil {
				results[idx] = trackerSyncResult{id: trackerID, err: err}
				return
			}
			defer s.releaseJob()

			slot := s.providerSlot(trackerID)
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				results[idx] = trackerSyncResult{id: trackerID, err: ctx.Err()}
				return
			}
			defer func() { <-slot }()

			if !s.trackers.Eligible(trackerID) {
				return
			}

			childKey := trackerID + ":" + mode
			childJob := domain.Job{
				ID:        childKey,
				TrackerID: trackerID,
				Kind:      "catalog-" + mode,
				State:     "running",
				DedupeKey: childKey,
				Label:     fmt.Sprintf("%s %s sync", tracker.Name(), mode),
				Attempt:   1,
				UpdatedAt: time.Now().UTC(),
			}
			_ = s.repo.SaveJob(ctx, childJob)
			s.publish("job.updated", childJob)

			var items []domain.TorrentRelease
			var err error
			if mode == "latest" {
				items, err = tracker.Latest(ctx)
			} else {
				categories := tracker.Categories()
				for catIdx, category := range categories {
					if category.Excluded {
						continue
					}
					catItems, e := tracker.Category(ctx, category.ID)
					if e != nil {
						err = e
						break
					}
					items = append(items, catItems...)
					if len(categories) > 0 {
						childJob.Progress = float64(catIdx+1) / float64(len(categories))
					}
					childJob.UpdatedAt = time.Now().UTC()
					_ = s.repo.SaveJob(ctx, childJob)
					s.publish("job.updated", childJob)
				}
			}

			// Check live eligibility again before exposing results
			if !s.trackers.Eligible(trackerID) {
				childJob.State = "completed"
				childJob.Progress = 1
				childJob.UpdatedAt = time.Now().UTC()
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				return
			}

			if err != nil {
				s.failOrWait(&childJob, err, "catalog-sync")
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				_ = s.repo.RecordSync(ctx, childKey, 0, err)
				results[idx] = trackerSyncResult{id: trackerID, err: err}
				return
			}

			stored, upsertErr := s.upsertTrackerReleases(ctx, trackerID, tracker.Name(), items)
			if upsertErr != nil {
				s.failOrWait(&childJob, upsertErr, "catalog-sync")
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				_ = s.repo.RecordSync(ctx, childKey, 0, upsertErr)
				results[idx] = trackerSyncResult{id: trackerID, err: upsertErr}
				return
			}

			childJob.State = "completed"
			childJob.Progress = 1
			childJob.UpdatedAt = time.Now().UTC()
			_ = s.repo.SaveJob(ctx, childJob)
			s.publish("job.updated", childJob)
			_ = s.repo.RecordSync(ctx, childKey, len(stored), nil)
			s.publish("catalog.updated", map[string]any{"mode": mode, "tracker": trackerID, "items": len(stored), "job": childJob})
			results[idx] = trackerSyncResult{id: trackerID, count: len(stored)}
		}(i, id, tr)
	}
	wg.Wait()

	var namedFailures []string
	successfulTrackers := 0
	for _, res := range results {
		if res.err != nil {
			namedFailures = append(namedFailures, fmt.Sprintf("%s: %s", res.id, res.err.Error()))
		} else {
			successfulTrackers++
			total += res.count
		}
	}

	job.UpdatedAt = time.Now().UTC()
	if len(namedFailures) > 0 && successfulTrackers == 0 && len(eligible) > 0 {
		job.State = "failed"
		job.Error = strings.Join(namedFailures, "; ")
		job.Progress = 0
	} else {
		job.State = "completed"
		job.Retryable = false
		job.NextAttemptAt = nil
		job.Progress = 1
		if len(namedFailures) > 0 {
			job.Error = strings.Join(namedFailures, "; ")
		} else {
			job.Error = ""
		}
		if retained, discoverable, countErr := s.repo.CatalogCounts(ctx, s.eligibleTrackerIDs()); countErr == nil {
			job.Label = fmt.Sprintf("%s · %d refreshed · %d retained (%d discoverable)", job.Label, total, retained, discoverable)
		}
	}
	_ = s.repo.SaveJob(ctx, job)
	s.publish("job.updated", job)
	if job.State == "completed" {
		s.jobLog(job, "info", "complete", "Catalog synchronization completed", map[string]any{"items": total})
	} else {
		s.jobLog(job, "error", "complete", "Catalog synchronization failed", map[string]any{"error": job.Error})
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
		sources, sourceErr := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{titleID}, s.eligibleTrackerIDs())
		if sourceErr != nil || len(sources) == 0 {
			return domain.Job{}, fmt.Errorf("catalog title is unavailable")
		}
		return s.QueueTitleRefresh(ctx, titleID, groupCatalog(sources, false)[0].Title, true)
	case "tracker-search":
		query := strings.TrimPrefix(job.Label, "Search for ")
		if query == job.Label {
			query = strings.TrimPrefix(job.Label, "Search FileList for ")
		}
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
	s.engines.Each(func(_ string, engine TorrentEngine) {
		if closer, ok := engine.(io.Closer); ok {
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	})
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
		if job.TrackerID == "" && (job.Kind == "tracker-search" || job.Kind == "catalog-latest" || job.Kind == "catalog-rebuild" || job.Kind == "catalog-title-refresh") {
			job.TrackerID = "filelist"
		}
		if job.State != "queued" && job.State != "running" {
			continue
		}
		if job.Kind == "catalog-title-refresh" {
			query := strings.TrimPrefix(job.Label, "Refresh all versions of ")
			if query == job.Label || len([]rune(strings.TrimSpace(query))) < 3 {
				continue
			}
			if sources, sourceErr := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{strings.TrimPrefix(job.DedupeKey, "catalog-title-refresh:")}, s.eligibleTrackerIDs()); sourceErr == nil && len(sources) > 0 {
				allowed := false
				for _, source := range sources {
					allowed = allowed || !source.Release.DiscoveryExcluded
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
	total, discoverable, err := s.repo.CatalogCounts(ctx, s.eligibleTrackerIDs())
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": "append-only observed cache", "observedReleases": total, "discoverableReleases": discoverable, "hiddenDiscovery": total - discoverable, "fileListLatestWindowLimit": 100, "historicalPagination": false}, nil
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
	eligible := s.eligibleTrackerIDs()
	stale := true
	if len(eligible) > 0 {
		for _, id := range eligible {
			if age, err := s.repo.SyncAge(ctx, id+":latest"); err == nil && age >= 0 && time.Duration(age)*time.Second <= s.settings.CatalogMaxAge() {
				stale = false
				break
			}
		}
	}
	page, err := s.repo.ListReleases(ctx, search, category, limit, offset, eligible)
	page.Stale = stale
	return page, err
}

func (s *Service) Search(ctx context.Context, q string) (domain.Page[domain.TorrentRelease], error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) < 3 {
		return domain.Page[domain.TorrentRelease]{Items: []domain.TorrentRelease{}}, nil
	}
	eligible := s.trackers.EligibleIDs()
	if len(eligible) == 0 {
		return domain.Page[domain.TorrentRelease]{Items: []domain.TorrentRelease{}}, nil
	}

	sum := sha256.Sum256([]byte(strings.ToLower(q)))
	queryHash := base64.RawURLEncoding.EncodeToString(sum[:12])
	type trackerSearchResult struct {
		id    string
		items []domain.TorrentRelease
		err   error
	}
	results := make([]trackerSearchResult, len(eligible))
	var wg sync.WaitGroup

	for i, id := range eligible {
		tr, ok := s.trackers.Lookup(id)
		if !ok {
			continue
		}
		wg.Add(1)
		s.wg.Add(1)
		go func(idx int, trackerID string, tracker Tracker) {
			defer s.wg.Done()
			defer wg.Done()

			if err := s.acquireJob(ctx); err != nil {
				results[idx] = trackerSearchResult{id: trackerID, err: err}
				return
			}
			defer s.releaseJob()

			slot := s.providerSlot(trackerID)
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				results[idx] = trackerSearchResult{id: trackerID, err: ctx.Err()}
				return
			}
			defer func() { <-slot }()

			if !s.trackers.Eligible(trackerID) {
				return
			}

			searchKey := "tracker-search:" + trackerID + ":" + queryHash
			childJob := domain.Job{
				ID:        searchKey,
				TrackerID: trackerID,
				Kind:      "tracker-search",
				State:     "running",
				DedupeKey: searchKey,
				Label:     fmt.Sprintf("Search %s for %s", tracker.Name(), q),
				Attempt:   1,
				UpdatedAt: time.Now().UTC(),
			}
			_ = s.repo.SaveJob(ctx, childJob)
			s.publish("job.updated", childJob)

			items, err := tracker.Search(ctx, q)

			// Recheck live eligibility before exposing results
			if !s.trackers.Eligible(trackerID) {
				childJob.State = "completed"
				childJob.Progress = 1
				childJob.UpdatedAt = time.Now().UTC()
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				return
			}

			if err != nil {
				s.failOrWait(&childJob, err, "tracker-search")
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				_ = s.repo.RecordSync(ctx, searchKey, 0, err)
				results[idx] = trackerSearchResult{id: trackerID, err: err}
				return
			}

			stored, upsertErr := s.upsertTrackerReleases(ctx, trackerID, tracker.Name(), items)
			if upsertErr != nil {
				s.failOrWait(&childJob, upsertErr, "tracker-search")
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				_ = s.repo.RecordSync(ctx, searchKey, 0, upsertErr)
				results[idx] = trackerSearchResult{id: trackerID, err: upsertErr}
				return
			}

			seen := map[string]bool{}
			for _, release := range stored {
				if release.DiscoveryExcluded {
					continue
				}
				parsed := domain.ParseRelease(release)
				tid := domain.CatalogTitleID(release, parsed)
				if tid == "" || seen[tid] || len([]rune(strings.TrimSpace(parsed.Title))) < 3 {
					continue
				}
				seen[tid] = true
				_, _ = s.QueueTitleRefresh(context.Background(), tid, parsed.Title, false)
			}

			childJob.State = "completed"
			childJob.Progress = 1
			childJob.UpdatedAt = time.Now().UTC()
			_ = s.repo.SaveJob(ctx, childJob)
			s.publish("job.updated", childJob)
			_ = s.repo.RecordSync(ctx, searchKey, len(stored), nil)
			s.publish("catalog.search.completed", map[string]any{"query": q, "tracker": trackerID, "items": len(stored), "titleCount": len(seen)})
			results[idx] = trackerSearchResult{id: trackerID, items: stored}
		}(i, id, tr)
	}
	wg.Wait()

	var allStored []domain.TorrentRelease
	var namedFailures []string
	successful := 0
	for _, res := range results {
		if res.err != nil {
			namedFailures = append(namedFailures, fmt.Sprintf("%s: %s", res.id, res.err.Error()))
		} else {
			successful++
			allStored = append(allStored, res.items...)
		}
	}

	if len(namedFailures) > 0 && successful == 0 {
		return domain.Page[domain.TorrentRelease]{}, errors.New(strings.Join(namedFailures, "; "))
	}

	seenTitles := map[string]bool{}
	for _, release := range allStored {
		if release.DiscoveryExcluded {
			continue
		}
		parsed := domain.ParseRelease(release)
		tid := domain.CatalogTitleID(release, parsed)
		if tid != "" {
			seenTitles[tid] = true
		}
	}
	s.publish("catalog.search.completed", map[string]any{"query": q, "items": len(allStored), "titleCount": len(seenTitles)})
	return domain.Page[domain.TorrentRelease]{Items: allStored, Total: len(allStored)}, nil
}

func (s *Service) eligibleTrackerIDs() []string {
	return s.trackers.EligibleIDs()
}

func (s *Service) upsertTrackerReleases(ctx context.Context, trackerID, trackerName string, items []domain.TorrentRelease) ([]domain.TorrentRelease, error) {
	for i := range items {
		if items[i].TrackerID == "" {
			items[i].TrackerID = trackerID
			items[i].TrackerName = trackerName
		}
		if items[i].ProviderID == "" {
			items[i].ProviderID = items[i].ID
		}
	}
	return s.repo.UpsertReleases(ctx, items)
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
	key := searchJobKey(q)
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
	job := domain.Job{ID: key, Kind: "tracker-search", State: "queued", Label: "Search for " + q, DedupeKey: key, Attempt: attempt, UpdatedAt: time.Now().UTC()}
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
	key := searchJobKey(request.Query)
	job, _ := s.repo.GetJob(context.Background(), key)
	job.State = "running"
	job.Progress = .1
	job.Error = ""
	job.NextAttemptAt = nil
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "tracker-search", "Submitted search started", map[string]any{"query": request.Query})

	ctx, cancel := context.WithTimeout(s.providerContext(), time.Duration(s.settings.Get().TitleRefreshTimeoutMinutes)*time.Minute)
	defer cancel()

	page, err := s.Search(ctx, request.Query)
	job.UpdatedAt = time.Now().UTC()

	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(request.Query))))
	queryHash := base64.RawURLEncoding.EncodeToString(sum[:12])
	var namedFailures []string
	for _, id := range s.trackers.EligibleIDs() {
		childKey := "tracker-search:" + id + ":" + queryHash
		if cj, cjErr := s.repo.GetJob(context.Background(), childKey); cjErr == nil && cj.Error != "" {
			namedFailures = append(namedFailures, fmt.Sprintf("%s: %s", id, cj.Error))
		}
	}

	if err != nil && len(page.Items) == 0 {
		s.failOrWait(&job, err, "tracker-search")
	} else {
		job.State = "completed"
		job.Progress = 1
		job.Retryable = false
		job.NextAttemptAt = nil
		if len(namedFailures) > 0 {
			job.Error = strings.Join(namedFailures, "; ")
		} else {
			job.Error = ""
		}
		s.jobLog(job, "info", "complete", "Tracker search completed", map[string]any{"releases": len(page.Items)})
	}
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
}

func (s *Service) QueueTitleRefresh(ctx context.Context, titleID, query string, force bool) (domain.Job, error) {
	query = strings.TrimSpace(query)
	if titleID == "" || len([]rune(query)) < 3 {
		return domain.Job{}, fmt.Errorf("title and refresh query are required")
	}
	key := "catalog-title-refresh:" + titleID
	if !force {
		for _, id := range s.trackers.EligibleIDs() {
			refreshKey := "catalog-title-refresh:" + id + ":" + titleID
			if age, err := s.repo.SyncAge(ctx, refreshKey); err == nil && age >= 0 && age < int64(time.Hour/time.Second) {
				return domain.Job{ID: key, Kind: "catalog-title-refresh", State: "completed", Label: "Title was refreshed less than one hour ago", DedupeKey: key, Progress: 1, UpdatedAt: time.Now().UTC()}, nil
			}
		}
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

// titleRefreshKeyID extracts the title id from a catalog-title-refresh job or
// sync key. Title ids are base64url (no colon), so the id follows the last
// colon: either "catalog-title-refresh:<tid>" or
// "catalog-title-refresh:<tracker>:<tid>".
func titleRefreshKeyID(key string) string {
	if i := strings.LastIndex(key, ":"); i >= 0 {
		return key[i+1:]
	}
	return key
}

func (s *Service) runTitleRefresh(request titleRefreshRequest) {
	key := "catalog-title-refresh:" + request.TitleID
	job, _ := s.repo.GetJob(context.Background(), key)
	if job.ID == "" {
		// Reconciliation retargets dedupe keys while preserving the historical
		// id, so a restarted or merged title can arrive under a key whose job
		// row still lives under the previous id.
		job = domain.Job{ID: key, Kind: "catalog-title-refresh", Label: "Refresh all versions of " + request.Query, DedupeKey: key}
	}
	job.State = "running"
	job.Progress = .05
	job.Error = ""
	job.NextAttemptAt = nil
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
	s.jobLog(job, "info", "tracker-search", "Searching for title versions", map[string]any{"query": request.Query})

	eligible := s.trackers.EligibleIDs()
	type refreshResult struct {
		id    string
		count int
		err   error
	}
	results := make([]refreshResult, len(eligible))
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(s.providerContext(), time.Duration(s.settings.Get().TitleRefreshTimeoutMinutes)*time.Minute)
	defer cancel()

	for i, id := range eligible {
		tr, ok := s.trackers.Lookup(id)
		if !ok {
			continue
		}
		wg.Add(1)
		s.wg.Add(1)
		go func(idx int, trackerID string, tracker Tracker) {
			defer s.wg.Done()
			defer wg.Done()

			if err := s.acquireJob(ctx); err != nil {
				results[idx] = refreshResult{id: trackerID, err: err}
				return
			}
			defer s.releaseJob()

			slot := s.providerSlot(trackerID)
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				results[idx] = refreshResult{id: trackerID, err: ctx.Err()}
				return
			}
			defer func() { <-slot }()

			if !s.trackers.Eligible(trackerID) {
				return
			}

			refreshKey := "catalog-title-refresh:" + trackerID + ":" + request.TitleID
			childJob := domain.Job{
				ID:        refreshKey,
				TrackerID: trackerID,
				Kind:      "catalog-title-refresh",
				State:     "running",
				DedupeKey: refreshKey,
				Label:     fmt.Sprintf("Refresh %s for %s", tracker.Name(), request.Query),
				Attempt:   1,
				UpdatedAt: time.Now().UTC(),
			}
			_ = s.repo.SaveJob(ctx, childJob)
			s.publish("job.updated", childJob)

			items, err := tracker.Search(ctx, request.Query)

			// Recheck live eligibility before exposing results
			if !s.trackers.Eligible(trackerID) {
				childJob.State = "completed"
				childJob.Progress = 1
				childJob.UpdatedAt = time.Now().UTC()
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				return
			}

			if err != nil {
				s.failOrWait(&childJob, err, "title-refresh")
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				_ = s.repo.RecordSync(ctx, refreshKey, 0, err)
				results[idx] = refreshResult{id: trackerID, err: err}
				return
			}

			stored, upsertErr := s.upsertTrackerReleases(ctx, trackerID, tracker.Name(), items)
			// The upsert above can reconcile the family and retarget this
			// running job's dedupe key transactionally (historical id kept).
			// Re-resolve before applying results so completion writes land on
			// the surviving row and sync state records the canonical key.
			fresh, freshErr := s.repo.GetJob(ctx, refreshKey)
			if freshErr != nil {
				// The child row was coalesced into a sibling tracker's
				// surviving duplicate by reconciliation; that job owns the
				// work. Skip child persistence and events instead of
				// resurrecting a stale row; the catalog upsert itself stands.
				// A failed upsert still surfaces as a named failure so the
				// tracker error is not masked by the coalescing.
				if upsertErr != nil {
					results[idx] = refreshResult{id: trackerID, err: upsertErr}
				} else {
					results[idx] = refreshResult{id: trackerID, count: len(stored)}
				}
				return
			}
			// Adopt the stored row's id (not the new key) so job logs and
			// events stay attached to the persisted row; only the dedupe key
			// moved.
			childJob.ID, childJob.DedupeKey = fresh.ID, fresh.DedupeKey
			refreshKey = fresh.DedupeKey
			if upsertErr != nil {
				s.failOrWait(&childJob, upsertErr, "title-refresh")
				_ = s.repo.SaveJob(ctx, childJob)
				s.publish("job.updated", childJob)
				_ = s.repo.RecordSync(ctx, refreshKey, 0, upsertErr)
				results[idx] = refreshResult{id: trackerID, err: upsertErr}
				return
			}

			releaseIDs := make([]string, 0, len(stored))
			for _, release := range stored {
				releaseIDs = append(releaseIDs, release.ID)
			}
			projected, projErr := s.repo.CatalogTitleIDsForReleases(ctx, releaseIDs)
			if projErr != nil {
				s.jobLog(childJob, "warn", "torrent-manifest", "Could not resolve projected titles for manifest warmup", map[string]any{"error": projErr.Error()})
				projected = map[string]string{}
			}
			for _, release := range stored {
				if release.FileCount > 1 && projected[release.ID] == titleRefreshKeyID(refreshKey) {
					if _, manifestErr := s.torrentManifest(ctx, release); manifestErr != nil {
						s.jobLog(childJob, "warn", "torrent-manifest", "Could not inspect a multi-file torrent", map[string]any{"releaseId": release.ID, "release": release.Name, "error": manifestErr.Error()})
						if errors.Is(manifestErr, context.DeadlineExceeded) || errors.Is(manifestErr, context.Canceled) {
							break
						}
					}
				}
			}

			childJob.State = "completed"
			childJob.Progress = 1
			childJob.UpdatedAt = time.Now().UTC()
			_ = s.repo.SaveJob(ctx, childJob)
			s.publish("job.updated", childJob)
			_ = s.repo.RecordSync(ctx, refreshKey, len(stored), nil)
			s.publish("catalog.updated", map[string]any{"mode": "title", "titleId": titleRefreshKeyID(refreshKey), "tracker": trackerID, "items": len(stored), "job": childJob})
			results[idx] = refreshResult{id: trackerID, count: len(stored)}
		}(i, id, tr)
	}
	wg.Wait()

	var namedFailures []string
	total := 0
	successful := 0
	for _, res := range results {
		if res.err != nil {
			namedFailures = append(namedFailures, fmt.Sprintf("%s: %s", res.id, res.err.Error()))
		} else {
			successful++
			total += res.count
		}
	}

	// Re-resolve the projected title before applying results: the tracker
	// upserts inside this refresh may have merged the family and retargeted
	// (or coalesced away) this job's row while preserving the historical id.
	fresh, freshErr := s.repo.GetJob(context.Background(), key)
	if freshErr != nil {
		// The row was coalesced into a surviving duplicate by reconciliation
		// (or deleted); that job owns the work. Drop the result instead of
		// resurrecting a stale completed row and publishing a stale event.
		return
	}
	if fresh.DedupeKey != "" {
		// Adopt the stored row's id (not the new key) so job logs and events
		// stay attached to the persisted row; only the dedupe key moved.
		job.ID, job.DedupeKey = fresh.ID, fresh.DedupeKey
	}
	appliedTitleID := titleRefreshKeyID(job.DedupeKey)
	job.UpdatedAt = time.Now().UTC()
	if len(namedFailures) > 0 && successful == 0 && len(eligible) > 0 {
		job.State = "failed"
		job.Error = strings.Join(namedFailures, "; ")
		job.Progress = 0
	} else {
		job.State = "completed"
		job.Retryable = false
		job.NextAttemptAt = nil
		job.Progress = 1
		if len(namedFailures) > 0 {
			job.Error = strings.Join(namedFailures, "; ")
		} else {
			job.Error = ""
		}
		job.Label = fmt.Sprintf("Refreshed %s · %d releases", request.Query, total)
		_ = s.EnsureMetadata(context.Background(), []string{appliedTitleID})
	}
	_ = s.repo.SaveJob(context.Background(), job)
	if job.State == "completed" {
		s.jobLog(job, "info", "complete", "Title refresh completed", map[string]any{"releases": total})
	} else {
		s.jobLog(job, "error", "complete", "Title refresh failed", map[string]any{"error": job.Error})
	}
	s.publish("job.updated", job)
	s.publish("catalog.updated", map[string]any{"mode": "title", "titleId": appliedTitleID, "items": total, "job": job})
}

func (s *Service) TestEngine(ctx context.Context) (string, error) {
	return s.engines.Default().Test(ctx)
}

// ensureAllocationRoom is the starvation path of ADR-0004: before a new
// torrent is added, the Allocation must be able to hold it. Stored bytes come
// from the same survey the retention job runs, and the fit check is
// retentionDeficit itself — the incoming torrent is added to the surveyed
// total, so the cap math exists in exactly one place. When the Allocation
// would overflow, unprotected torrents are evicted one at a time (same hook
// the retention job uses) and the survey re-run; only when nothing evictable
// remains does the download fail with a visible Allocation problem. A zero
// Allocation disables the whole check.
func (s *Service) ensureAllocationRoom(ctx context.Context, release domain.TorrentRelease, incoming int64) error {
	settings := s.settings.Get()
	if settings.AllocationGB <= 0 {
		return nil
	}
	if incoming <= 0 {
		incoming = s.incomingTorrentBytes(ctx, release)
	}
	plan, err := s.retentionSurvey(ctx)
	if err != nil {
		return err
	}
	if err := refuseUncertainOwners(plan); err != nil {
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
		if err := refuseUncertainOwners(plan); err != nil {
			return err
		}
		plan.storedBytes += incoming
		reason, tripped = retentionDeficit(plan, settings)
	}
	return nil
}

// refuseUncertainOwners is the admission gate for survey uncertainty: a
// download may not be admitted while any owner's footprint is unknown — the
// under-count could wrongly report a cap fit. Checked on the initial survey
// and again after every mid-eviction re-survey.
func refuseUncertainOwners(plan retentionPlan) error {
	if len(plan.uncertainOwners) == 0 {
		return nil
	}
	return fmt.Errorf("refusing new download: cannot account for engine %s storage: %w",
		strings.Join(plan.uncertainOwners, ", "), domain.ErrEngineUnavailable)
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
		titleID, titleErr := s.projectedTitleID(ctx, release.ID)
		if titleErr != nil {
			return domain.Download{}, titleErr
		}
		s.enrichDownload(ctx, &existing, release, titleID)
		return s.prepareManagedDownload(ctx, existing)
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return domain.Download{}, existingErr
	}
	if existing, reuseErr := s.prepareExistingTorrentFile(ctx, release, fileIndex); reuseErr == nil {
		return existing, nil
	} else if !errors.Is(reuseErr, sql.ErrNoRows) {
		return domain.Download{}, reuseErr
	}
	_, err = s.trackers.RequireEligible(release.TrackerID)
	if err != nil {
		return domain.Download{}, err
	}
	if err := s.ensureAllocationRoom(ctx, release, 0); err != nil {
		return domain.Download{}, err
	}
	metainfo, err := s.releaseMetainfo(ctx, release)
	if err != nil {
		if errors.Is(err, domain.ErrTorrentRemoved) {
			return domain.Download{}, s.removeDeadRelease(ctx, release)
		}
		return domain.Download{}, err
	}
	manifest, err := parseTorrentManifest(release.ID, metainfo)
	if err != nil {
		return domain.Download{}, err
	}
	manifest.Metainfo = metainfo
	_ = s.repo.SaveTorrentManifest(ctx, manifest)

	incoming := release.SizeBytes
	var totalFiles int64
	for _, f := range manifest.Files {
		totalFiles += f.SizeBytes
	}
	if totalFiles > 0 {
		incoming = totalFiles
	}
	if incoming > release.SizeBytes {
		if err := s.ensureAllocationRoom(ctx, release, incoming); err != nil {
			return domain.Download{}, err
		}
	}
	engine := s.engines.Default()
	settings := s.settings.Get()
	hash, err := engine.Add(ctx, bytes.NewReader(metainfo), settings.DownloadRoot)
	if err != nil {
		return domain.Download{}, err
	}
	var files []domain.TorrentFile
	deadline := time.Now().Add(30 * time.Second)
	for {
		files, err = engine.Files(ctx, hash)
		if err == nil && len(files) > 0 {
			break
		}
		if time.Now().After(deadline) {
			return domain.Download{}, fmt.Errorf("%s metadata unavailable: %w", engineIdentity(s.engines.DefaultPrefix()), err)
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
	route := s.engines.DefaultPrefix() + hash
	indices := s.managedTorrentIndices(ctx, route, files, fileIndex)
	if err = engine.PrepareFiles(ctx, hash, indices, subs); err != nil {
		return domain.Download{}, err
	}
	status, err := engine.Status(ctx, hash)
	if err != nil {
		return domain.Download{}, err
	}
	abs, err := safeQBPath(settings.DownloadRoot, status.SavePath, selected.Path)
	if err != nil {
		return domain.Download{}, err
	}
	id := sourceID(releaseID, selected.Path)
	now := time.Now().UTC()
	d := domain.Download{
		ID:           id,
		ReleaseID:    releaseID,
		EngineID:     route,
		FileIndex:    fileIndex,
		FilePath:     selected.Path,
		AbsolutePath: abs,
		SizeBytes:    selected.SizeBytes,
		FileOffset:   selected.Offset,
		TrackerID:    release.TrackerID,
		TrackerName:  release.TrackerName,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	applyDownloadStatus(&d, status, selected)
	if old, e := s.repo.GetDownload(ctx, id); e == nil {
		d.CreatedAt = old.CreatedAt
	}
	if err = s.repo.SaveDownload(ctx, d); err != nil {
		return domain.Download{}, err
	}
	titleID, titleErr := s.projectedTitleID(ctx, release.ID)
	if titleErr != nil {
		return domain.Download{}, titleErr
	}
	s.enrichDownload(ctx, &d, release, titleID)
	return d, nil
}

func (s *Service) prepareExistingTorrentFile(ctx context.Context, release domain.TorrentRelease, fileIndex int) (domain.Download, error) {
	downloads, err := s.repo.ListDownloads(ctx)
	if err != nil {
		return domain.Download{}, err
	}
	unavailableOwner := ""
	for _, managed := range downloads {
		if managed.ReleaseID != release.ID {
			continue
		}
		engine, hash, ok := s.owner(managed.EngineID)
		if !ok {
			if unavailableOwner == "" {
				unavailableOwner = managed.EngineID
			}
			continue
		}
		files, filesErr := engine.Files(ctx, hash)
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
		status, statusErr := engine.Status(ctx, hash)
		if statusErr != nil {
			return domain.Download{}, statusErr
		}
		abs, pathErr := safeQBPath(s.settings.Get().DownloadRoot, status.SavePath, selected.Path)
		if pathErr != nil {
			return domain.Download{}, pathErr
		}
		now := time.Now().UTC()
		download := domain.Download{
			ID:           sourceID(release.ID, selected.Path),
			ReleaseID:    release.ID,
			EngineID:     managed.EngineID,
			FileIndex:    selected.Index,
			FilePath:     selected.Path,
			AbsolutePath: abs,
			SizeBytes:    selected.SizeBytes,
			FileOffset:   selected.Offset,
			TrackerID:    release.TrackerID,
			TrackerName:  release.TrackerName,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		applyDownloadStatus(&download, status, selected)
		if old, oldErr := s.repo.GetDownload(ctx, download.ID); oldErr == nil {
			download.CreatedAt = old.CreatedAt
		}
		if saveErr := s.repo.SaveDownload(ctx, download); saveErr != nil {
			return domain.Download{}, saveErr
		}
		titleID, titleErr := s.projectedTitleID(ctx, release.ID)
		if titleErr != nil {
			return domain.Download{}, titleErr
		}
		s.enrichDownload(ctx, &download, release, titleID)
		return s.prepareManagedDownload(ctx, download)
	}
	if unavailableOwner != "" {
		return domain.Download{}, s.engineUnavailableErr(unavailableOwner)
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
	projected, err := s.repo.CatalogTitleIDsForReleases(ctx, []string{release.ID})
	if err != nil {
		return nil, err
	}
	detail, err := s.CatalogDetail(ctx, projected[release.ID])
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

	var engine TorrentEngine
	engineID, hash, enginePrefix := "", "", ""
	unavailableOwner := ""
	if managed, listErr := s.repo.ListDownloads(ctx); listErr == nil {
		for _, download := range managed {
			if download.ReleaseID == releaseID {
				if owned, existingHash, ok := s.owner(download.EngineID); ok {
					engine, engineID, hash, enginePrefix = owned, download.EngineID, existingHash, strings.TrimSuffix(download.EngineID, existingHash)
					break
				}
				if unavailableOwner == "" {
					unavailableOwner = download.EngineID
				}
			}
		}
	}
	settings := s.settings.Get()
	if hash == "" && unavailableOwner != "" {
		return nil, s.engineUnavailableErr(unavailableOwner)
	}
	if hash == "" {
		_, err = s.trackers.RequireEligible(release.TrackerID)
		if err != nil {
			return nil, err
		}
		if err := s.ensureAllocationRoom(ctx, release, 0); err != nil {
			return nil, err
		}
		metainfo, err := s.releaseMetainfo(ctx, release)
		if err != nil {
			if errors.Is(err, domain.ErrTorrentRemoved) {
				return nil, s.removeDeadRelease(ctx, release)
			}
			return nil, err
		}
		manifest, err := parseTorrentManifest(release.ID, metainfo)
		if err != nil {
			return nil, err
		}
		manifest.Metainfo = metainfo
		_ = s.repo.SaveTorrentManifest(ctx, manifest)

		incoming := release.SizeBytes
		var totalFiles int64
		for _, f := range manifest.Files {
			totalFiles += f.SizeBytes
		}
		if totalFiles > 0 {
			incoming = totalFiles
		}
		if incoming > release.SizeBytes {
			if fitErr := s.ensureAllocationRoom(ctx, release, incoming); fitErr != nil {
				return nil, fitErr
			}
		}
		engine = s.engines.Default()
		enginePrefix = s.engines.DefaultPrefix()
		hash, err = engine.Add(ctx, bytes.NewReader(metainfo), settings.DownloadRoot)
		if err != nil {
			return nil, err
		}
		engineID = enginePrefix + hash
	}

	var files []domain.TorrentFile
	deadline := time.Now().Add(30 * time.Second)
	for {
		files, err = engine.Files(ctx, hash)
		if err == nil && len(files) > 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s metadata unavailable: %w", engineIdentity(enginePrefix), err)
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
	if err = engine.PrepareFiles(ctx, hash, indices, subs); err != nil {
		return nil, err
	}
	status, err := engine.Status(ctx, hash)
	if err != nil {
		return nil, err
	}
	manifest := domain.TorrentManifest{ReleaseID: releaseID, Files: files, FetchedAt: time.Now().UTC()}
	_ = s.repo.SaveTorrentManifest(ctx, manifest)
	now := time.Now().UTC()
	titleID, titleErr := s.projectedTitleID(ctx, releaseID)
	if titleErr != nil {
		return nil, titleErr
	}
	out := make([]domain.Download, 0, len(selected))
	for i := range selected {
		file := &selected[i]
		abs, pathErr := safeQBPath(settings.DownloadRoot, status.SavePath, file.Path)
		if pathErr != nil {
			return nil, pathErr
		}
		download := domain.Download{
			ID:           sourceID(releaseID, file.Path),
			ReleaseID:    releaseID,
			EngineID:     engineID,
			FileIndex:    file.Index,
			FilePath:     file.Path,
			AbsolutePath: abs,
			SizeBytes:    file.SizeBytes,
			FileOffset:   file.Offset,
			TrackerID:    release.TrackerID,
			TrackerName:  release.TrackerName,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		applyDownloadStatus(&download, status, file)
		if old, oldErr := s.repo.GetDownload(ctx, download.ID); oldErr == nil {
			download.CreatedAt = old.CreatedAt
		}
		if saveErr := s.repo.SaveDownload(ctx, download); saveErr != nil {
			return nil, saveErr
		}
		s.enrichDownload(ctx, &download, release, titleID)
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
	engine, hash, ok := s.owner(d.EngineID)
	if !ok {
		return d, s.engineUnavailableErr(d.EngineID)
	}
	lockAny, _ := s.locks.LoadOrStore("stream:"+hash, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	status, err := engine.Status(ctx, hash)
	if err != nil {
		// A completed local file remains playable even when qBittorrent is down.
		if completedLocalFile(d) {
			return d, nil
		}
		return d, err
	}
	if domain.IsPaused(status.State) {
		if err = engine.Resume(ctx, hash); err != nil {
			return d, fmt.Errorf("resume torrent for playback: %w", err)
		}
	}
	files, err := engine.Files(ctx, hash)
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
	if err = engine.PrepareFiles(ctx, hash, indices, subs); err != nil {
		return d, err
	}
	status, err = engine.Status(ctx, hash)
	if err != nil {
		return d, err
	}
	if !status.Sequential || !status.FirstLastPriority {
		return d, fmt.Errorf("engine did not enable progressive streaming priorities")
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
	releaseIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.ReleaseID != "" {
			releaseIDs = append(releaseIDs, item.ReleaseID)
		}
	}
	projected, projErr := s.repo.CatalogTitleIDsForReleases(ctx, releaseIDs)
	if projErr != nil {
		return nil, projErr
	}
	for i := range items {
		if release, releaseErr := s.repo.GetRelease(ctx, items[i].ReleaseID); releaseErr == nil {
			s.enrichDownload(ctx, &items[i], release, projected[items[i].ReleaseID])
		}
		engine, hash, ok := s.owner(items[i].EngineID)
		var st domain.DownloadStatus
		statusErr := error(nil)
		if ok {
			st, statusErr = engine.Status(ctx, hash)
		} else {
			// The owning engine is absent from the set: the row surfaces as
			// unavailable rather than serving stale persisted state.
			statusErr = s.engineUnavailableErr(items[i].EngineID)
		}
		if statusErr != nil {
			items[i].Error = statusErr.Error()
			items[i].State = "unavailable"
		} else {
			var selected *domain.TorrentFile
			if files, filesErr := engine.Files(ctx, hash); filesErr == nil {
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

func (s *Service) enrichDownload(ctx context.Context, download *domain.Download, release domain.TorrentRelease, titleID string) {
	baseParsed := domain.ParseRelease(release)
	download.Parsed = baseParsed
	download.TitleID = titleID
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
	engine, hash, ok := s.owner(d.EngineID)
	if !ok {
		return s.engineUnavailableErr(d.EngineID)
	}
	switch action {
	case "pause":
		err = engine.Pause(ctx, hash)
	case "resume", "retry":
		err = engine.Resume(ctx, hash)
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
	engine, hash, ok := s.owner(engineID)
	if !ok {
		return s.engineUnavailableErr(engineID)
	}
	if err := engine.Remove(ctx, hash, true); err != nil && !errors.Is(err, domain.ErrTorrentNotFound) {
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

// SetFavorite resolves the CURRENT projected canonical title for the release
// at write time, so a favorite never lands on a group the reconciliation has
// retired. Only a release with no persisted projection (not upserted yet)
// falls back to computing the id from its own release evidence.
func (s *Service) SetFavorite(ctx context.Context, releaseID string, favorite bool) error {
	projected, err := s.repo.CatalogTitleIDsForReleases(ctx, []string{releaseID})
	if err != nil {
		return err
	}
	titleID := projected[releaseID]
	if titleID == "" {
		release, err := s.repo.GetRelease(ctx, releaseID)
		if err != nil {
			return err
		}
		titleID = domain.CatalogTitleID(release, domain.ParseRelease(release))
	}
	return s.repo.SetFavorite(ctx, householdProfile, titleID, favorite)
}

// projectedTitleID resolves one release's canonical title id from the
// persisted catalog projection.
func (s *Service) projectedTitleID(ctx context.Context, releaseID string) (string, error) {
	ids, err := s.repo.CatalogTitleIDsForReleases(ctx, []string{releaseID})
	if err != nil {
		return "", err
	}
	return ids[releaseID], nil
}

// managedTitleIDs resolves the canonical title ID for every managed download
// from the persisted catalog projection. Downloads do not store title IDs, so
// disabled-only titles are matched through their release IDs instead of a
// per-row field.
func (s *Service) managedTitleIDs(ctx context.Context, downloads []domain.Download) (map[string]string, error) {
	releaseIDs := make([]string, 0, len(downloads))
	for _, dl := range downloads {
		if dl.ReleaseID != "" {
			releaseIDs = append(releaseIDs, dl.ReleaseID)
		}
	}
	return s.repo.CatalogTitleIDsForReleases(ctx, releaseIDs)
}

func (s *Service) SetTitleFavorite(ctx context.Context, titleID string, favorite bool) error {
	sources, err := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{titleID}, s.eligibleTrackerIDs())
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		hasManaged := false
		downloads, dlErr := s.repo.ListDownloads(ctx)
		if dlErr != nil {
			return dlErr
		}
		titleIDs, idErr := s.managedTitleIDs(ctx, downloads)
		if idErr != nil {
			return idErr
		}
		for _, dl := range downloads {
			if titleIDs[dl.ReleaseID] == titleID {
				hasManaged = true
				break
			}
		}
		if !hasManaged {
			return sql.ErrNoRows
		}
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
	catalogSources, _ := s.repo.ListCatalogSourcesByTitleIDs(ctx, titleIDs, s.eligibleTrackerIDs())
	titleSources := map[string][]domain.CatalogSource{}
	for _, source := range catalogSources {
		titleSources[source.TitleID] = append(titleSources[source.TitleID], source)
	}
	for _, relID := range releaseIDs {
		if tid := releaseTitle[relID]; tid != "" && len(titleSources[tid]) == 0 {
			if rel, relErr := s.repo.GetRelease(ctx, relID); relErr == nil {
				titleSources[tid] = append(titleSources[tid], domain.CatalogSource{Release: rel, Parsed: domain.ParseRelease(rel), TitleID: tid})
			}
		}
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
		item, ok := s.householdItem(ctx, p, false, titleSources[releaseTitle[p.ReleaseID]], releaseTitle[p.ReleaseID])
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
		item, ok := s.householdItem(ctx, p, true, sources, f.TitleID)
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

func (s *Service) householdItem(ctx context.Context, p domain.PlaybackState, favorite bool, sources []domain.CatalogSource, titleID string) (domain.HouseholdItem, bool) {
	release, err := s.repo.GetRelease(ctx, p.ReleaseID)
	if err != nil {
		return domain.HouseholdItem{}, false
	}
	parsed := domain.ParseRelease(release)
	item := domain.HouseholdItem{Release: release, PlaybackState: p, Favorite: favorite, TitleID: titleID, SeasonNumber: parsed.SeasonStart, EpisodeNumber: parsed.EpisodeStart}
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
	engine, hash, ok := s.owner(d.EngineID)
	if !ok {
		return s.engineUnavailableErr(d.EngineID)
	}
	deadline := time.NewTimer(s.settings.PieceWaitTimeout())
	defer deadline.Stop()
	for {
		pieces, err := engine.Pieces(ctx, hash)
		if err != nil {
			return err
		}
		pieceSize := pieces.PieceSize
		if pieceSize <= 0 {
			pieceSize = d.PieceSize
		}
		if pieceSize <= 0 {
			return fmt.Errorf("engine did not report piece size")
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

// engineIdentities maps owner prefixes to the user-facing engine identity
// used in diagnostics and error messages.
var engineIdentities = map[string]string{
	"native:": EngineNative,
	"qb:":     EngineQbittorrent,
}

// engineIdentity names an owner prefix for diagnostics and messages:
// "native:" -> "native", "qb:" -> "qbittorrent".
func engineIdentity(prefix string) string {
	if name, ok := engineIdentities[prefix]; ok {
		return name
	}
	return strings.TrimSuffix(prefix, ":")
}

// routePrefix returns the owner prefix of a "prefix:hash" route.
func routePrefix(route string) string {
	prefix, _, _ := strings.Cut(route, ":")
	return prefix + ":"
}

// owner splits an Engine route and returns the engine registered under its
// prefix. Routes issued by an engine absent from the set fail the lookup and
// surface as unavailable downloads — there is no fallback to the default.
func (s *Service) owner(route string) (TorrentEngine, string, bool) {
	return s.engines.Resolve(route)
}

// engineUnavailableErr renders the neutral unavailable error for a download
// whose owning engine is absent from the set: it names the route and either
// the engine's construction error or the fact it was never constructed.
func (s *Service) engineUnavailableErr(route string) error {
	initErr, _ := s.engines.InitError(routePrefix(route))
	reason := "not constructed"
	if initErr != nil {
		reason = initErr.Error()
	}
	return fmt.Errorf("%w: %s (%s)", domain.ErrEngineUnavailable, route, reason)
}

// EngineDefault names the engine new acquisitions are issued under.
func (s *Service) EngineDefault() string {
	return engineIdentity(s.engines.DefaultPrefix())
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
