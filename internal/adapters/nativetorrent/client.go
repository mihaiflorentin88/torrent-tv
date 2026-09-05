// Package nativetorrent implements the TorrentEngine port with an embedded
// anacrolix/torrent client. Media bytes land at
// <DataDir>/<infohash-hex>/<torrent-relative path> so the application's
// disk-first progressive serving reads them exactly like qBittorrent content.
package nativetorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// Config is the native engine's deployment surface.
type Config struct {
	// DataDir holds media files: <DataDir>/<infohash-hex>/<torrent-relative path>.
	DataDir string
	// SessionDir holds session.json and the bolt piece-completion database.
	SessionDir string
	// PeerPort is the BitTorrent listen port; 0 lets the OS assign one.
	PeerPort int
	// Readahead is the seek-window size in bytes.
	Readahead int64
	// StartWindow is the window elevated when a file is first prepared.
	StartWindow int64
}

type Client struct {
	cl      *torrent.Client
	dataDir string
	cfg     Config
	// stop closes to end the speed sampler's loop.
	stop chan struct{}

	announce *announceCapture

	mu           sync.Mutex
	session      *sessionStore
	paused       map[string]bool
	speeds       map[string]*speedMeter
	uploadSpeeds map[string]*speedMeter
	windows      map[string]*streamWindow
	// selected mirrors the active file selection per hash so Status can
	// report qBittorrent-compatible selected-set completion.
	selected map[string][]int
	// writeErrs records the latest storage write failure per hash — the
	// pinned library's only surfaceable per-torrent fault; writeErrHooks
	// keeps the registered callback reachable so tests can fire it.
	writeErrs     map[string]writeErrRec
	writeErrHooks map[string]func(error)
}

// writeErrRec is a recorded storage write failure and when it happened.
type writeErrRec struct {
	err error
	at  time.Time
}

// armWriteChunkErrorLocked arms the torrent's write-chunk callback — the
// pinned library's only public fault surface — recording failures under the
// hash so Status can report the canonical error state; Resume and Remove
// clear them. Invoked wherever a torrent enters the client.
// Caller holds c.mu.
func (c *Client) armWriteChunkErrorLocked(hash string, t *torrent.Torrent) {
	hook := func(err error) {
		c.mu.Lock()
		c.writeErrs[hash] = writeErrRec{err: err, at: time.Now()}
		c.mu.Unlock()
	}
	c.writeErrHooks[hash] = hook
	t.SetOnWriteChunkError(hook)
}

// New constructs the engine and reloads every persisted torrent from the
// session store.
func New(cfg Config) (*Client, error) {
	if cfg.DataDir == "" || cfg.SessionDir == "" {
		return nil, errors.New("native engine requires dataDir and sessionDir")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("native engine data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.SessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("native engine session dir: %w", err)
	}
	pc, err := storage.NewBoltPieceCompletion(cfg.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("native engine piece completion db: %w", err)
	}
	impl := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: cfg.DataDir,
		// Torrent display names are arbitrary tracker bytes and can be invalid
		// path components (a colon on Windows); key the layout by infohash.
		TorrentDirMaker: func(baseDir string, _ *metainfo.Info, ih metainfo.Hash) string {
			return filepath.Join(baseDir, ih.HexString())
		},
		// Pieces must write in place at their final paths: playback reads the
		// files directly, and part-file promotion would hide incomplete bytes
		// behind a .part rename until completion.
		UsePartFiles:    g.Some(false),
		PieceCompletion: pc,
	})
	tcfg := torrent.NewDefaultClientConfig()
	tcfg.DataDir = cfg.DataDir
	tcfg.DefaultStorage = impl
	tcfg.NoDHT = true                   // FileList is a private tracker; torrents are private-flagged
	tcfg.NoDefaultPortForwarding = true // household appliance; never poke the router
	tcfg.Seed = true                    // seed until eviction
	tcfg.ListenPort = cfg.PeerPort
	// Private trackers allowlist client identities; presenting the household's
	// established qBittorrent identity keeps announces acceptable.
	tcfg.PeerID, tcfg.HTTPUserAgent = newTrackerIdentity()
	capture := newAnnounceCapture()
	tcfg.Slogger = slog.New(capture.Handler())
	cl, err := torrent.NewClient(tcfg)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("native torrent client: %w", err)
	}
	c := &Client{
		cl:            cl,
		dataDir:       cfg.DataDir,
		announce:      capture,
		cfg:           cfg,
		stop:          make(chan struct{}),
		session:       newSessionStore(cfg.SessionDir, pc),
		paused:        make(map[string]bool),
		speeds:        make(map[string]*speedMeter),
		uploadSpeeds:  make(map[string]*speedMeter),
		windows:       make(map[string]*streamWindow),
		selected:      make(map[string][]int),
		writeErrs:     make(map[string]writeErrRec),
		writeErrHooks: make(map[string]func(error)),
	}
	if err := c.loadSession(); err != nil {
		_ = cl.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("native engine session reload: %w", err)
	}
	go c.speedLoop()
	return c, nil
}

