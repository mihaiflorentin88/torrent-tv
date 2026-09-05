package application

import (
	"cmp"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// retentionKind is the persisted Job kind for storage enforcement. The
// scheduler runs it hourly next to the catalog sync.
const retentionKind = "retention"

// gigabytesToBytes converts the fractional-GB settings into bytes using binary
// gigabytes (GiB), the convention qBittorrent reports torrent sizes in.
func gigabytesToBytes(gb float64) int64 {
	return int64(gb * (1 << 30))
}

// retentionRoute is one Engine route — a torrent — plus every Managed download
// row pinned to it. Season-pack siblings share a route and die together. The
// household fields are resolved once per survey from the persisted favorites
// and playback state, and feed the protection toggles and the recency and
// watched ordering rules.
type retentionRoute struct {
	engineID   string
	hash       string
	rows       []domain.Download
	status     domain.DownloadStatus
	favorite   bool
	watched    bool
	lastPlayed time.Time
}

// completedAt stands in for a completion timestamp: the oldest row timestamp
// on the route, since the schema keeps no dedicated completion column.
func (r retentionRoute) completedAt() time.Time {
	oldest := r.rows[0].UpdatedAt
	for _, row := range r.rows[1:] {
		if row.UpdatedAt.Before(oldest) {
			oldest = row.UpdatedAt
		}
	}
	return oldest
}

// retentionProtected is the eviction protection predicate, driven by the user
// toggles of the current run's settings snapshot (ADR-0004): incomplete and
// actively-streamed (leased) downloads default to protected, favorites and
// never-watched media are opt-in.
func retentionProtected(route retentionRoute, settings config.Settings) bool {
	if settings.ProtectIncomplete && routeIncomplete(route) {
		return true
	}
	if settings.ProtectLeased && routeLeased(route) {
		return true
	}
	if settings.ProtectFavorites && route.favorite {
		return true
	}
	if settings.ProtectNeverWatched && !route.watched {
		return true
	}
	return false
}

// routeIncomplete reports whether the torrent or any pinned row is still
// downloading.
func routeIncomplete(route retentionRoute) bool {
	if route.status.Progress < 1 {
		return true
	}
	for _, row := range route.rows {
		if row.Progress < 1 {
			return true
		}
	}
	return false
}

// routeLeased reports whether any pinned row is being actively streamed.
func routeLeased(route retentionRoute) bool {
	for _, row := range route.rows {
		if row.Leased {
			return true
		}
	}
	return false
}

// applyEvictionRules orders candidates for eviction by walking the composed
// rule list in order (ADR-0004): each rule compares the whole pair, and the
// first distinguishing rule wins. Routes every rule ties on break to the
// oldest completed download, then EngineID — the historical default.
func applyEvictionRules(candidates []retentionRoute, rules []string) {
	sort.Slice(candidates, func(i, j int) bool {
		for _, rule := range rules {
			if c := compareEvictionRule(rule, candidates[i], candidates[j]); c != 0 {
				return c < 0
			}
		}
		if c := candidates[i].completedAt().Compare(candidates[j].completedAt()); c != 0 {
			return c < 0
		}
		return candidates[i].engineID < candidates[j].engineID
	})
}

// compareEvictionRule ranks two routes under one rule atom; negative means a
// is evicted first. Recency atoms use the household playback timestamp and
// stand in the download's oldest row timestamp for never-played routes.
func compareEvictionRule(rule string, a, b retentionRoute) int {
	switch rule {
	case "newest-completed":
		return b.completedAt().Compare(a.completedAt())
	case "least-recently-played":
		return a.lastActivity().Compare(b.lastActivity())
	case "most-recently-played":
		return b.lastActivity().Compare(a.lastActivity())
	case "watched-first":
		return boolFirst(a.watched, b.watched)
	case "never-watched-first":
		return boolFirst(!a.watched, !b.watched)
	case "largest":
		return cmp.Compare(b.status.TotalBytes, a.status.TotalBytes)
	case "smallest":
		return cmp.Compare(a.status.TotalBytes, b.status.TotalBytes)
	default: // oldest-completed, and anything unrecognized stays safe
		return a.completedAt().Compare(b.completedAt())
	}
}

// boolFirst orders true before false.
func boolFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}

// lastActivity is the recency timestamp for the played-first rules: the
// household playback time when the route was last played, otherwise the
// download's oldest row timestamp so never-played media competes by its age.
func (r retentionRoute) lastActivity() time.Time {
	if !r.lastPlayed.IsZero() {
		return r.lastPlayed
	}
	return r.completedAt()
}

// retentionPlan is one evaluation pass over live engine telemetry.
type retentionPlan struct {
	routes      []retentionRoute
	storedBytes int64
	freeBytes   int64
	freeErr     error
}

