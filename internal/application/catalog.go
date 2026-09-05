package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func (s *Service) CatalogTitles(ctx context.Context, q domain.CatalogQuery) (domain.Page[domain.CatalogTitle], error) {
	ids, err := s.repo.QueryCatalogTitleIDs(ctx, q)
	if err != nil {
		return domain.Page[domain.CatalogTitle]{}, err
	}
	sources, err := s.repo.ListCatalogSourcesByTitleIDs(ctx, ids.Items)
	if err != nil {
		return domain.Page[domain.CatalogTitle]{}, err
	}
	grouped := groupCatalog(sources, false)
	byID := make(map[string]domain.CatalogTitle, len(grouped))
	for i := range grouped {
		s.applyMetadataOnly(&grouped[i])
		grouped[i].LibraryState = emptyMediaState()
		byID[grouped[i].ID] = grouped[i]
	}
	if state, stateErr := s.catalogState(ctx); stateErr == nil {
		for i := range grouped {
			grouped[i].LibraryState = state.sourcesState(sourcesForTitle(sources, grouped[i].ID))
			byID[grouped[i].ID] = grouped[i]
		}
	}
	items := make([]domain.CatalogTitle, 0, len(ids.Items))
	for _, id := range ids.Items {
		if title, ok := byID[id]; ok {
			items = append(items, title)
		}
	}
	return domain.Page[domain.CatalogTitle]{Items: items, NextCursor: ids.NextCursor, Total: ids.Total}, nil
}

func (s *Service) CatalogDetail(ctx context.Context, id string) (domain.CatalogDetail, error) {
	matched, err := s.repo.ListCatalogSourcesByTitleIDs(ctx, []string{id})
	if err != nil {
		return domain.CatalogDetail{}, err
	}
	if len(matched) == 0 {
		return domain.CatalogDetail{}, fmt.Errorf("catalog title not found")
	}
	title := groupCatalog(matched, true)[0]
	s.applyCachedMetadata(&title)
	detail := domain.CatalogDetail{Title: title, Seasons: []domain.CatalogSeason{}, Sources: []domain.CatalogSource{}}
	if title.Kind == domain.MediaMovie {
		detail.Sources = matched
		s.applyCatalogState(ctx, &detail)
		return detail, nil
	}
	type episodeKey struct{ season, episode int }
	episodes := map[episodeKey][]domain.CatalogSource{}
	seasonPacks := map[int][]domain.CatalogSource{}
	for _, source := range matched {
		p := source.Parsed
		if p.EpisodeStart > 0 {
			key := episodeKey{p.SeasonStart, p.EpisodeStart}
			episodes[key] = append(episodes[key], source)
			continue
		}
		if source.Release.FileCount > 1 {
			if manifest, manifestErr := s.catalogTorrentManifest(ctx, source.Release.ID); manifestErr == nil {
				for _, file := range manifest.Files {
					if !file.Playable {
						continue
					}
					if virtual, ok := episodeSource(source, file); ok {
						key := episodeKey{virtual.Parsed.SeasonStart, virtual.Parsed.EpisodeStart}
						episodes[key] = append(episodes[key], virtual)
					}
				}
			}
		}
		start, end := p.SeasonStart, p.SeasonEnd
		if start == 0 {
			start, end = 1, 1
		}
		for season := start; season <= max(start, end); season++ {
			seasonPacks[season] = append(seasonPacks[season], source)
		}
	}
	seasonNumbers := map[int]bool{}
	for key := range episodes {
		seasonNumbers[key.season] = true
	}
	for number := range seasonPacks {
		seasonNumbers[number] = true
	}
	numbers := make([]int, 0, len(seasonNumbers))
	for number := range seasonNumbers {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	for _, number := range numbers {
		season := domain.CatalogSeason{Number: number, Title: fmt.Sprintf("Season %d", number), Episodes: []domain.CatalogEpisode{}, PackSources: seasonPacks[number]}
		keys := []episodeKey{}
		for key := range episodes {
			if key.season == number {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].episode < keys[j].episode })
		for _, key := range keys {
			items := episodes[key]
			name := items[0].Parsed.EpisodeTitle
			if name == "" {
				name = fmt.Sprintf("Episode %d", key.episode)
			}
			season.Episodes = append(season.Episodes, domain.CatalogEpisode{Number: key.episode, Season: number, Title: name, SourceCount: len(items), Sources: items})
		}
		season.EpisodeCount = len(season.Episodes)
		detail.Seasons = append(detail.Seasons, season)
	}
	s.applyCatalogState(ctx, &detail)
	return detail, nil
}