func (c *Client) Close() error {
	c.stopSpeedLoop()
	errs := c.cl.Close()
	if c.session.pc != nil {
		if err := c.session.pc.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// loadSession re-adds every persisted torrent and re-applies its selection
// and paused flag. Piece completion comes from the bolt db, so nothing is
// re-verified from disk.
func (c *Client) loadSession() error {
	for hash, entry := range c.session.entries {
		if len(entry.Metainfo) == 0 {
			continue
		}
		mi, err := metainfo.Load(bytes.NewReader(entry.Metainfo))
		if err != nil {
			return err
		}
		if c.torrent(hash) != nil {
			continue
		}
		t, err := c.cl.AddTorrent(mi)
		if err != nil {
			return err
		}
		if err := waitInfo(context.Background(), t); err != nil {
			return err
		}
		c.mu.Lock()
		c.armWriteChunkErrorLocked(hash, t)
		c.mu.Unlock()
		if len(entry.MediaIndices) > 0 {
			if err := c.PrepareFiles(context.Background(), hash, entry.MediaIndices, entry.SubtitleIndices); err != nil {
				return err
			}
		}
		if entry.Paused {
			if err := c.Pause(context.Background(), hash); err != nil {
				return err
			}
		}
	}
	return nil
}

// torrent returns the live torrent for an infohash hex string, or nil.
func (c *Client) torrent(hash string) *torrent.Torrent {
	var ih metainfo.Hash
	// metainfo.NewHashFromHex panics on malformed hex in v1.61.0; the method
	// form returns an error instead.
	if err := ih.FromHexString(hash); err != nil {
		return nil
	}
	for _, t := range c.cl.Torrents() {
		if t.InfoHash() == ih {
			return t
		}
	}
	return nil
}

// Add registers a .torrent (bencode metainfo from the tracker) and returns
// its infohash hex. Adding an infohash the engine already holds is
// idempotent. Nothing downloads until PrepareFiles selects files.
func (c *Client) Add(ctx context.Context, r io.Reader, _ string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return "", fmt.Errorf("read torrent metainfo: %w", err)
	}
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("torrent metainfo: %w", err)
	}
	hash := mi.HashInfoBytes().HexString()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.torrent(hash); t != nil {
		return hash, nil
	}
	// The library's initial piece check must run: pieces with unknown
	// completion are never schedulable (effectivePriority stays None), so
	// disabling it would deadlock the swarm. Bolt-persisted completion lets
	// the check skip already-verified data on restart, and a fresh torrent's
	// check only marks its missing pieces known-incomplete. Priorities stay
	// None until PrepareFiles selects files, so Add still downloads nothing.
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return "", fmt.Errorf("torrent spec: %w", err)
	}
	t, _, err := c.cl.AddTorrentSpec(spec)
	if err != nil {
		return "", fmt.Errorf("add torrent: %w", err)
	}
	// FileList metainfo carries the info dictionary, so metadata is
	// effectively immediate; wait briefly rather than assume.
	if err := waitInfo(ctx, t); err != nil {
		return "", err
	}
	c.armWriteChunkErrorLocked(hash, t)
	if err := c.session.putMeta(hash, raw); err != nil {
		return "", fmt.Errorf("persist torrent session: %w", err)
	}
	return hash, nil
}