// retentionSurvey groups Managed downloads by Engine route, samples engine
// Status once per route, and probes free space on the download root. A route
// the engine cannot describe is neither counted nor evicted — protection errs
// on the safe side.
func (s *Service) retentionSurvey(ctx context.Context) (retentionPlan, error) {
	managed, err := s.repo.ListDownloads(ctx)
	if err != nil {
		return retentionPlan{}, err
	}
	byRoute := map[string][]domain.Download{}
	order := []string{}
	for _, row := range managed {
		if _, ok := s.route(row.EngineID); !ok {
			continue
		}
		if _, seen := byRoute[row.EngineID]; !seen {
			order = append(order, row.EngineID)
		}
		byRoute[row.EngineID] = append(byRoute[row.EngineID], row)
	}
	household, err := s.retentionHousehold(ctx, managed)
	if err != nil {
		return retentionPlan{}, err
	}
	plan := retentionPlan{}
	for _, engineID := range order {
		hash, _ := s.route(engineID)
		status, statusErr := s.engine.Status(ctx, hash)
		if statusErr != nil {
			continue
		}
		favorite, watched, lastPlayed := household.routeFacts(byRoute[engineID])
		plan.routes = append(plan.routes, retentionRoute{engineID: engineID, hash: hash, rows: byRoute[engineID], status: status, favorite: favorite, watched: watched, lastPlayed: lastPlayed})
		plan.storedBytes += status.TotalBytes
	}
	plan.freeBytes, plan.freeErr = s.freeSpace(s.settings.Get().DownloadRoot)
	return plan, nil
}

// retentionHousehold resolves the Household facts the eviction rules consume:
// the favorite title set and the latest playback state per canonical title.
// Matching is title-level — the same aggregation the household screens use —
// so a re-downloaded title keeps its watched and favorite status.
type retentionHousehold struct {
	favoriteTitles map[string]bool
	latestByTitle  map[string]domain.PlaybackState
	titleByRelease map[string]string
}

func (s *Service) retentionHousehold(ctx context.Context, managed []domain.Download) (retentionHousehold, error) {
	h := retentionHousehold{favoriteTitles: map[string]bool{}, latestByTitle: map[string]domain.PlaybackState{}, titleByRelease: map[string]string{}}
	releaseIDs := make([]string, 0, len(managed))
	seen := map[string]bool{}
	for _, row := range managed {
		if row.ReleaseID == "" || seen[row.ReleaseID] {
			continue
		}
		seen[row.ReleaseID] = true
		releaseIDs = append(releaseIDs, row.ReleaseID)
	}
	titles, err := s.repo.CatalogTitleIDsForReleases(ctx, releaseIDs)
	if err != nil {
		return h, err
	}
	h.titleByRelease = titles
	favorites, err := s.repo.ListFavorites(ctx, householdProfile)
	if err != nil {
		return h, err
	}
	for _, favorite := range favorites {
		h.favoriteTitles[favorite.TitleID] = true
	}
	playback, err := s.repo.ListPlayback(ctx, householdProfile)
	if err != nil {
		return h, err
	}
	for _, p := range playback {
		titleID := h.titleByRelease[p.ReleaseID]
		if titleID == "" {
			continue
		}
		if latest, ok := h.latestByTitle[titleID]; !ok || p.UpdatedAt.After(latest.UpdatedAt) {
			h.latestByTitle[titleID] = p
		}
	}
	return h, nil
}

// routeFacts projects the household state onto one Engine route: the route is
// favorited when any pinned row's canonical title is a household favorite,
// watched when the title's latest playback is watched, and its lastPlayed is
// the latest playback timestamp across the route's titles (zero when the
// household never played them).
func (h retentionHousehold) routeFacts(rows []domain.Download) (favorite, watched bool, lastPlayed time.Time) {
	for _, row := range rows {
		titleID := h.titleByRelease[row.ReleaseID]
		if titleID == "" {
			continue
		}
		if h.favoriteTitles[titleID] {
			favorite = true
		}
		playback, ok := h.latestByTitle[titleID]
		if !ok {
			continue
		}
		if playback.Watched {
			watched = true
		}
		if lastPlayed.IsZero() || playback.UpdatedAt.After(lastPlayed) {
			lastPlayed = playback.UpdatedAt
		}
	}
	return favorite, watched, lastPlayed
}

// retentionDeficit reports which check tripped. The Allocation cap is
// evaluated first; the Reserve only triggers eviction while the cap is
// satisfied. A zero-valued setting disables its check.
func retentionDeficit(plan retentionPlan, settings config.Settings) (string, bool) {
	if settings.AllocationGB > 0 {
		if excess := plan.storedBytes - gigabytesToBytes(settings.AllocationGB); excess > 0 {
			return "cap", true
		}
	}
	if settings.ReserveGB > 0 && plan.freeErr == nil {
		if gigabytesToBytes(settings.ReserveGB) > plan.freeBytes {
			return "reserve", true
		}
	}
	return "", false
}