type catalogStateIndex struct {
	downloadsByRelease map[string][]domain.Download
	playbackBySource   map[string]domain.PlaybackState
	playbackByRelease  map[string][]domain.PlaybackState
}

func (s *Service) catalogState(ctx context.Context) (catalogStateIndex, error) {
	downloads, err := s.repo.ListDownloads(ctx)
	if err != nil {
		return catalogStateIndex{}, err
	}
	playback, err := s.repo.ListPlayback(ctx, householdProfile)
	if err != nil {
		return catalogStateIndex{}, err
	}
	state := catalogStateIndex{
		downloadsByRelease: map[string][]domain.Download{},
		playbackBySource:   map[string]domain.PlaybackState{},
		playbackByRelease:  map[string][]domain.PlaybackState{},
	}
	for _, item := range downloads {
		state.downloadsByRelease[item.ReleaseID] = append(state.downloadsByRelease[item.ReleaseID], item)
	}
	for _, item := range playback {
		state.playbackBySource[item.SourceID] = item
		state.playbackByRelease[item.ReleaseID] = append(state.playbackByRelease[item.ReleaseID], item)
	}
	return state, nil
}

func emptyMediaState() domain.MediaState {
	return domain.MediaState{DownloadState: "none", TransferState: "idle", WatchState: "unwatched"}
}

func (s *Service) applyCatalogState(ctx context.Context, detail *domain.CatalogDetail) {
	detail.Title.LibraryState = emptyMediaState()
	for i := range detail.Title.Sources {
		detail.Title.Sources[i].LibraryState = emptyMediaState()
	}
	for i := range detail.Sources {
		detail.Sources[i].LibraryState = emptyMediaState()
	}
	for seasonIndex := range detail.Seasons {
		season := &detail.Seasons[seasonIndex]
		season.LibraryState = emptyMediaState()
		for i := range season.PackSources {
			season.PackSources[i].LibraryState = emptyMediaState()
		}
		for episodeIndex := range season.Episodes {
			episode := &season.Episodes[episodeIndex]
			episode.LibraryState = emptyMediaState()
			for sourceIndex := range episode.Sources {
				episode.Sources[sourceIndex].LibraryState = emptyMediaState()
			}
		}
	}
	state, err := s.catalogState(ctx)
	if err != nil {
		return
	}
	for i := range detail.Title.Sources {
		detail.Title.Sources[i].LibraryState = state.sourceState(detail.Title.Sources[i])
	}
	for i := range detail.Sources {
		detail.Sources[i].LibraryState = state.sourceState(detail.Sources[i])
	}
	for seasonIndex := range detail.Seasons {
		season := &detail.Seasons[seasonIndex]
		for episodeIndex := range season.Episodes {
			episode := &season.Episodes[episodeIndex]
			for sourceIndex := range episode.Sources {
				episode.Sources[sourceIndex].LibraryState = state.sourceState(episode.Sources[sourceIndex])
			}
			episode.LibraryState = aggregateSourceState(episode.Sources)
		}
		for i := range season.PackSources {
			season.PackSources[i].LibraryState = packSourceState(season.PackSources[i].Release.ID, season.Episodes)
		}
		season.LibraryState = aggregateEpisodeState(season.Episodes)
	}
	if detail.Title.Kind == domain.MediaMovie {
		detail.Title.LibraryState = aggregateSourceState(detail.Sources)
	} else {
		detail.Title.LibraryState = aggregateSeasonState(detail.Seasons)
	}
}