func waitInfo(ctx context.Context, t *torrent.Torrent) error {
	deadline := time.Now().Add(5 * time.Second)
	for t.Info() == nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("native engine torrent metadata timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// Files lists the torrent's files in metainfo order with cumulative byte
// offsets. An empty slice means metadata is not ready yet; the caller polls.
func (c *Client) Files(_ context.Context, hash string) ([]domain.TorrentFile, error) {
	t := c.torrent(hash)
	if t == nil {
		return nil, domain.ErrTorrentNotFound
	}
	if t.Info() == nil {
		return []domain.TorrentFile{}, nil
	}
	files := t.Files()
	out := make([]domain.TorrentFile, 0, len(files))
	for i, f := range files {
		size := f.Length()
		progress := 0.0
		if size > 0 {
			progress = float64(f.BytesCompleted()) / float64(size)
		}
		out = append(out, domain.TorrentFile{
			Index:     i,
			Path:      f.Path(),
			SizeBytes: size,
			Offset:    f.Offset(),
			Progress:  progress,
			Playable:  playable(f.Path()),
		})
	}
	return out, nil
}

// Test reports a diagnostic for the settings test endpoint.
func (c *Client) Test(_ context.Context) (string, error) {
	n := len(c.cl.Torrents())
	if addrs := c.cl.ListenAddrs(); len(addrs) > 0 {
		if tcp, ok := addrs[0].(*net.TCPAddr); ok {
			// The HTTP layer prefixes the settings-configured engine name.
			return fmt.Sprintf("%d torrents, peer port %d", n, tcp.Port), nil
		}
	}
	return fmt.Sprintf("%d torrents", n), nil
}

// playable is shared-by-copy with the qBittorrent adapter's media-extension
// test: the two copies must move together.
func playable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".webm", ".m4v", ".ts", ".m2ts":
		return true
	}
	return false
}

// Status reports the engine-level DTO. Tracker seeder/leecher counts are not
// exposed by anacrolix v1.61.0 and are reported as zero; peers/seeds come
// from live connection gauges.
func (c *Client) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	t := c.torrent(hash)
	if t == nil {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	info := t.Info()
	var total, pieceSize int64
	if info != nil {
		total = info.TotalLength()
		pieceSize = info.PieceLength
	}
	done := t.BytesCompleted()
	progress := 0.0
	if total > 0 {
		progress = float64(done) / float64(total)
	}
	c.mu.Lock()
	_, failed := c.writeErrs[hash]
	paused := c.paused[hash]
	speed := c.currentSpeed(hash)
	sel := c.selected[hash]
	c.mu.Unlock()
	// qBittorrent's progress and seeding state describe the selected download
	// set, not the whole metainfo; mirror that so a completed selection inside
	// a season pack ever reaches the completed path.
	if len(sel) > 0 {
		if p, ok := selectedProgress(t, sel); ok {
			progress = p
		}
	}
	st := t.Stats()
	eta := int64(0)
	if speed > 0 && total > done {
		eta = (total - done) / speed
	}
	state := domain.StateDownloading
	switch {
	case failed:
		state = domain.StateError
	case paused && progress >= 1:
		state = domain.StatePausedUP
	case paused:
		state = domain.StatePausedDL
	case progress >= 1:
		state = domain.StateSeeding
	}
	return domain.DownloadStatus{
		Hash:                      hash,
		State:                     state,
		Progress:                  progress,
		DownloadedBytes:           done,
		TotalBytes:                total,
		SpeedBytesPerSecond:       speed,
		UploadSpeedBytesPerSecond: c.currentUploadSpeed(hash),
		ETASeconds:                eta,
		Peers:                     st.TotalPeers,
		Seeds:                     st.ConnectedSeeders,
		PieceSize:                 pieceSize,
		Sequential:                true,
		FirstLastPriority:         true,
		TrackerError:              c.announce.Error(hash),
		SavePath:                  c.dataDir,
		ContentPath:               filepath.Join(c.dataDir, hash),
		TempPathEnabled:           false,
	}, nil
}

