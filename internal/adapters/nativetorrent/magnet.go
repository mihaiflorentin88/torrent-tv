package nativetorrent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// hashGate coordinates concurrent operations on a single info hash so
// normal Add and ResolveMagnet do not collide, and multiple resolvers for
// the same hash do not leak or double-drop transient torrents.
type hashGate struct {
	mu   sync.Mutex
	refs int
}

func (c *Client) acquireHashGate(ih metainfo.Hash) *hashGate {
	c.gateMu.Lock()
	gate := c.hashGates[ih]
	if gate == nil {
		gate = &hashGate{}
		c.hashGates[ih] = gate
	}
	gate.refs++
	c.gateMu.Unlock()
	gate.mu.Lock()
	return gate
}

func (c *Client) releaseHashGate(ih metainfo.Hash, gate *hashGate) {
	gate.mu.Unlock()
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	gate.refs--
	if gate.refs == 0 {
		delete(c.hashGates, ih)
	}
}

// ResolveMagnet fetches only the metadata a magnet URI names and returns it
// as bencode metainfo bytes for the application to persist. The resolver
// owns its transient torrent: it never selects payload files, never
// persists a session placeholder, and drops the torrent before releasing the
// per-hash gate. Preexisting torrents with the same info hash are exported
// without mutating their trackers, selection, pause state, or persistence,
// and are never dropped.
//
// Native storage stays under the client's configured DataDir: the download
// root argument is accepted for port parity and ignored, mirroring Add's
// save-path convention. Bounded waiting is governed strictly by ctx: the
// engine's fixed five-second waitInfo helper is not used here.
func (c *Client) ResolveMagnet(ctx context.Context, uri string, _ string, discovery domain.MagnetDiscovery) ([]byte, error) {
	if discovery != domain.MagnetDiscoveryPublic {
		return nil, domain.ErrMagnetDiscoveryDenied
	}
	// Validate before the pinned library call: AddTorrentOpt panics on a
	// zero v1 infohash, so hashless and v2-only magnets must be rejected
	// here, not in the library.
	m, err := metainfo.ParseMagnetV2Uri(uri)
	if err != nil {
		return nil, fmt.Errorf("parse magnet uri: %w", err)
	}
	if !m.InfoHash.Ok {
		return nil, errors.New("magnet has no v1 info hash")
	}
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		return nil, fmt.Errorf("torrent spec from magnet uri: %w", err)
	}
	ih := spec.InfoHash

	// The per-hash gate spans the whole add/wait/export/drop cycle so a
	// concurrent Add cannot observe the transient torrent and mistake it
	// for an existing one (which would skip session persistence), and two
	// resolvers cannot fight over one torrent. The global client mutex is
	// taken only for brief bookkeeping, never across the network wait.
	gate := c.acquireHashGate(ih)
	defer c.releaseHashGate(ih, gate)

	t := c.torrent(ih.HexString())
	if t != nil {
		// Existing torrent: export without changing trackers, selection,
		// pause state, or persistence. Never drop.
		if t.Info() == nil {
			select {
			case <-t.GotInfo():
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return exportMetainfo(t, ih)
	}

	c.mu.Lock()
	t, new, err := c.publicClient.AddTorrentSpec(spec)
	if err == nil {
		c.owners[ih] = c.publicClient
	}
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("add torrent spec: %w", err)
	}
	if !new {
		// Defensive: if another path somehow added it, share its wait and
		// never drop what we do not own.
		select {
		case <-t.GotInfo():
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if err := rejectPrivateMetainfo(t); err != nil {
			return nil, err
		}
		return exportMetainfo(t, ih)
	}

	// Ownership confirmed: drop only the owned transient torrent before
	// returning, while the per-hash gate is still held.
	defer func() {
		c.mu.Lock()
		delete(c.owners, ih)
		c.mu.Unlock()
		t.Drop()
	}()
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := rejectPrivateMetainfo(t); err != nil {
		return nil, err
	}
	return exportMetainfo(t, ih)
}

// rejectPrivateMetainfo guards only the transient-resolve path; it must never
// be applied to the preexisting-torrent export path where managed torrents
// (even private ones) are exported as-is.
func rejectPrivateMetainfo(t *torrent.Torrent) error {
	mi := t.Metainfo()
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return fmt.Errorf("decode resolved metainfo info: %w", err)
	}
	if info.Private != nil && *info.Private {
		return domain.ErrPrivateMagnet
	}
	return nil
}

func exportMetainfo(t *torrent.Torrent, ih metainfo.Hash) ([]byte, error) {
	mi := t.Metainfo()
	if mi.HashInfoBytes() != ih {
		return nil, errors.New("resolved metadata does not match magnet info hash")
	}
	raw, err := bencode.Marshal(mi)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) >= 16<<20 {
		return nil, errors.New("resolved metadata is empty or too large")
	}
	return raw, nil
}
