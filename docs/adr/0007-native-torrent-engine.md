# The native torrent engine is the default acquisition engine; qBittorrent coexists by owner

---
status: revised — the one-active-engine rule was superseded by owner-based routing (2026-09-08); the native-default and range-elevation decisions stand
---

The server embeds a BitTorrent engine (anacrolix/torrent v1.61.0, pinned, MPL-2.0,
pure Go) implementing the same TorrentEngine port the qBittorrent adapter
implements. Both engines register under owner prefixes and every download routes
to the engine that created it through its Engine route (`native:<hash>` /
`qb:<hash>`); there is no fallback to another engine. Settings no longer pick one
active engine: the saved `downloadEngine` (`native` default, `qbittorrent`) selects
the acquisition engine applied at startup, responses carry `engineRunning` (the
acquisition engine this process started with) alongside the saved value so
running-vs-saved drift is visible, and a download whose owning engine failed to
construct surfaces as unavailable. The native engine writes pieces in place under
`<DownloadRoot>/<infohash>/`, seeds until eviction, keeps its session (metainfo,
file selection, piece-completion bolt db) under `data/torrent-session`, and
elevates exactly the byte window a seek or probe needs (`PrepareRange`), which
qBittorrent cannot do and no-ops. Seek and probe windows are elevated as
explicit piece-priority ranges set through the public per-piece setter
(`Piece.SetPriority`), with the per-file baseline reasserting behind
piece-level overrides (priorities Raise); reader-based steering is inert in
the pinned version because reader readahead zeroes while not reading.
The engine runs two isolated underlying torrent clients behind one facade: a
private client (NoDHT, DisablePEX) for private-flagged metainfo and a public
client (DHT + PEX on) for everything else. Admission classifies by the info
dictionary's `private` bit — new Adds, magnet resolves, and saved-session
restore alike; the pinned library enforces nothing itself, so this
classification is the whole privacy boundary. Magnets resolve only when the
acquisition source explicitly authorizes public discovery (`MagnetDiscovery` on
the engine port; Pirate Bay is the only authorized source today); a magnet
without authorization is denied before any network activity, and unexpected
private metadata on an authorized public resolve is rejected, not adopted. A
separate `torrentPublicPeerPort` (default 0, OS-assigned) listens for the
public client; both clients share one session store, one piece-completion
database, and the infohash-keyed media layout, with a per-hash owner map
keeping every infohash on exactly one client.

## Evidence

- cenkalti/rain was disqualified on its own source: no per-file selection, and
  every file preallocates — season-pack exclusion is impossible.
- Hand-rolling a BEP-3 client was rejected: protocol-edge stall risk for a
  dependency saving that is compile-time only.
- The dependency argument is operational: the native default removes the
  qBittorrent container entirely; go.mod weight is not runtime weight.

## Considered options

- **Both engines live simultaneously** — rejected at acceptance time; reversed by
  owner-based routing (2026-09-08): each download routes to its creating engine,
  so retention and allocation accounting run per-owner and coexistence costs
  nothing per household. New acquisitions still default to the native engine.
- **rain (cenkalti)** — rejected: no file selection.
- **cgo libtorrent bindings** — rejected: stale, and cgo breaks the
  six-platform matrix (windows/linux/darwin x amd64/arm64).

## Consequences

- The compose default is single-container; `--profile qbittorrent` restores the
  sidecar stack; external qBittorrent keeps serving bare-metal Pi deployments
  (ADR-0005 governs those).
- Anacrolix upgrades require checking its retract history; the pinned version
  is the stability boundary.
- Per-tracker seeder counts are unavailable from anacrolix v1.61.0's public
  API; native-mode downloads report tracker stats as zero.
- Originally retention skipped foreign-engine routes because only one engine was
  active; owner-based routing removed that: eviction resolves each route's owning
  engine and removes through it.
- Native error surfacing in v1.61.0 is limited to disk-write failures
  (`SetOnWriteChunkError` → canonical error state); tracker and peer failures
  surface only as stalled progress (the WaitRange timeout at playback) — the
  pinned library exposes no per-torrent lastError.