func packSourceState(releaseID string, episodes []domain.CatalogEpisode) domain.MediaState {
	states := make([]domain.MediaState, 0, len(episodes))
	weights := make([]int64, 0, len(episodes))
	for _, episode := range episodes {
		for _, source := range episode.Sources {
			if source.Release.ID != releaseID {
				continue
			}
			states = append(states, source.LibraryState)
			weight := source.FileSizeBytes
			if weight <= 0 {
				weight = 1
			}
			weights = append(weights, weight)
		}
	}
	if len(states) == 0 {
		return emptyMediaState()
	}
	result := aggregateMediaStates(states, true)
	downloaded, managed, downloading, queued, failed := 0, 0, 0, 0, 0
	var weightedProgress float64
	var totalWeight int64
	for i, item := range states {
		weight := weights[i]
		totalWeight += weight
		weightedProgress += item.Progress * float64(weight)
		if item.DownloadID != "" && (result.DownloadID == "" || item.Progress >= result.Progress) {
			result.DownloadID = item.DownloadID
		}
		switch item.DownloadState {
		case "downloaded":
			downloaded++
			managed++
		case "error":
			failed++
			managed++
		case "downloading":
			downloading++
			managed++
		case "queued":
			queued++
			managed++
		case "partial":
			managed++
		}
	}
	if totalWeight > 0 {
		result.Progress = weightedProgress / float64(totalWeight)
	}
	switch {
	case downloaded == len(states):
		result.DownloadState = "downloaded"
	case failed > 0:
		result.DownloadState = "error"
	case downloading > 0:
		result.DownloadState = "downloading"
	case queued > 0:
		result.DownloadState = "queued"
	case managed > 0:
		result.DownloadState = "partial"
	default:
		result.DownloadState = "none"
	}
	return result
}

func (state catalogStateIndex) sourceState(source domain.CatalogSource) domain.MediaState {
	result := emptyMediaState()
	var selected *domain.Download
	for i := range state.downloadsByRelease[source.Release.ID] {
		download := &state.downloadsByRelease[source.Release.ID][i]
		if source.FileIndex != nil && download.FileIndex != *source.FileIndex {
			continue
		}
		if source.FileIndex == nil && source.Release.FileCount > 1 {
			continue
		}
		if selected == nil || download.Progress > selected.Progress || (download.Progress == selected.Progress && download.CreatedAt.After(selected.CreatedAt)) {
			selected = download
		}
	}
	if selected != nil {
		result.DownloadID = selected.ID
		result.Progress = selected.Progress
		// matches canonical (domain/state.go) and legacy qBittorrent state strings
		stateName := strings.ToLower(selected.State)
		switch {
		case selected.Progress >= 0.999:
			result.DownloadState = "downloaded"
			result.TransferState = "complete"
		case selected.Error != "":
			result.DownloadState = "error"
			result.TransferState = "error"
		case strings.Contains(stateName, "paused") || strings.Contains(stateName, "stopped"):
			result.DownloadState = "downloading"
			result.TransferState = "paused"
		case strings.Contains(stateName, "queued"):
			result.DownloadState = "queued"
			result.TransferState = "queued"
		default:
			result.DownloadState = "downloading"
			result.TransferState = "active"
		}
		if playback, ok := state.playbackBySource[selected.ID]; ok {
			applyPlaybackState(&result, playback)
		}
	}
	if result.WatchState == "unwatched" {
		for _, playback := range state.playbackByRelease[source.Release.ID] {
			if source.FileIndex != nil && playback.FileIndex != *source.FileIndex {
				continue
			}
			applyPlaybackState(&result, playback)
			if result.WatchState == "watched" {
				break
			}
		}
	}
	return result
}

func (state catalogStateIndex) sourcesState(sources []domain.CatalogSource) domain.MediaState {
	projected := make([]domain.CatalogSource, len(sources))
	copy(projected, sources)
	for i := range projected {
		projected[i].LibraryState = state.sourceState(projected[i])
	}
	result := aggregateSourceState(projected)
	if result.DownloadState == "none" {
		for _, source := range sources {
			for _, download := range state.downloadsByRelease[source.Release.ID] {
				result.DownloadState = "partial"
				result.DownloadID = download.ID
				result.Progress = max(result.Progress, download.Progress)
			}
		}
	}
	return result
}