// evictNext removes one unprotected torrent from the surveyed plan — the
// first candidate under the run's configured rule list (ADR-0004) — through
// the same delete path as the manual remove action, and announces it on the
// live feed. It reports false when storage holds no evictable torrent. The
// retention job and the download admission gate (starvation path) share this
// hook; the protection predicate and ordering live only here.
func (s *Service) evictNext(ctx context.Context, plan retentionPlan, reason string, settings config.Settings, rules []string) (retentionRoute, bool, error) {
	candidates := make([]retentionRoute, 0, len(plan.routes))
	for _, route := range plan.routes {
		if !retentionProtected(route, settings) {
			candidates = append(candidates, route)
		}
	}
	if len(candidates) == 0 {
		return retentionRoute{}, false, nil
	}
	applyEvictionRules(candidates, rules)
	victim := candidates[0]
	if err := s.removeTorrent(ctx, victim.engineID); err != nil {
		return retentionRoute{}, false, err
	}
	s.publish("downloads.evicted", s.evictionEvent(ctx, victim, reason))
	return victim, true, nil
}

// RunRetention enforces the Allocation cap and free-space Reserve (ADR-0004).
// It evicts one torrent at a time — through the same delete path as the manual
// remove action — re-evaluating after each until storage fits again or only
// protected downloads remain. The run is synchronous, follows the persisted
// Job pattern, and is scheduled hourly next to the catalog sync.
func (s *Service) RunRetention() (domain.Job, error) {
	job := domain.Job{ID: retentionKind, Kind: retentionKind, State: "queued", Label: "Enforce storage allocation and reserve", DedupeKey: retentionKind, Attempt: 1, UpdatedAt: time.Now().UTC()}
	jobs, listErr := s.repo.ListJobs(context.Background(), 200)
	if listErr != nil {
		return domain.Job{}, listErr
	}
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
	s.runRetention(job)
	return s.repo.GetJob(context.Background(), job.ID)
}

func (s *Service) runRetention(job domain.Job) {
	ctx := context.Background()
	settings := s.settings.Get()
	rules := config.NormalizeEvictionRules(settings.EvictionRules)
	job.State = "running"
	job.Progress = .05
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(ctx, job)
	s.publish("job.updated", job)

	plan, err := s.retentionSurvey(ctx)
	if err != nil {
		s.failOrWait(&job, err, retentionKind)
		s.finishRetentionJob(job, 0, 0)
		return
	}
	if settings.ReserveGB > 0 && plan.freeErr != nil {
		s.jobLog(job, "warn", retentionKind, "Free space unavailable on the download root; reserve check skipped", map[string]any{"error": plan.freeErr.Error()})
	}
	evicted, freedBytes := 0, int64(0)
	for {
		reason, tripped := retentionDeficit(plan, settings)
		if !tripped {
			break
		}
		victim, removed, err := s.evictNext(ctx, plan, reason, settings, rules)
		if err != nil {
			s.failOrWait(&job, err, retentionKind)
			break
		}
		if !removed {
			s.jobLog(job, "warn", retentionKind, "Storage remains over the limit; every candidate is protected", map[string]any{"reason": reason, "storedBytes": plan.storedBytes, "freeBytes": plan.freeBytes})
			break
		}
		evicted++
		freedBytes += victim.status.TotalBytes
		job.UpdatedAt = time.Now().UTC()
		_ = s.repo.SaveJob(ctx, job)
		s.publish("job.updated", job)
		if plan, err = s.retentionSurvey(ctx); err != nil {
			s.failOrWait(&job, err, retentionKind)
			break
		}
	}
	s.finishRetentionJob(job, evicted, freedBytes)
}

func (s *Service) finishRetentionJob(job domain.Job, evicted int, freedBytes int64) {
	if job.State == "running" {
		if evicted > 0 {
			job.Label = fmt.Sprintf("%s · %d evicted · %.1f GiB freed", job.Label, evicted, float64(freedBytes)/(1<<30))
		} else {
			job.Label += " · within limits"
		}
		job.State = "completed"
		job.Retryable = false
		job.NextAttemptAt = nil
		job.Progress = 1
	}
	job.UpdatedAt = time.Now().UTC()
	_ = s.repo.SaveJob(context.Background(), job)
	s.publish("job.updated", job)
}

// evictionEvent names the evicted torrent for the journal and live feed. The
// names come from the cached release, the same enrichment the downloads list
// uses.
func (s *Service) evictionEvent(ctx context.Context, route retentionRoute, reason string) map[string]any {
	titles, releases := []string{}, []string{}
	seen := map[string]bool{}
	for _, row := range route.rows {
		if seen[row.ReleaseID] {
			continue
		}
		seen[row.ReleaseID] = true
		named := row
		if release, err := s.repo.GetRelease(ctx, row.ReleaseID); err == nil {
			s.enrichDownload(ctx, &named, release)
		}
		if named.DisplayTitle != "" {
			titles = append(titles, named.DisplayTitle)
		}
		if named.ReleaseName != "" {
			releases = append(releases, named.ReleaseName)
		}
	}
	return map[string]any{"reason": reason, "titles": titles, "releases": releases}
}
