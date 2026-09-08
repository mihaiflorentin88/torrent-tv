package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// familyKey identifies one (media kind, normalized sort title) reconciliation
// family. Grouping authority is domain.ResolveFamilyTitleIDs.
type familyKey struct {
	Kind      domain.MediaKind
	SortTitle string
}

// ReconcileAllCatalogProjections re-resolves every (media_kind, sort_title)
// family from raw evidence and applies projection diffs in one transaction.
// Idempotent: unchanged evidence writes nothing. Runs at startup after
// migrations/backfill, before the catalog is served.
func (r *Repository) ReconcileAllCatalogProjections(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT media_kind,sort_title FROM catalog_releases`)
	if err != nil {
		return err
	}
	families := make(map[familyKey]struct{})
	for rows.Next() {
		var kind, sortTitle string
		if err := rows.Scan(&kind, &sortTitle); err != nil {
			rows.Close()
			return err
		}
		families[familyKey{Kind: domain.MediaKind(kind), SortTitle: sortTitle}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(families) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := reconcileFamiliesTx(ctx, tx, families); err != nil {
		return err
	}
	return tx.Commit()
}

// reconcileFamiliesTx re-resolves the given families from raw evidence and
// applies per-release projection diffs inside tx. When a family's old group
// loses its last release and every mover landed on one surviving id (clean
// merge), its dependents are moved; splits leave dependents on the old group.
func reconcileFamiliesTx(ctx context.Context, tx *sql.Tx, families map[familyKey]struct{}) error {
	for key := range families {
		rows, err := tx.QueryContext(ctx, `SELECT c.release_id,c.title_id,c.title,c.sort_title,c.year,COALESCE(r.imdb_id,'')
FROM catalog_releases c JOIN releases r ON r.id=c.release_id
WHERE c.media_kind=? AND c.sort_title=?`, string(key.Kind), key.SortTitle)
		if err != nil {
			return err
		}
		evidence := make([]domain.ReleaseEvidence, 0, 16)
		current := make(map[string]string, 16)
		for rows.Next() {
			var ev domain.ReleaseEvidence
			var titleID string
			if err := rows.Scan(&ev.ReleaseID, &titleID, &ev.Title, &ev.SortTitle, &ev.Year, &ev.IMDbID); err != nil {
				rows.Close()
				return err
			}
			evidence = append(evidence, ev)
			current[ev.ReleaseID] = titleID
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(evidence) == 0 {
			continue
		}
		resolved := domain.ResolveFamilyTitleIDs(key.Kind, key.SortTitle, evidence)

		// Per-release updates ONLY: a split must update rows individually, never
		// a blanket WHERE title_id = old.
		movedFrom := map[string]map[string]struct{}{} // old title id -> new title ids
		for _, ev := range evidence {
			newID := resolved[ev.ReleaseID]
			oldID := current[ev.ReleaseID]
			if oldID == newID || newID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_releases SET title_id=? WHERE release_id=?`, newID, ev.ReleaseID); err != nil {
				return err
			}
			if movedFrom[oldID] == nil {
				movedFrom[oldID] = make(map[string]struct{})
			}
			movedFrom[oldID][newID] = struct{}{}
		}

		for oldID, targets := range movedFrom {
			if len(targets) != 1 {
				continue // split or partial move: dependents stay on the old group
			}
			var newID string
			for id := range targets {
				newID = id
			}
			var remaining int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_releases WHERE title_id=?`, oldID).Scan(&remaining); err != nil {
				return err
			}
			if remaining != 0 {
				continue // the old group is still alive
			}
			if err := moveExtinctGroupDependents(ctx, tx, oldID, newID); err != nil {
				return err
			}
		}
	}
	return nil
}

// titleRefreshKeyPrefix is the fixed prefix of catalog-title-refresh job and
// sync keys: "catalog-title-refresh:" (22 bytes). Keys are either
// "<prefix><titleID>" or "<prefix><trackerID>:<titleID>"; title ids are
// base64url (no colon), so the first colon after the prefix delimits the
// optional tracker segment.
const titleRefreshKeyPrefix = "catalog-title-refresh:"

// retargetTitleIDKey rewrites one key of a known shape onto newID, reporting
// whether the key was one of the known shapes. Exact equality per shape only —
// base64url ids contain '_' so LIKE is not an option.
func retargetTitleIDKey(key, oldID, newID string) (string, bool) {
	switch {
	case key == titleRefreshKeyPrefix+oldID:
		return titleRefreshKeyPrefix + newID, true
	case key == "metadata:"+oldID:
		return "metadata:" + newID, true
	case strings.HasPrefix(key, titleRefreshKeyPrefix):
		rest := key[len(titleRefreshKeyPrefix):]
		if i := strings.Index(rest, ":"); i > 0 && rest[i+1:] == oldID {
			return titleRefreshKeyPrefix + rest[:i+1] + newID, true
		}
	}
	return "", false
}

// titleIDKeyMatchSQL matches exact title-id key shapes for the given column:
// the plain refresh key, the metadata key, or a tracker-qualified refresh key
// whose trailing segment (after the first post-prefix colon) equals the final
// parameter. Bind in order: plainRefreshOld, metadataOld, prefixLen, prefix,
// colonAt, colonAt, colonAt, oldID.
const titleIDKeyMatchSQL = `(col = ? OR col = ?
OR (substr(col,1,?)=? AND instr(substr(col,?),':')>0 AND substr(col,?+instr(substr(col,?),':'))=?))`

func titleIDKeyMatchArgs(oldID string) []any {
	return []any{
		titleRefreshKeyPrefix + oldID,
		"metadata:" + oldID,
		len(titleRefreshKeyPrefix), titleRefreshKeyPrefix,
		len(titleRefreshKeyPrefix) + 1, len(titleRefreshKeyPrefix) + 1, len(titleRefreshKeyPrefix) + 1,
		oldID,
	}
}

// moveExtinctGroupDependents moves the dependents of a title group that lost
// its last release to a single surviving canonical group. Only ever called on
// a clean merge; job ids are preserved (worker identity/history) while queued
// and running dedupe keys are retargeted, so in-flight workers re-resolve the
// projected identity before applying results.
func moveExtinctGroupDependents(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	// Metadata: transfer only when the target has none — never refresh
	// timestamps on stale content; otherwise drop the extinct row.
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_metadata SET title_id=? WHERE title_id=?
AND NOT EXISTS(SELECT 1 FROM catalog_metadata WHERE title_id=?)`, newID, oldID, newID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_metadata WHERE title_id=?`, oldID); err != nil {
		return err
	}
	// Favorites: uniqueness-preserving union; the surviving row keeps its
	// original created_at.
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE favorites SET title_id=? WHERE title_id=?`, newID, oldID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM favorites WHERE title_id=?`, oldID); err != nil {
		return err
	}

	// Jobs: preserve id, retarget dedupe keys of queued/running work. The
	// payload column stores the human label (see SaveJob), which carries the
	// search query or display title but never a title id, so there is no typed
	// payload field to remarshal.
	jobMatch := strings.ReplaceAll(titleIDKeyMatchSQL, "col", "dedupe_key")
	rows, err := tx.QueryContext(ctx, `SELECT id,dedupe_key FROM jobs WHERE state IN ('queued','running') AND `+jobMatch,
		titleIDKeyMatchArgs(oldID)...)
	if err != nil {
		return err
	}
	type jobRef struct {
		id, key string
	}
	var refs []jobRef
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, jobRef{id, key})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, ref := range refs {
		newKey, ok := retargetTitleIDKey(ref.key, oldID, newID)
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE jobs SET dedupe_key=? WHERE id=? AND state IN ('queued','running') AND dedupe_key=?`, newKey, ref.id, ref.key); err != nil {
			return err
		}
		// Coalesce the duplicate left behind on UNIQUE conflict (the target key
		// already existed); a no-op when the update moved the row.
		if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE state IN ('queued','running') AND dedupe_key=?`, ref.key); err != nil {
			return err
		}
	}

	// sync_state: same exact-key retarget for catalog-title-refresh names.
	syncMatch := strings.ReplaceAll(titleIDKeyMatchSQL, "col", "name")
	rows, err = tx.QueryContext(ctx, `SELECT name FROM sync_state WHERE `+syncMatch,
		titleIDKeyMatchArgs(oldID)...)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range names {
		newName, ok := retargetTitleIDKey(name, oldID, newID)
		if !ok || newName == name {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE sync_state SET name=? WHERE name=?`, newName, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_state WHERE name=?`, name); err != nil {
			return err
		}
	}
	return nil
}