func applyPlaybackState(state *domain.MediaState, playback domain.PlaybackState) {
	if playback.Watched {
		state.WatchState = "watched"
	} else if playback.PositionMS > 0 && state.WatchState != "watched" {
		state.WatchState = "inProgress"
	}
	if playback.UpdatedAt.After(time.Unix(0, 0)) && (state.PositionMS == 0 || playback.Watched) {
		state.PositionMS = playback.PositionMS
		state.DurationMS = playback.DurationMS
	}
}

func aggregateSourceState(sources []domain.CatalogSource) domain.MediaState {
	states := make([]domain.MediaState, 0, len(sources))
	for _, source := range sources {
		states = append(states, source.LibraryState)
	}
	return aggregateMediaStates(states, false)
}

func aggregateEpisodeState(episodes []domain.CatalogEpisode) domain.MediaState {
	states := make([]domain.MediaState, 0, len(episodes))
	for _, episode := range episodes {
		states = append(states, episode.LibraryState)
	}
	return aggregateMediaStates(states, true)
}

func aggregateSeasonState(seasons []domain.CatalogSeason) domain.MediaState {
	states := make([]domain.MediaState, 0, len(seasons))
	for _, season := range seasons {
		states = append(states, season.LibraryState)
	}
	return aggregateMediaStates(states, true)
}

func aggregateMediaStates(states []domain.MediaState, requireAll bool) domain.MediaState {
	result := emptyMediaState()
	if len(states) == 0 {
		return result
	}
	downloaded, managed, watched, started := 0, 0, 0, 0
	for _, state := range states {
		result.TransferState = mergeTransferState(result.TransferState, state.TransferState)
		if state.DownloadState == "downloaded" {
			downloaded++
		}
		if state.DownloadState != "none" {
			managed++
		}
		if state.WatchState == "watched" {
			watched++
		}
		if state.WatchState != "unwatched" {
			started++
		}
		if state.Progress > result.Progress {
			result.Progress, result.DownloadID = state.Progress, state.DownloadID
		}
	}
	if requireAll {
		switch {
		case downloaded == len(states):
			result.DownloadState = "downloaded"
		case managed > 0:
			result.DownloadState = "partial"
		}
		switch {
		case watched == len(states):
			result.WatchState = "watched"
		case started > 0:
			result.WatchState = "partial"
		}
		return result
	}
	if downloaded > 0 {
		result.DownloadState = "downloaded"
	} else if managed > 0 {
		result.DownloadState = "downloading"
	}
	if watched > 0 {
		result.WatchState = "watched"
	} else if started > 0 {
		result.WatchState = "inProgress"
	}
	return result
}

func mergeTransferState(current, next string) string {
	priority := map[string]int{"idle": 0, "complete": 1, "paused": 2, "queued": 3, "active": 4, "error": 5}
	if priority[next] > priority[current] {
		return next
	}
	return current
}

func sourcesForTitle(sources []domain.CatalogSource, titleID string) []domain.CatalogSource {
	out := make([]domain.CatalogSource, 0)
	for _, source := range sources {
		if domain.CatalogTitleID(source.Release, source.Parsed) == titleID {
			out = append(out, source)
		}
	}
	return out
}

func (s *Service) catalogTorrentManifest(ctx context.Context, releaseID string) (domain.TorrentManifest, error) {
	manifest, err := s.repo.GetTorrentManifest(ctx, releaseID)
	if err == nil || err != sql.ErrNoRows || s.engine == nil {
		return manifest, err
	}
	downloads, listErr := s.repo.ListDownloads(ctx)
	if listErr != nil {
		return domain.TorrentManifest{}, listErr
	}
	for _, download := range downloads {
		if download.ReleaseID != releaseID {
			continue
		}
		hash, ok := s.route(download.EngineID)
		if !ok {
			continue
		}
		files, filesErr := s.engine.Files(ctx, hash)
		if filesErr != nil || len(files) == 0 {
			continue
		}
		manifest = domain.TorrentManifest{ReleaseID: releaseID, Files: files, FetchedAt: time.Now().UTC()}
		if saveErr := s.repo.SaveTorrentManifest(ctx, manifest); saveErr != nil {
			return domain.TorrentManifest{}, saveErr
		}
		return manifest, nil
	}
	return domain.TorrentManifest{}, sql.ErrNoRows
}

