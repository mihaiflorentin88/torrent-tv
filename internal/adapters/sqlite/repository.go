package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	_ "modernc.org/sqlite"
)

type Repository struct{ db *sql.DB }

func Open(path string) (*Repository, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	r := &Repository{db: db}
	if err := r.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}
func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) migrate(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS releases(
 id TEXT PRIMARY KEY,name TEXT NOT NULL,category TEXT NOT NULL,size_bytes INTEGER NOT NULL,imdb_id TEXT NOT NULL DEFAULT '',
 seeders INTEGER NOT NULL DEFAULT 0,leechers INTEGER NOT NULL DEFAULT 0,times_completed INTEGER NOT NULL DEFAULT 0,
 freeleech INTEGER NOT NULL DEFAULT 0,double_up INTEGER NOT NULL DEFAULT 0,internal INTEGER NOT NULL DEFAULT 0,moderated INTEGER NOT NULL DEFAULT 0,
 small_description TEXT NOT NULL DEFAULT '',uploaded_at INTEGER,file_count INTEGER NOT NULL DEFAULT 0,comments INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS releases_browse ON releases(uploaded_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS releases_category ON releases(category,uploaded_at DESC);
CREATE TABLE IF NOT EXISTS catalog_releases(
 release_id TEXT PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,title_id TEXT NOT NULL,title TEXT NOT NULL,sort_title TEXT NOT NULL,
 media_kind TEXT NOT NULL,year INTEGER NOT NULL DEFAULT 0,season_start INTEGER NOT NULL DEFAULT 0,season_end INTEGER NOT NULL DEFAULT 0,
 episode_start INTEGER NOT NULL DEFAULT 0,episode_end INTEGER NOT NULL DEFAULT 0,episode_title TEXT NOT NULL DEFAULT '',
 resolution TEXT NOT NULL DEFAULT '',source TEXT NOT NULL DEFAULT '',video_codec TEXT NOT NULL DEFAULT '',audio TEXT NOT NULL DEFAULT '',
 hdr TEXT NOT NULL DEFAULT '',edition TEXT NOT NULL DEFAULT '',release_group TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS catalog_releases_title ON catalog_releases(title_id);
CREATE INDEX IF NOT EXISTS catalog_releases_browse ON catalog_releases(media_kind,sort_title,year);
CREATE TABLE IF NOT EXISTS catalog_metadata(
 title_id TEXT PRIMARY KEY,provider TEXT NOT NULL DEFAULT '',provider_id TEXT NOT NULL DEFAULT '',title TEXT NOT NULL DEFAULT '',
 original_title TEXT NOT NULL DEFAULT '',overview TEXT NOT NULL DEFAULT '',poster_path TEXT NOT NULL DEFAULT '',backdrop_path TEXT NOT NULL DEFAULT '',
 language TEXT NOT NULL DEFAULT '',rating REAL NOT NULL DEFAULT 0,rating_votes INTEGER NOT NULL DEFAULT 0,rating_provider TEXT NOT NULL DEFAULT '',
 fetched_at INTEGER NOT NULL DEFAULT 0,expires_at INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS sync_state(name TEXT PRIMARY KEY,last_success INTEGER,item_count INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS downloads(
 id TEXT PRIMARY KEY,release_id TEXT NOT NULL,engine_id TEXT NOT NULL,file_index INTEGER NOT NULL,file_path TEXT NOT NULL,absolute_path TEXT NOT NULL,
 size_bytes INTEGER NOT NULL,file_offset INTEGER NOT NULL,piece_size INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL,progress REAL NOT NULL DEFAULT 0,
 downloaded_bytes INTEGER NOT NULL DEFAULT 0,speed_bytes_per_second INTEGER NOT NULL DEFAULT 0,eta_seconds INTEGER NOT NULL DEFAULT 0,peers INTEGER NOT NULL DEFAULT 0,seeds INTEGER NOT NULL DEFAULT 0,
 buffered_bytes INTEGER NOT NULL DEFAULT 0,leased INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,kind TEXT NOT NULL,state TEXT NOT NULL,payload TEXT NOT NULL,dedupe_key TEXT NOT NULL UNIQUE,
 priority INTEGER NOT NULL DEFAULT 0,attempt INTEGER NOT NULL DEFAULT 0,lease_owner TEXT,lease_expires INTEGER,progress REAL NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',retryable INTEGER NOT NULL DEFAULT 0,next_attempt_at INTEGER,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS job_logs(id INTEGER PRIMARY KEY AUTOINCREMENT,job_id TEXT NOT NULL,attempt INTEGER NOT NULL DEFAULT 0,
 level TEXT NOT NULL,phase TEXT NOT NULL,message TEXT NOT NULL,context_json TEXT NOT NULL DEFAULT '{}',created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS event_journal(id INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,payload TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS playback_state(profile_id TEXT NOT NULL DEFAULT 'household',source_id TEXT NOT NULL,release_id TEXT NOT NULL DEFAULT '',
 file_index INTEGER NOT NULL DEFAULT -1,file_path TEXT NOT NULL DEFAULT '',position_ms INTEGER NOT NULL DEFAULT 0,
 duration_ms INTEGER NOT NULL DEFAULT 0,watched INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL,PRIMARY KEY(profile_id,source_id));
CREATE TABLE IF NOT EXISTS favorites(profile_id TEXT NOT NULL DEFAULT 'household',title_id TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(profile_id,title_id));`)
	if err == nil {
		_, err = r.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS torrent_manifests(
 release_id TEXT PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,files_json TEXT NOT NULL,fetched_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_lookup ON jobs(state,kind,updated_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS subtitle_assets(
 id TEXT PRIMARY KEY,source_id TEXT NOT NULL,provider TEXT NOT NULL,candidate_id TEXT NOT NULL,name TEXT NOT NULL DEFAULT '',language TEXT NOT NULL DEFAULT '',
 format TEXT NOT NULL,mime_type TEXT NOT NULL,path TEXT NOT NULL,created_at INTEGER NOT NULL,last_used_at INTEGER NOT NULL,
 UNIQUE(source_id,provider,candidate_id,format));
CREATE INDEX IF NOT EXISTS subtitle_assets_source ON subtitle_assets(source_id,last_used_at DESC);
CREATE TABLE IF NOT EXISTS playback_preferences(
 profile_id TEXT NOT NULL DEFAULT 'household',source_id TEXT NOT NULL,audio_language TEXT NOT NULL DEFAULT 'en',audio_track_index INTEGER NOT NULL DEFAULT -1,
 subtitle_language TEXT NOT NULL DEFAULT 'ro',subtitle_provider TEXT NOT NULL DEFAULT '',subtitle_candidate_id TEXT NOT NULL DEFAULT '',
 subtitle_mode TEXT NOT NULL DEFAULT 'auto',updated_at INTEGER NOT NULL,PRIMARY KEY(profile_id,source_id));`)
	}
	if err != nil {
		return err
	}
	for _, q := range []string{
		"ALTER TABLE downloads ADD COLUMN downloaded_bytes INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE downloads ADD COLUMN speed_bytes_per_second INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE downloads ADD COLUMN eta_seconds INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE downloads ADD COLUMN peers INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE downloads ADD COLUMN seeds INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE playback_state ADD COLUMN release_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE playback_state ADD COLUMN file_index INTEGER NOT NULL DEFAULT -1",
		"ALTER TABLE playback_state ADD COLUMN file_path TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE jobs ADD COLUMN retryable INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE jobs ADD COLUMN next_attempt_at INTEGER",
		"ALTER TABLE catalog_metadata ADD COLUMN rating REAL NOT NULL DEFAULT 0",
		"ALTER TABLE catalog_metadata ADD COLUMN rating_votes INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE catalog_metadata ADD COLUMN rating_provider TEXT NOT NULL DEFAULT ''",
	} {
		if _, e := r.db.ExecContext(ctx, q); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column") {
			return e
		}
	}
	_, err = r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS playback_updated ON playback_state(profile_id,updated_at DESC);
CREATE INDEX IF NOT EXISTS playback_watched ON playback_state(profile_id,watched,updated_at DESC);
CREATE INDEX IF NOT EXISTS favorites_created ON favorites(profile_id,created_at DESC);
CREATE INDEX IF NOT EXISTS job_logs_job ON job_logs(job_id,id DESC);
CREATE INDEX IF NOT EXISTS job_logs_created ON job_logs(created_at);`)
	if err != nil {
		return err
	}
	return r.backfillCatalog(ctx)
}

func (r *Repository) backfillCatalog(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT r.id,r.name,r.category,r.size_bytes,r.imdb_id,r.seeders,r.leechers,r.times_completed,r.freeleech,r.double_up,r.internal,r.moderated,r.small_description,r.uploaded_at,r.file_count,r.comments
FROM releases r LEFT JOIN catalog_releases c ON c.release_id=r.id WHERE c.release_id IS NULL`)
	if err != nil {
		return err
	}
	items := []domain.TorrentRelease{}
	for rows.Next() {
		x, scanErr := scanRelease(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(items) > 0 {
		if err := r.UpsertReleases(ctx, items); err != nil {
			return err
		}
	}
	_, err = r.db.ExecContext(ctx, `UPDATE favorites SET title_id=(SELECT c.title_id FROM catalog_releases c WHERE c.release_id=favorites.title_id)
WHERE EXISTS(SELECT 1 FROM catalog_releases c WHERE c.release_id=favorites.title_id)`)
	return err
}

func (r *Repository) UpsertReleases(ctx context.Context, items []domain.TorrentRelease) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `INSERT INTO releases(id,name,category,size_bytes,imdb_id,seeders,leechers,times_completed,freeleech,double_up,internal,moderated,small_description,uploaded_at,file_count,comments,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,category=excluded.category,size_bytes=excluded.size_bytes,imdb_id=excluded.imdb_id,
seeders=excluded.seeders,leechers=excluded.leechers,times_completed=excluded.times_completed,freeleech=excluded.freeleech,double_up=excluded.double_up,internal=excluded.internal,
moderated=excluded.moderated,small_description=excluded.small_description,uploaded_at=excluded.uploaded_at,file_count=excluded.file_count,comments=excluded.comments,updated_at=excluded.updated_at`
	for _, x := range items {
		var uploaded any
		if x.UploadedAt != nil {
			uploaded = x.UploadedAt.Unix()
		}
		if _, err = tx.ExecContext(ctx, q, x.ID, x.Name, x.Category, x.SizeBytes, x.IMDbID, x.Seeders, x.Leechers, x.TimesCompleted, x.Freeleech, x.DoubleUp, x.Internal, x.Moderated, x.SmallDescription, uploaded, x.FileCount, x.Comments, time.Now().Unix()); err != nil {
			return err
		}
		parsed := domain.ParseRelease(x)
		if _, err = tx.ExecContext(ctx, `INSERT INTO catalog_releases(release_id,title_id,title,sort_title,media_kind,year,season_start,season_end,episode_start,episode_end,episode_title,resolution,source,video_codec,audio,hdr,edition,release_group)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(release_id) DO UPDATE SET title_id=excluded.title_id,title=excluded.title,sort_title=excluded.sort_title,
media_kind=excluded.media_kind,year=excluded.year,season_start=excluded.season_start,season_end=excluded.season_end,episode_start=excluded.episode_start,
episode_end=excluded.episode_end,episode_title=excluded.episode_title,resolution=excluded.resolution,source=excluded.source,video_codec=excluded.video_codec,
audio=excluded.audio,hdr=excluded.hdr,edition=excluded.edition,release_group=excluded.release_group`,
			x.ID, domain.CatalogTitleID(x, parsed), parsed.Title, parsed.SortTitle, parsed.Kind, parsed.Year, parsed.SeasonStart, parsed.SeasonEnd,
			parsed.EpisodeStart, parsed.EpisodeEnd, parsed.EpisodeTitle, parsed.Resolution, parsed.Quality, parsed.VideoCodec, parsed.Audio, parsed.HDR, parsed.Edition, parsed.ReleaseGroup); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) ListCatalogSources(ctx context.Context) ([]domain.CatalogSource, error) {
	rows, err := r.db.QueryContext(ctx, catalogSourceSelect+` ORDER BY COALESCE(r.uploaded_at,0) DESC,r.id DESC`)
	if err != nil {
		return nil, err
	}
	return scanCatalogSources(rows)
}

const catalogSourceSelect = `SELECT r.id,r.name,r.category,r.size_bytes,r.imdb_id,r.seeders,r.leechers,r.times_completed,r.freeleech,r.double_up,r.internal,r.moderated,r.small_description,r.uploaded_at,r.file_count,r.comments,
c.title,c.sort_title,c.media_kind,c.year,c.season_start,c.season_end,c.episode_start,c.episode_end,c.episode_title,c.resolution,c.source,c.video_codec,c.audio,c.hdr,c.edition,c.release_group
FROM releases r JOIN catalog_releases c ON c.release_id=r.id`

func scanCatalogSources(rows *sql.Rows) ([]domain.CatalogSource, error) {
	defer rows.Close()
	out := []domain.CatalogSource{}
	for rows.Next() {
		var x domain.CatalogSource
		var uploaded sql.NullInt64
		if err := rows.Scan(&x.Release.ID, &x.Release.Name, &x.Release.Category, &x.Release.SizeBytes, &x.Release.IMDbID, &x.Release.Seeders, &x.Release.Leechers,
			&x.Release.TimesCompleted, &x.Release.Freeleech, &x.Release.DoubleUp, &x.Release.Internal, &x.Release.Moderated, &x.Release.SmallDescription, &uploaded,
			&x.Release.FileCount, &x.Release.Comments, &x.Parsed.Title, &x.Parsed.SortTitle, &x.Parsed.Kind, &x.Parsed.Year, &x.Parsed.SeasonStart,
			&x.Parsed.SeasonEnd, &x.Parsed.EpisodeStart, &x.Parsed.EpisodeEnd, &x.Parsed.EpisodeTitle, &x.Parsed.Resolution, &x.Parsed.Quality,
			&x.Parsed.VideoCodec, &x.Parsed.Audio, &x.Parsed.HDR, &x.Parsed.Edition, &x.Parsed.ReleaseGroup); err != nil {
			return nil, err
		}
		if uploaded.Valid {
			t := time.Unix(uploaded.Int64, 0).UTC()
			x.Release.UploadedAt = &t
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func catalogFilter(q domain.CatalogQuery) (string, []any) {
	where := []string{"r.seeders>0"}
	args := []any{}
	blacklisted := []string{}
	for _, category := range domain.Categories {
		if category.DefaultBlacklisted {
			blacklisted = append(blacklisted, category.Name)
		}
	}
	if len(blacklisted) > 0 {
		where = append(where, "r.category NOT IN ("+strings.TrimRight(strings.Repeat("?,", len(blacklisted)), ",")+")")
		for _, value := range blacklisted {
			args = append(args, value)
		}
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		where = append(where, "(c.title LIKE ? ESCAPE '\\' OR c.episode_title LIKE ? ESCAPE '\\' OR r.name LIKE ? ESCAPE '\\')")
		like := "%" + escapeLike(search) + "%"
		args = append(args, like, like, like)
	}
	for value, column := range map[string]string{q.Category: "r.category", string(q.Kind): "c.media_kind", q.Resolution: "c.resolution", q.HDR: "c.hdr", q.Quality: "c.source", q.Codec: "c.video_codec"} {
		if value != "" {
			where = append(where, column+"=?")
			args = append(args, value)
		}
	}
	if q.MinSeeders > 0 {
		where = append(where, "r.seeders>=?")
		args = append(args, q.MinSeeders)
	}
	for value, column := range map[*bool]string{q.Freeleech: "r.freeleech", q.Internal: "r.internal", q.Moderated: "r.moderated"} {
		if value != nil {
			where = append(where, column+"=?")
			args = append(args, *value)
		}
	}
	return strings.Join(where, " AND "), args
}

func (r *Repository) QueryCatalogTitleIDs(ctx context.Context, q domain.CatalogQuery) (domain.Page[string], error) {
	if q.Limit < 1 || q.Limit > 100 {
		q.Limit = 24
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	where, args := catalogFilter(q)
	grouped := ` FROM releases r JOIN catalog_releases c ON c.release_id=r.id LEFT JOIN catalog_metadata m ON m.title_id=c.title_id WHERE ` + where + ` GROUP BY c.title_id`
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT c.title_id`+grouped+`)`, args...).Scan(&total); err != nil {
		return domain.Page[string]{}, err
	}
	order := "MAX(COALESCE(r.uploaded_at,0)) DESC,c.title_id DESC"
	switch q.Sort {
	case "oldest":
		order = "MAX(COALESCE(r.uploaded_at,0)) ASC,c.title_id"
	case "title", "title-asc":
		order = "MIN(c.sort_title) ASC,c.title_id"
	case "title-desc":
		order = "MIN(c.sort_title) DESC,c.title_id DESC"
	case "seeders":
		order = "MAX(r.seeders) DESC,c.title_id"
	case "size":
		order = "MAX(r.size_bytes) DESC,c.title_id"
	case "rating", "rating-desc":
		order = "CASE WHEN MAX(COALESCE(m.rating,0))>0 THEN 0 ELSE 1 END,MAX(COALESCE(m.rating,0)) DESC,c.title_id"
	case "rating-asc":
		order = "CASE WHEN MAX(COALESCE(m.rating,0))>0 THEN 0 ELSE 1 END,MAX(COALESCE(m.rating,0)) ASC,c.title_id"
	}
	queryArgs := append(append([]any{}, args...), q.Limit, q.Offset)
	rows, err := r.db.QueryContext(ctx, `SELECT c.title_id`+grouped+` ORDER BY `+order+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return domain.Page[string]{}, err
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return domain.Page[string]{}, err
		}
		items = append(items, id)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[string]{}, err
	}
	var next *string
	if q.Offset+len(items) < total {
		value := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(q.Offset + len(items))))
		next = &value
	}
	return domain.Page[string]{Items: items, NextCursor: next, Total: total}, nil
}

func (r *Repository) ListCatalogSourcesByTitleIDs(ctx context.Context, ids []string) ([]domain.CatalogSource, error) {
	if len(ids) == 0 {
		return []domain.CatalogSource{}, nil
	}
	args := make([]any, len(ids))
	for i := range ids {
		args[i] = ids[i]
	}
	rows, err := r.db.QueryContext(ctx, catalogSourceSelect+` WHERE c.title_id IN (`+strings.TrimRight(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY COALESCE(r.uploaded_at,0) DESC,r.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	return scanCatalogSources(rows)
}

func (r *Repository) CatalogTitleIDsForReleases(ctx context.Context, releaseIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(releaseIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(releaseIDs))
	for i := range releaseIDs {
		args[i] = releaseIDs[i]
	}
	rows, err := r.db.QueryContext(ctx, `SELECT release_id,title_id FROM catalog_releases WHERE release_id IN (`+strings.TrimRight(strings.Repeat("?,", len(releaseIDs)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var releaseID, titleID string
		if err := rows.Scan(&releaseID, &titleID); err != nil {
			return nil, err
		}
		out[releaseID] = titleID
	}
	return out, rows.Err()
}

func (r *Repository) CatalogFacets(ctx context.Context) (domain.CatalogFacets, error) {
	blacklisted := []string{}
	for _, category := range domain.Categories {
		if category.DefaultBlacklisted {
			blacklisted = append(blacklisted, category.Name)
		}
	}
	where, args := "r.seeders>0", []any{}
	if len(blacklisted) > 0 {
		where += " AND r.category NOT IN (" + strings.TrimRight(strings.Repeat("?,", len(blacklisted)), ",") + ")"
		for _, v := range blacklisted {
			args = append(args, v)
		}
	}
	read := func(column string) ([]string, error) {
		rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT `+column+` FROM releases r JOIN catalog_releases c ON c.release_id=r.id WHERE `+where+` AND `+column+`<>'' ORDER BY `+column, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, rows.Err()
	}
	var f domain.CatalogFacets
	var err error
	if f.Categories, err = read("r.category"); err != nil {
		return f, err
	}
	if f.Kinds, err = read("c.media_kind"); err != nil {
		return f, err
	}
	if f.Resolutions, err = read("c.resolution"); err != nil {
		return f, err
	}
	if f.HDR, err = read("c.hdr"); err != nil {
		return f, err
	}
	if f.Qualities, err = read("c.source"); err != nil {
		return f, err
	}
	f.Codecs, err = read("c.video_codec")
	return f, err
}

func (r *Repository) SaveCatalogMetadata(ctx context.Context, m domain.CatalogMetadata) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO catalog_metadata(title_id,provider,provider_id,title,original_title,overview,poster_path,backdrop_path,language,rating,rating_votes,rating_provider,fetched_at,expires_at,last_error)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(title_id) DO UPDATE SET provider=excluded.provider,provider_id=excluded.provider_id,title=excluded.title,
original_title=excluded.original_title,overview=excluded.overview,poster_path=excluded.poster_path,backdrop_path=excluded.backdrop_path,language=excluded.language,
rating=excluded.rating,rating_votes=excluded.rating_votes,rating_provider=excluded.rating_provider,fetched_at=excluded.fetched_at,expires_at=excluded.expires_at,last_error=excluded.last_error`, m.TitleID, m.Provider, m.ProviderID, m.Title, m.OriginalTitle,
		m.Overview, m.PosterPath, m.BackdropPath, m.Language, m.Rating, m.RatingVotes, m.RatingProvider, m.FetchedAt.Unix(), m.ExpiresAt.Unix(), m.LastError)
	return err
}

func (r *Repository) GetCatalogMetadata(ctx context.Context, titleID string) (domain.CatalogMetadata, error) {
	var m domain.CatalogMetadata
	var fetched, expires int64
	err := r.db.QueryRowContext(ctx, `SELECT title_id,provider,provider_id,title,original_title,overview,poster_path,backdrop_path,language,rating,rating_votes,rating_provider,fetched_at,expires_at,last_error FROM catalog_metadata WHERE title_id=?`, titleID).
		Scan(&m.TitleID, &m.Provider, &m.ProviderID, &m.Title, &m.OriginalTitle, &m.Overview, &m.PosterPath, &m.BackdropPath, &m.Language, &m.Rating, &m.RatingVotes, &m.RatingProvider, &fetched, &expires, &m.LastError)
	m.FetchedAt, m.ExpiresAt = time.Unix(fetched, 0).UTC(), time.Unix(expires, 0).UTC()
	return m, err
}

func (r *Repository) ListReleases(ctx context.Context, search, category string, limit, offset int) (domain.Page[domain.TorrentRelease], error) {
	if limit < 1 || limit > 100 {
		limit = 24
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if search != "" {
		where = append(where, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(search)+"%")
	}
	if category != "" {
		where = append(where, "category=?")
		args = append(args, category)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM releases WHERE "+w, args...).Scan(&total); err != nil {
		return domain.Page[domain.TorrentRelease]{}, err
	}
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,category,size_bytes,imdb_id,seeders,leechers,times_completed,freeleech,double_up,internal,moderated,small_description,uploaded_at,file_count,comments FROM releases WHERE `+w+` ORDER BY COALESCE(uploaded_at,0) DESC,id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return domain.Page[domain.TorrentRelease]{}, err
	}
	defer rows.Close()
	items := []domain.TorrentRelease{}
	for rows.Next() {
		x, err := scanRelease(rows)
		if err != nil {
			return domain.Page[domain.TorrentRelease]{}, err
		}
		items = append(items, x)
	}
	var next *string
	if offset+len(items) < total {
		s := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + len(items))))
		next = &s
	}
	return domain.Page[domain.TorrentRelease]{Items: items, NextCursor: next, Total: total}, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanRelease(s scanner) (domain.TorrentRelease, error) {
	var x domain.TorrentRelease
	var uploaded sql.NullInt64
	err := s.Scan(&x.ID, &x.Name, &x.Category, &x.SizeBytes, &x.IMDbID, &x.Seeders, &x.Leechers, &x.TimesCompleted, &x.Freeleech, &x.DoubleUp, &x.Internal, &x.Moderated, &x.SmallDescription, &uploaded, &x.FileCount, &x.Comments)
	if uploaded.Valid {
		t := time.Unix(uploaded.Int64, 0).UTC()
		x.UploadedAt = &t
	}
	return x, err
}

func (r *Repository) GetRelease(ctx context.Context, id string) (domain.TorrentRelease, error) {
	return scanRelease(r.db.QueryRowContext(ctx, `SELECT id,name,category,size_bytes,imdb_id,seeders,leechers,times_completed,freeleech,double_up,internal,moderated,small_description,uploaded_at,file_count,comments FROM releases WHERE id=?`, id))
}

func (r *Repository) SyncAge(ctx context.Context, name string) (int64, error) {
	var ts sql.NullInt64
	err := r.db.QueryRowContext(ctx, "SELECT last_success FROM sync_state WHERE name=?", name).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) || !ts.Valid {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	return time.Now().Unix() - ts.Int64, nil
}

func (r *Repository) RecordSync(ctx context.Context, name string, count int, syncErr error) error {
	var ts any
	msg := ""
	if syncErr == nil {
		ts = time.Now().Unix()
	} else {
		msg = syncErr.Error()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO sync_state(name,last_success,item_count,last_error) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET last_success=COALESCE(excluded.last_success,sync_state.last_success),item_count=excluded.item_count,last_error=excluded.last_error`, name, ts, count, msg)
	return err
}

func (r *Repository) SaveDownload(ctx context.Context, d domain.Download) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO downloads(id,release_id,engine_id,file_index,file_path,absolute_path,size_bytes,file_offset,piece_size,state,progress,downloaded_bytes,speed_bytes_per_second,eta_seconds,peers,seeds,buffered_bytes,leased,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET engine_id=excluded.engine_id,absolute_path=excluded.absolute_path,piece_size=excluded.piece_size,state=excluded.state,progress=excluded.progress,downloaded_bytes=excluded.downloaded_bytes,speed_bytes_per_second=excluded.speed_bytes_per_second,eta_seconds=excluded.eta_seconds,peers=excluded.peers,seeds=excluded.seeds,buffered_bytes=excluded.buffered_bytes,leased=excluded.leased,error=excluded.error,updated_at=excluded.updated_at`, d.ID, d.ReleaseID, d.EngineID, d.FileIndex, d.FilePath, d.AbsolutePath, d.SizeBytes, d.FileOffset, d.PieceSize, d.State, d.Progress, d.DownloadedBytes, d.SpeedBytesPerSecond, d.ETASeconds, d.Peers, d.Seeds, d.BufferedBytes, d.Leased, d.Error, d.CreatedAt.Unix(), d.UpdatedAt.Unix())
	return err
}

func (r *Repository) DeleteDownload(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM downloads WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RemoveRelease drops a release whose torrent the tracker no longer hosts.
// catalog_releases rows cascade with it, so the dead version disappears from
// every title that offered it; download history keeps its release_id.
func (r *Repository) RemoveRelease(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM releases WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) GetDownload(ctx context.Context, id string) (domain.Download, error) {
	return scanDownload(r.db.QueryRowContext(ctx, `SELECT id,release_id,engine_id,file_index,file_path,absolute_path,size_bytes,file_offset,piece_size,state,progress,downloaded_bytes,speed_bytes_per_second,eta_seconds,peers,seeds,buffered_bytes,leased,error,created_at,updated_at FROM downloads WHERE id=?`, id))
}

func (r *Repository) FindDownload(ctx context.Context, releaseID string, fileIndex int) (domain.Download, error) {
	query := `SELECT id,release_id,engine_id,file_index,file_path,absolute_path,size_bytes,file_offset,piece_size,state,progress,downloaded_bytes,speed_bytes_per_second,eta_seconds,peers,seeds,buffered_bytes,leased,error,created_at,updated_at FROM downloads WHERE release_id=?`
	args := []any{releaseID}
	if fileIndex >= 0 {
		query += ` AND file_index=?`
		args = append(args, fileIndex)
	}
	query += ` ORDER BY CASE WHEN progress>=1 THEN 0 ELSE 1 END,updated_at DESC LIMIT 1`
	return scanDownload(r.db.QueryRowContext(ctx, query, args...))
}

func (r *Repository) ListDownloads(ctx context.Context) ([]domain.Download, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,release_id,engine_id,file_index,file_path,absolute_path,size_bytes,file_offset,piece_size,state,progress,downloaded_bytes,speed_bytes_per_second,eta_seconds,peers,seeds,buffered_bytes,leased,error,created_at,updated_at FROM downloads ORDER BY created_at DESC,id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Download{}
	for rows.Next() {
		x, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func scanDownload(s scanner) (domain.Download, error) {
	var d domain.Download
	var created, updated int64
	err := s.Scan(&d.ID, &d.ReleaseID, &d.EngineID, &d.FileIndex, &d.FilePath, &d.AbsolutePath, &d.SizeBytes, &d.FileOffset, &d.PieceSize, &d.State, &d.Progress, &d.DownloadedBytes, &d.SpeedBytesPerSecond, &d.ETASeconds, &d.Peers, &d.Seeds, &d.BufferedBytes, &d.Leased, &d.Error, &created, &updated)
	d.CreatedAt = time.Unix(created, 0).UTC()
	d.UpdatedAt = time.Unix(updated, 0).UTC()
	return d, err
}

func (r *Repository) SetLease(ctx context.Context, id string, v bool) error {
	res, err := r.db.ExecContext(ctx, "UPDATE downloads SET leased=?,updated_at=? WHERE id=?", v, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) SaveJob(ctx context.Context, job domain.Job) error {
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	var next any
	if job.NextAttemptAt != nil {
		next = job.NextAttemptAt.Unix()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO jobs(id,kind,state,payload,dedupe_key,attempt,progress,error,retryable,next_attempt_at,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(dedupe_key) DO UPDATE SET state=excluded.state,payload=excluded.payload,attempt=excluded.attempt,
progress=excluded.progress,error=excluded.error,retryable=excluded.retryable,next_attempt_at=excluded.next_attempt_at,updated_at=excluded.updated_at`, job.ID, job.Kind, job.State, job.Label, job.DedupeKey,
		job.Attempt, job.Progress, job.Error, job.Retryable, next, job.CreatedAt.Unix(), job.UpdatedAt.Unix())
	return err
}

func (r *Repository) ListJobs(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,kind,state,payload,dedupe_key,progress,attempt,error,retryable,next_attempt_at,created_at,updated_at
FROM jobs ORDER BY CASE state WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END,updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Job{}
	for rows.Next() {
		var job domain.Job
		var created, updated int64
		var next sql.NullInt64
		if err := rows.Scan(&job.ID, &job.Kind, &job.State, &job.Label, &job.DedupeKey, &job.Progress, &job.Attempt, &job.Error, &job.Retryable, &next, &created, &updated); err != nil {
			return nil, err
		}
		job.CreatedAt, job.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		if next.Valid {
			value := time.Unix(next.Int64, 0).UTC()
			job.NextAttemptAt = &value
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (r *Repository) ListDueJobs(ctx context.Context, before time.Time, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 1000 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,kind,state,payload,dedupe_key,progress,attempt,error,retryable,next_attempt_at,created_at,updated_at
FROM jobs WHERE next_attempt_at IS NOT NULL AND next_attempt_at<=? AND (state='retry_wait' OR (state='failed' AND retryable=1))
ORDER BY next_attempt_at,id LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Job{}
	for rows.Next() {
		var job domain.Job
		var created, updated int64
		var next sql.NullInt64
		if err := rows.Scan(&job.ID, &job.Kind, &job.State, &job.Label, &job.DedupeKey, &job.Progress, &job.Attempt, &job.Error, &job.Retryable, &next, &created, &updated); err != nil {
			return nil, err
		}
		job.CreatedAt, job.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		if next.Valid {
			value := time.Unix(next.Int64, 0).UTC()
			job.NextAttemptAt = &value
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (r *Repository) QueryJobs(ctx context.Context, search, state, kind, retryable string, updatedSince int64, limit, offset int) (domain.Page[domain.Job], error) {
	if limit < 1 || limit > 100 {
		limit = 24
	}
	if offset < 0 {
		offset = 0
	}
	where, args := []string{"1=1"}, []any{}
	if q := strings.TrimSpace(search); q != "" {
		where = append(where, "(id LIKE ? OR kind LIKE ? OR payload LIKE ? OR error LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	if state != "" {
		where = append(where, "state=?")
		args = append(args, state)
	}
	if kind != "" {
		where = append(where, "kind=?")
		args = append(args, kind)
	}
	if retryable == "true" || retryable == "false" {
		where = append(where, "retryable=?")
		args = append(args, retryable == "true")
	}
	if updatedSince > 0 {
		where = append(where, "updated_at>=?")
		args = append(args, updatedSince)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE "+clause, args...).Scan(&total); err != nil {
		return domain.Page[domain.Job]{}, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.QueryContext(ctx, `SELECT id,kind,state,payload,dedupe_key,progress,attempt,error,retryable,next_attempt_at,created_at,updated_at FROM jobs WHERE `+clause+`
ORDER BY CASE state WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END,updated_at DESC,id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return domain.Page[domain.Job]{}, err
	}
	defer rows.Close()
	items := []domain.Job{}
	for rows.Next() {
		var job domain.Job
		var created, updated int64
		var next sql.NullInt64
		if err := rows.Scan(&job.ID, &job.Kind, &job.State, &job.Label, &job.DedupeKey, &job.Progress, &job.Attempt, &job.Error, &job.Retryable, &next, &created, &updated); err != nil {
			return domain.Page[domain.Job]{}, err
		}
		job.CreatedAt, job.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		if next.Valid {
			value := time.Unix(next.Int64, 0).UTC()
			job.NextAttemptAt = &value
		}
		items = append(items, job)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[domain.Job]{}, err
	}
	var next *string
	if offset+len(items) < total {
		value := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + len(items))))
		next = &value
	}
	return domain.Page[domain.Job]{Items: items, NextCursor: next, Total: total}, nil
}

func (r *Repository) GetJob(ctx context.Context, id string) (domain.Job, error) {
	var job domain.Job
	var created, updated int64
	var next sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT id,kind,state,payload,dedupe_key,progress,attempt,error,retryable,next_attempt_at,created_at,updated_at FROM jobs WHERE id=?`, id).
		Scan(&job.ID, &job.Kind, &job.State, &job.Label, &job.DedupeKey, &job.Progress, &job.Attempt, &job.Error, &job.Retryable, &next, &created, &updated)
	if err == nil {
		job.CreatedAt, job.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		if next.Valid {
			value := time.Unix(next.Int64, 0).UTC()
			job.NextAttemptAt = &value
		}
	}
	return job, err
}

func (r *Repository) AppendJobLog(ctx context.Context, entry domain.JobLog) (domain.JobLog, error) {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.Level == "" {
		entry.Level = "info"
	}
	if entry.Phase == "" {
		entry.Phase = "job"
	}
	b, err := json.Marshal(entry.Context)
	if err != nil {
		return domain.JobLog{}, err
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO job_logs(job_id,attempt,level,phase,message,context_json,created_at) VALUES(?,?,?,?,?,?,?)`, entry.JobID, entry.Attempt, entry.Level, entry.Phase, entry.Message, string(b), entry.CreatedAt.Unix())
	if err != nil {
		return domain.JobLog{}, err
	}
	entry.ID, err = result.LastInsertId()
	if err == nil {
		_, _ = r.db.ExecContext(ctx, `DELETE FROM job_logs WHERE job_id=? AND id NOT IN (SELECT id FROM job_logs WHERE job_id=? ORDER BY id DESC LIMIT 500)`, entry.JobID, entry.JobID)
	}
	return entry, err
}

func (r *Repository) ListJobLogs(ctx context.Context, jobID string, before int64, limit int) (domain.Page[domain.JobLog], error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	if before <= 0 {
		before = int64(^uint64(0) >> 1)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,job_id,attempt,level,phase,message,context_json,created_at FROM job_logs WHERE job_id=? AND id<? ORDER BY id DESC LIMIT ?`, jobID, before, limit+1)
	if err != nil {
		return domain.Page[domain.JobLog]{}, err
	}
	defer rows.Close()
	items := []domain.JobLog{}
	for rows.Next() {
		var x domain.JobLog
		var raw string
		var created int64
		if err := rows.Scan(&x.ID, &x.JobID, &x.Attempt, &x.Level, &x.Phase, &x.Message, &raw, &created); err != nil {
			return domain.Page[domain.JobLog]{}, err
		}
		_ = json.Unmarshal([]byte(raw), &x.Context)
		x.CreatedAt = time.Unix(created, 0).UTC()
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[domain.JobLog]{}, err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(items[len(items)-1].ID, 10)))
		next = &value
	}
	return domain.Page[domain.JobLog]{Items: items, NextCursor: next, Total: len(items)}, nil
}

func (r *Repository) PruneJobLogs(ctx context.Context, before time.Time, perJob int) error {
	if perJob < 1 {
		perJob = 500
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM job_logs WHERE created_at<?`, before.Unix())
	return err
}

func (r *Repository) SaveTorrentManifest(ctx context.Context, manifest domain.TorrentManifest) error {
	b, err := json.Marshal(manifest.Files)
	if err != nil {
		return err
	}
	if manifest.FetchedAt.IsZero() {
		manifest.FetchedAt = time.Now().UTC()
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO torrent_manifests(release_id,files_json,fetched_at) VALUES(?,?,?)
ON CONFLICT(release_id) DO UPDATE SET files_json=excluded.files_json,fetched_at=excluded.fetched_at`, manifest.ReleaseID, string(b), manifest.FetchedAt.Unix())
	return err
}

func (r *Repository) GetTorrentManifest(ctx context.Context, releaseID string) (domain.TorrentManifest, error) {
	var raw string
	var fetched int64
	err := r.db.QueryRowContext(ctx, `SELECT files_json,fetched_at FROM torrent_manifests WHERE release_id=?`, releaseID).Scan(&raw, &fetched)
	manifest := domain.TorrentManifest{ReleaseID: releaseID, FetchedAt: time.Unix(fetched, 0).UTC()}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &manifest.Files)
	}
	return manifest, err
}

func (r *Repository) CatalogCounts(ctx context.Context) (int, int, error) {
	var total, discoverable int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN seeders>0 THEN 1 ELSE 0 END),0) FROM releases`).Scan(&total, &discoverable)
	return total, discoverable, err
}

func (r *Repository) AppendEvent(ctx context.Context, kind, payload string) (domain.Event, error) {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, "INSERT INTO event_journal(kind,payload,created_at) VALUES(?,?,?)", kind, payload, now.Unix())
	if err != nil {
		return domain.Event{}, err
	}
	id, err := result.LastInsertId()
	return domain.Event{ID: id, Kind: kind, Payload: payload, CreatedAt: now}, err
}

func (r *Repository) ListEvents(ctx context.Context, after int64, limit int) ([]domain.Event, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id,kind,payload,created_at FROM event_journal WHERE id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var event domain.Event
		var created int64
		if err := rows.Scan(&event.ID, &event.Kind, &event.Payload, &created); err != nil {
			return nil, err
		}
		event.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, event)
	}
	return out, rows.Err()
}

func (r *Repository) SavePlayback(ctx context.Context, p domain.PlaybackState) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO playback_state(profile_id,source_id,release_id,file_index,file_path,position_ms,duration_ms,watched,updated_at)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(profile_id,source_id) DO UPDATE SET release_id=excluded.release_id,file_index=excluded.file_index,file_path=excluded.file_path,
position_ms=excluded.position_ms,duration_ms=excluded.duration_ms,watched=excluded.watched,updated_at=excluded.updated_at`,
		p.ProfileID, p.SourceID, p.ReleaseID, p.FileIndex, p.FilePath, p.PositionMS, p.DurationMS, p.Watched, p.UpdatedAt.Unix())
	return err
}

func (r *Repository) GetPlayback(ctx context.Context, profileID, sourceID string) (domain.PlaybackState, error) {
	return scanPlayback(r.db.QueryRowContext(ctx, `SELECT profile_id,source_id,release_id,file_index,file_path,position_ms,duration_ms,watched,updated_at FROM playback_state WHERE profile_id=? AND source_id=?`, profileID, sourceID))
}

func (r *Repository) ListPlayback(ctx context.Context, profileID string) ([]domain.PlaybackState, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT profile_id,source_id,release_id,file_index,file_path,position_ms,duration_ms,watched,updated_at FROM playback_state WHERE profile_id=? ORDER BY updated_at DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PlaybackState{}
	for rows.Next() {
		p, e := scanPlayback(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPlayback(s scanner) (domain.PlaybackState, error) {
	var p domain.PlaybackState
	var updated int64
	err := s.Scan(&p.ProfileID, &p.SourceID, &p.ReleaseID, &p.FileIndex, &p.FilePath, &p.PositionMS, &p.DurationMS, &p.Watched, &updated)
	p.UpdatedAt = time.Unix(updated, 0).UTC()
	return p, err
}

func (r *Repository) SavePlaybackPreferences(ctx context.Context, p domain.PlaybackPreferences) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO playback_preferences(profile_id,source_id,audio_language,audio_track_index,subtitle_language,subtitle_provider,subtitle_candidate_id,subtitle_mode,updated_at)
VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(profile_id,source_id) DO UPDATE SET audio_language=excluded.audio_language,audio_track_index=excluded.audio_track_index,
subtitle_language=excluded.subtitle_language,subtitle_provider=excluded.subtitle_provider,subtitle_candidate_id=excluded.subtitle_candidate_id,subtitle_mode=excluded.subtitle_mode,updated_at=excluded.updated_at`,
		p.ProfileID, p.SourceID, p.AudioLanguage, p.AudioTrackIndex, p.SubtitleLanguage, p.SubtitleProvider, p.SubtitleCandidateID, p.SubtitleMode, p.UpdatedAt.Unix())
	return err
}

func (r *Repository) GetPlaybackPreferences(ctx context.Context, profileID, sourceID string) (domain.PlaybackPreferences, error) {
	var p domain.PlaybackPreferences
	var updated int64
	err := r.db.QueryRowContext(ctx, `SELECT profile_id,source_id,audio_language,audio_track_index,subtitle_language,subtitle_provider,subtitle_candidate_id,subtitle_mode,updated_at
FROM playback_preferences WHERE profile_id=? AND source_id=?`, profileID, sourceID).Scan(&p.ProfileID, &p.SourceID, &p.AudioLanguage, &p.AudioTrackIndex, &p.SubtitleLanguage, &p.SubtitleProvider, &p.SubtitleCandidateID, &p.SubtitleMode, &updated)
	p.UpdatedAt = time.Unix(updated, 0).UTC()
	return p, err
}

func (r *Repository) SetFavorite(ctx context.Context, profileID, releaseID string, favorite bool) error {
	if favorite {
		_, err := r.db.ExecContext(ctx, `INSERT INTO favorites(profile_id,title_id,created_at) VALUES(?,?,?) ON CONFLICT(profile_id,title_id) DO NOTHING`, profileID, releaseID, time.Now().Unix())
		return err
	}
	_, err := r.db.ExecContext(ctx, "DELETE FROM favorites WHERE profile_id=? AND title_id=?", profileID, releaseID)
	return err
}

func (r *Repository) ListFavorites(ctx context.Context, profileID string) ([]domain.Favorite, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT profile_id,title_id,created_at FROM favorites WHERE profile_id=? ORDER BY created_at DESC", profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Favorite{}
	for rows.Next() {
		var f domain.Favorite
		var created int64
		if err := rows.Scan(&f.ProfileID, &f.TitleID, &created); err != nil {
			return nil, err
		}
		f.ReleaseID = f.TitleID
		f.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *Repository) SaveSubtitleAsset(ctx context.Context, a domain.SubtitleAsset) error {
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.LastUsedAt.IsZero() {
		a.LastUsedAt = now
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO subtitle_assets(id,source_id,provider,candidate_id,name,language,format,mime_type,path,created_at,last_used_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_id,provider,candidate_id,format) DO UPDATE SET
id=excluded.id,name=excluded.name,language=excluded.language,mime_type=excluded.mime_type,path=excluded.path,last_used_at=excluded.last_used_at`,
		a.ID, a.SourceID, a.Provider, a.CandidateID, a.Name, a.Language, a.Format, a.MimeType, a.Path, a.CreatedAt.Unix(), a.LastUsedAt.Unix())
	return err
}

func (r *Repository) GetSubtitleAsset(ctx context.Context, sourceID, provider, candidateID, format string) (domain.SubtitleAsset, error) {
	var a domain.SubtitleAsset
	var created, lastUsed int64
	err := r.db.QueryRowContext(ctx, `SELECT id,source_id,provider,candidate_id,name,language,format,mime_type,path,created_at,last_used_at
FROM subtitle_assets WHERE source_id=? AND provider=? AND candidate_id=? AND format=?`, sourceID, provider, candidateID, format).
		Scan(&a.ID, &a.SourceID, &a.Provider, &a.CandidateID, &a.Name, &a.Language, &a.Format, &a.MimeType, &a.Path, &created, &lastUsed)
	if err != nil {
		return domain.SubtitleAsset{}, err
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	a.LastUsedAt = time.Unix(lastUsed, 0).UTC()
	ext := ".smi"
	if a.Format == "vtt" {
		ext = ".vtt"
	}
	a.URL = "/api/v1/subtitles/" + a.ID + ext
	return a, nil
}

func (r *Repository) HasSubtitleAsset(ctx context.Context, sourceID, provider, candidateID string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM subtitle_assets WHERE source_id=? AND provider=? AND candidate_id=?)`, sourceID, provider, candidateID).
		Scan(&exists)
	return exists, err
}

func DecodeCursor(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return n, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}
