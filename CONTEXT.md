# Torrent TV

Turns a private-tracker (FileList) catalog into a browsable, streamable home library for a single household on the home LAN, served by a low-power always-on box.

## Language

### Tracker & catalog

**Release**:
One torrent entry on the FileList tracker; the durable source record the catalog mirrors.
_Avoid_: torrent (when talking about catalog data)

**Parsed release**:
The structured interpretation of a Release name: title, Kind, season/episode, quality attributes.

**Quality attributes**:
The technical characteristics parsed from a Release name: resolution, codec, and release class (REMUX, WEB-DL, WEBRIP, ...).
_Avoid_: source

**Kind**:
The media class of a Release: `movie` or `series`. Never inferred from the tracker category alone.

**Category**:
A FileList tracker category ID; a hint that can mislead Kind.
_Avoid_: genre, section

**Canonical title**:
One movie/show identity that groups many Releases (IMDb ID preferred, title+year fallback). The unit the library browses.

**Catalog**:
The append-only local mirror of tracker Releases; rows are never removed.
_Avoid_: library

**Catalog sync**:
A pull of tracker Releases into the Catalog, in one of two modes: `latest` appends the newest tracker window; `rebuild` refreshes every enabled category's window and rebuilds local projections. Append-only either way; runs on a schedule (latest hourly, rebuild weekly) or by hand as Fetch latest / Rebuild catalog.
_Avoid_: cache rebuild, refresh (unqualified)

### Playback

**Managed download**:
A download this server created and tracks. Only Managed downloads are visible or deletable.

**Engine route**:
A persistent pointer to where a torrent lives in the download engine, stable across restarts.

**Download engine**:
The torrent client the server drives for Managed downloads: the embedded native engine or an external qBittorrent. One engine is active per deployment; a download belongs to the engine that created it, through its Engine route.
_Avoid_: torrent client (unqualified), backend

**Prepare**:
Resolve or create the Managed download behind a Source and return its stream URL; the step before any playback.

**Source**:
A playable file entry of a Release — a file inside the torrent, or a virtual per-episode entry.
_Avoid_: "source" for quality attributes (resolution/codec) — say quality attributes

**Progressive playback**:
Playing a torrent before it completes by serving the pieces already on disk.
_Avoid_: streaming (unqualified)

**Direct play**:
Serving original bytes so the client device decodes everything; on both screens for natively playable content.

**Compatibility stream**:
The server-built playback route for browser-hostile audio in a Source: video bytes copied untouched, audio transcoded to AAC. Introduced by ADR-0003, superseding the removed client-side decode.
_Avoid_: browser stream (code name), client decode

### Player controls

**Browser player**:
The player surface that renders video with an HTML5 video element in a browser.
_Avoid_: web client, web player

**Player command**:
A logical playback action — play/pause, seek, volume step, mute, fullscreen, subtitle Player panel, fraction jump — triggered by keys or buttons.
_Avoid_: player action

**Player shortcut**:
A keyboard binding on the Browser player that fires a Player command while no Player panel is open.
_Avoid_: hotkey, keybinding

**Player panel**:
An in-player chooser — audio tracks or subtitles — that takes keys away from Player shortcuts while it is open.
_Avoid_: dialog, popup

**OSD**:
The transient on-screen feedback a Player command triggers; it auto-hides.
_Avoid_: toast, notification

### Subtitles

**Subtitle candidate**:
A selectable subtitle offering listed for a download, from any Subtitle source.

**Subtitle source**:
Where a Subtitle candidate came from: `contained` (sidecar file shipped with the torrent), `embedded` (stream inside the media container), `subdl` (fetched from the SubDL provider).
_Avoid_: local/remote as the primary taxonomy

**Subtitle asset**:
Persisted subtitle bytes prepared for one download, identified by an id scoped to that download.

Menus display `contained` — and provider candidates already downloaded — as **Local**, `embedded` as **Built-in**, and providers by their own name.

### Household & jobs

**Household**:
The single server-side profile: favorites, resume positions, watched state. Survives torrent deletion.

**Job**:
A persisted unit of background work with a dedupe key, states, and retries.

### Retention

**Allocation**:
The configured cap on total bytes of the service's stored torrent content, incomplete downloads included.

**Eviction**:
Automatic deletion of a torrent and its files — every Managed download sharing that Engine route — to bring stored content back within the Allocation. Catalog rows and Household state survive.

### Devices

**Support floor**:
The oldest Tizen TV platform the single TV client is built to run on: 5.0. Declared in the manifest as `required_version` — a pure floor; one package serves every newer platform with no ceiling.
_Avoid_: target version (what a validation run is aimed at)

**Android floor**:
The oldest Android TV platform TorrentTV, the Android TV client, runs on: Android 8.0, declared as `minSdk 26` — a pure floor; one APK serves every newer platform with no ceiling. Same posture as the Tizen Support floor.
_Avoid_: target SDK (a build setting, not the support promise)

**Verified TV**:
A physical TV recorded in the Tizen verification log, where behavior counts as confirmed only by direct observation; the household's 2019 premium set and 2023 S90C are the Verified TVs. Any other Tizen set at or above the Support floor is best-effort. The Android client's verification log lives in docs/ANDROIDTV.md and starts with no named hardware.
_Avoid_: target TV, test device

**Error panel**:
The full-screen, readable explanation the TV client shows when the application hits an unhandled error, reported to the server's client-diagnostics channel. The client never fails silently.
_Avoid_: black screen (as a state name), crash overlay