func (s *Service) applyMetadataOnly(title *domain.CatalogTitle) {
	if metadata, err := s.repo.GetCatalogMetadata(context.Background(), title.ID); err == nil {
		applyMetadata(title, metadata)
	}
}

func (s *Service) applyCachedMetadata(title *domain.CatalogTitle) {
	metadata, err := s.repo.GetCatalogMetadata(context.Background(), title.ID)
	if err == nil {
		applyMetadata(title, metadata)
		if metadata.ExpiresAt.After(time.Now()) {
			return
		}
	} else if !errorsIsNoRows(err) {
		return
	}
	if s.metadata != nil && title.IMDbID != "" {
		s.EnsureMetadata(context.Background(), []string{title.ID})
	}
}

func applyMetadata(title *domain.CatalogTitle, metadata domain.CatalogMetadata) {
	if metadata.Title != "" {
		title.Title = metadata.Title
	}
	title.OriginalTitle, title.Overview = metadata.OriginalTitle, metadata.Overview
	title.Rating, title.RatingVotes, title.RatingProvider = metadata.Rating, metadata.RatingVotes, metadata.RatingProvider
	if metadata.PosterPath != "" {
		title.PosterURL = "/api/v1/artwork/" + title.ID + "/poster"
	}
	if metadata.BackdropPath != "" {
		title.BackdropURL = "/api/v1/artwork/" + title.ID + "/backdrop"
	}
}

func errorsIsNoRows(err error) bool { return err == sql.ErrNoRows }

func (s *Service) Artwork(ctx context.Context, titleID, kind string) (string, string, error) {
	if kind != "poster" && kind != "backdrop" {
		return "", "", fmt.Errorf("invalid artwork kind")
	}
	metadata, err := s.repo.GetCatalogMetadata(ctx, titleID)
	if err != nil {
		return "", "", err
	}
	remotePath := metadata.PosterPath
	if kind == "backdrop" {
		remotePath = metadata.BackdropPath
	}
	if remotePath == "" || s.metadata == nil {
		return "", "", fmt.Errorf("artwork is unavailable")
	}
	sum := sha256.Sum256([]byte(titleID + "\x00" + kind + "\x00" + remotePath))
	base := base64.RawURLEncoding.EncodeToString(sum[:18])
	root := s.settings.Get().ArtworkCachePath
	for _, ext := range []string{".jpg", ".png", ".webp"} {
		path := filepath.Join(root, base+ext)
		if _, statErr := os.Stat(path); statErr == nil {
			return path, mime.TypeByExtension(ext), nil
		}
	}
	body, contentType, err := s.metadata.OpenArtwork(ctx, remotePath, kind)
	if err != nil {
		return "", "", err
	}
	defer body.Close()
	ext := ".jpg"
	if strings.Contains(contentType, "png") {
		ext = ".png"
	} else if strings.Contains(contentType, "webp") {
		ext = ".webp"
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", "", err
	}
	path := filepath.Join(root, base+ext)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", "", err
	}
	written, copyErr := io.Copy(f, io.LimitReader(body, (16<<20)+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || written > 16<<20 {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return "", "", copyErr
		}
		if closeErr != nil {
			return "", "", closeErr
		}
		return "", "", fmt.Errorf("artwork exceeds 16 MiB")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	return path, contentType, nil
}

func (s *Service) CatalogFacets(ctx context.Context) (domain.CatalogFacets, error) {
	return s.repo.CatalogFacets(ctx)
}

func filterCatalogSources(items []domain.CatalogSource, q domain.CatalogQuery) []domain.CatalogSource {
	search := strings.ToLower(strings.TrimSpace(q.Search))
	out := make([]domain.CatalogSource, 0, len(items))
	for _, x := range items {
		p, r := x.Parsed, x.Release
		if r.Seeders <= 0 {
			continue
		}
		if domain.DefaultBlacklistedCategory(r.Category) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.EpisodeTitle+" "+r.Name), search) {
			continue
		}
		if q.Category != "" && !strings.EqualFold(r.Category, q.Category) {
			continue
		}
		if q.Kind != "" && p.Kind != q.Kind {
			continue
		}
		if q.Resolution != "" && !strings.EqualFold(p.Resolution, q.Resolution) {
			continue
		}
		if q.HDR != "" && !strings.EqualFold(p.HDR, q.HDR) {
			continue
		}
		if q.Quality != "" && !strings.EqualFold(p.Quality, q.Quality) {
			continue
		}
		if q.Codec != "" && !strings.EqualFold(p.VideoCodec, q.Codec) {
			continue
		}
		if r.Seeders < q.MinSeeders {
			continue
		}
		if q.Freeleech != nil && r.Freeleech != *q.Freeleech {
			continue
		}
		if q.Internal != nil && r.Internal != *q.Internal {
			continue
		}
		if q.Moderated != nil && r.Moderated != *q.Moderated {
			continue
		}
		out = append(out, x)
	}
	return out
}