// selectedProgress computes progress over the selected files' bytes only,
// mirroring qBittorrent whose progress and uploading state describe the
// selected download set. ok is false when the selection covers no bytes.
func selectedProgress(t *torrent.Torrent, indices []int) (progress float64, ok bool) {
	files := t.Files()
	var total, done int64
	for _, i := range indices {
		if i < 0 || i >= len(files) {
			continue
		}
		total += files[i].Length()
		done += files[i].BytesCompleted()
	}
	if total <= 0 {
		return 0, false
	}
	return float64(done) / float64(total), true
}

// Pieces maps piece state to the qbit-compatible integer convention the
// application consumes: 0 missing, 1 in progress, 2 complete.
func (c *Client) Pieces(_ context.Context, hash string) (domain.PieceMap, error) {
	t := c.torrent(hash)
	if t == nil {
		return domain.PieceMap{}, domain.ErrTorrentNotFound
	}
	n := int(t.NumPieces())
	states := make([]int, n)
	for i := range n {
		ps := t.PieceState(i)
		switch {
		case ps.Complete:
			states[i] = 2
		case ps.Partial || ps.Checking || ps.QueuedForHash:
			states[i] = 1
		}
	}
	pieceSize := int64(0)
	if info := t.Info(); info != nil {
		pieceSize = info.PieceLength
	}
	return domain.PieceMap{States: states, PieceSize: pieceSize}, nil
}

// streamWindow is the per-torrent priority steer: the piece range currently
// elevated above the per-file baselines so playback and probes download
// their exact byte window first. It never serves bytes — the app reads the
// files from disk. anacrolix v1.61.0 readers cannot steer without a
// blocking Read (their readahead is suppressed until reading begins), so
// the window is carried as explicit piece priorities.
type streamWindow struct {
	first, last int // inclusive piece range; empty when first > last
}

// PrepareFile prepares a single media file plus subtitle sidecars.
func (c *Client) PrepareFile(ctx context.Context, hash string, index int, subtitleIndices []int) error {
	return c.PrepareFiles(ctx, hash, []int{index}, subtitleIndices)
}