func groupCatalog(items []domain.CatalogSource, includeSources bool) []domain.CatalogTitle {
	groups := map[string]*domain.CatalogTitle{}
	categories, resolutions := map[string]map[string]bool{}, map[string]map[string]bool{}
	seasons, episodes := map[string]map[int]bool{}, map[string]map[string]bool{}
	order := []string{}
	for _, x := range items {
		id := domain.CatalogTitleID(x.Release, x.Parsed)
		title := groups[id]
		if title == nil {
			title = &domain.CatalogTitle{ID: id, Title: x.Parsed.Title, Kind: x.Parsed.Kind, Year: x.Parsed.Year, IMDbID: x.Release.IMDbID, Categories: []string{}, Resolutions: []string{}, Sources: []domain.CatalogSource{}}
			groups[id] = title
			categories[id], resolutions[id], seasons[id], episodes[id] = map[string]bool{}, map[string]bool{}, map[int]bool{}, map[string]bool{}
			order = append(order, id)
		}
		title.SourceCount++
		if x.Release.SizeBytes > title.LargestSizeBytes {
			title.LargestSizeBytes = x.Release.SizeBytes
		}
		if x.Release.Seeders > title.BestSeeders {
			title.BestSeeders = x.Release.Seeders
		}
		if x.Release.UploadedAt != nil && (title.NewestUpload == nil || x.Release.UploadedAt.After(*title.NewestUpload)) {
			title.NewestUpload = x.Release.UploadedAt
		}
		categories[id][x.Release.Category] = true
		setNonEmpty(resolutions[id], x.Parsed.Resolution)
		if x.Parsed.SeasonStart > 0 {
			for n := x.Parsed.SeasonStart; n <= max(x.Parsed.SeasonStart, x.Parsed.SeasonEnd); n++ {
				seasons[id][n] = true
			}
		}
		if x.Parsed.EpisodeStart > 0 {
			episodes[id][fmt.Sprintf("%d:%d", x.Parsed.SeasonStart, x.Parsed.EpisodeStart)] = true
		}
		if includeSources {
			title.Sources = append(title.Sources, x)
		}
	}
	out := make([]domain.CatalogTitle, 0, len(order))
	for _, id := range order {
		title := groups[id]
		title.Categories, title.Resolutions = sortedKeys(categories[id]), sortedKeys(resolutions[id])
		title.SeasonCount, title.EpisodeCount = len(seasons[id]), len(episodes[id])
		out = append(out, *title)
	}
	return out
}

func sortCatalogTitles(items []domain.CatalogTitle, order string) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch order {
		case "oldest":
			if a.NewestUpload == nil {
				return true
			}
			if b.NewestUpload == nil {
				return false
			}
			return a.NewestUpload.Before(*b.NewestUpload)
		case "title", "title-asc":
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "title-desc":
			return strings.ToLower(a.Title) > strings.ToLower(b.Title)
		case "seeders":
			return a.BestSeeders > b.BestSeeders
		case "size":
			return a.LargestSizeBytes > b.LargestSizeBytes
		default:
			if a.NewestUpload == nil {
				return false
			}
			if b.NewestUpload == nil {
				return true
			}
			return a.NewestUpload.After(*b.NewestUpload)
		}
	})
}

func setNonEmpty(set map[string]bool, value string) {
	if value != "" {
		set[value] = true
	}
}
func sortedKeys(set map[string]bool) []string {
	out := []string{}
	for key, ok := range set {
		if ok && key != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