// PrepareFiles queues exactly the wanted files (baseline priority) and
// un-queues everything else, then elevates the head window of the first
// selected media file so playback starts on the first pieces, mirroring
// qBittorrent's sequential + first/last-piece scheduling.
func (c *Client) PrepareFiles(_ context.Context, hash string, indices []int, subtitleIndices []int) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	if t.Info() == nil {
		return errors.New("native engine torrent metadata not ready")
	}
	if len(indices) == 0 {
		return errors.New("no media files selected")
	}
	files := t.Files()
	for _, i := range indices {
		if i < 0 || i >= len(files) {
			return fmt.Errorf("selected file %d unavailable in torrent", i)
		}
	}
	want := map[int]bool{}
	for _, i := range indices {
		want[i] = true
	}
	for _, i := range subtitleIndices {
		want[i] = true
	}
	for i, f := range files {
		if want[i] {
			f.SetPriority(torrent.PiecePriorityNormal)
		} else {
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.session.setSelection(hash, indices, subtitleIndices); err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	if _, ok := c.windows[hash]; !ok {
		c.windows[hash] = &streamWindow{first: 0, last: -1}
		c.steerLocked(t, files[indices[0]].Offset(), c.cfg.StartWindow)
	}
	sel := make([]int, 0, len(want))
	for i := range want {
		sel = append(sel, i)
	}
	c.selected[hash] = sel
	return nil
}

// PrepareRange repositions the stream window onto the requested byte range.
// Called from waitReadablePath on every progressive media read, this is what
// makes deep seeks and tail probes (MKV cues, MP4 moov) arrive without
// waiting for the whole file. start is a torrent-global byte offset per the
// TorrentEngine port contract, so the file is implicit in it.
func (c *Client) PrepareRange(_ context.Context, hash string, _ int, start, count int64) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	if t.Info() == nil {
		return errors.New("native engine torrent metadata not ready")
	}
	if count <= 0 {
		// A non-positive window is a legitimate no-op, but only once the
		// torrent and its metadata are known to exist.
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.windows[hash]; !ok {
		c.windows[hash] = &streamWindow{first: 0, last: -1}
	}
	c.steerLocked(t, start, count)
	return nil
}

// steerLocked moves the torrent's stream window onto [start, start+count)
// in torrent-global byte offsets: the previous window's pieces drop back to
// their per-file baselines and the new range is raised to Readahead so it
// overtakes the Normal baseline. Caller holds c.mu.
func (c *Client) steerLocked(t *torrent.Torrent, start, count int64) {
	n := int(t.NumPieces())
	pieceLen := t.Info().PieceLength
	w := c.windows[t.InfoHash().HexString()]
	for i := w.first; i <= w.last; i++ {
		t.Piece(i).SetPriority(torrent.PiecePriorityNone)
	}
	first := max(start, 0) / pieceLen
	last := min((start+count-1)/pieceLen, int64(n-1))
	for i := int(first); i <= int(last); i++ {
		t.Piece(i).SetPriority(torrent.PiecePriorityReadahead)
	}
	w.first, w.last = int(first), int(last)
}

// Pause suspends data transfer without removing the torrent or its files.
func (c *Client) Pause(_ context.Context, hash string) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	t.DisallowDataDownload()
	t.DisallowDataUpload()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused[hash] = true
	if err := c.session.setPaused(hash, true); err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	return nil
}

// Resume restores data transfer after Pause.
func (c *Client) Resume(_ context.Context, hash string) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	t.AllowDataDownload()
	t.AllowDataUpload()
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.paused, hash)
	// Resuming is a user-initiated fresh start: a recorded storage failure no
	// longer pins the torrent in the error state.
	delete(c.writeErrs, hash)
	if err := c.session.setPaused(hash, false); err != nil {
		return fmt.Errorf("persist session: %w", err)
	}
	return nil
}

// Remove drops the torrent; with deleteFiles it also deletes the torrent's
// data directory and clears its persisted piece-completion rows so a later
// re-add never trusts stale completion bits for deleted bytes.
func (c *Client) Remove(_ context.Context, hash string, deleteFiles bool) error {
	t := c.torrent(hash)
	c.mu.Lock()
	delete(c.windows, hash)
	delete(c.selected, hash)
	delete(c.writeErrs, hash)
	delete(c.writeErrHooks, hash)
	c.announce.clear(hash)
	paused := c.paused[hash]
	delete(c.paused, hash)
	delete(c.speeds, hash)
	delete(c.uploadSpeeds, hash)
	c.mu.Unlock()
	// Bookkeeping and data cleanup both run to completion even when the other
	// fails; the first error wins, joined when both fail.
	var err error
	if t != nil {
		if deleteFiles {
			c.clearPieceCompletion(hash, t)
		}
		t.Drop()
	}
	if serr := c.session.delete(hash, paused); serr != nil {
		err = fmt.Errorf("persist session: %w", serr)
	}
	if deleteFiles {
		if rerr := os.RemoveAll(filepath.Join(c.dataDir, hash)); rerr != nil {
			err = errors.Join(err, fmt.Errorf("delete torrent data: %w", rerr))
		}
	}
	return err
}

func (c *Client) clearPieceCompletion(hash string, t *torrent.Torrent) {
	n := int(t.NumPieces())
	ih := t.InfoHash()
	for i := range n {
		_ = c.session.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, false)
	}
}
