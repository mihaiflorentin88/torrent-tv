# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

One household uses the browser and a Samsung Tizen television from the sofa to discover, download, resume, and manage private media. The interface must remain ready for future profiles without exposing a profile selector in this release.

## Product Purpose

Torrent TV turns tracker releases into a browsable media library, sends chosen sources to the household qBittorrent instance, and plays completed or progressively available media through the browser and television. Success means reaching the desired movie or episode quickly with a remote, understanding source quality before downloading, and resuming playback reliably.

## Positioning

Unlike a generic tracker browser or media-server library, the product combines live FileList availability and source health with a Plex-like canonical movie/show library, version selection, managed qBittorrent ownership, and household playback history.

## Operating Context

The server runs on a Raspberry Pi on the private LAN alongside qBittorrent. The browser performs full configuration and administration. The television is primarily operated at distance with a Samsung Smart Remote D-pad, Back, OK, and media keys. FileList names and metadata can be incomplete, so parsed fallback titles and source facts must remain useful without third-party metadata.

## Capabilities and Constraints

- Preact and TypeScript clients share the server API but use platform-specific interaction models.
- The server persists configuration and application state in local files and SQLite.
- The application manages only torrents it added to qBittorrent.
- Tizen direct-plays original media. Browser compatibility may transcode one selected audio stream to AAC stereo while always copying video unchanged; video is never transcoded.
- Playback before torrent completion is implemented at the range/piece level but remains unreliable on the target hardware and is documented as an open defect.
- Tizen currently exposes connection management plus playback/subtitle controls inside the player. Provider credentials, dependency diagnostics, storage, and network administration remain browser-only; TV playback-preference settings are future work.
- No operating-system packages may be installed without explicit permission.

## Brand Commitments

- Product name: Torrent TV; television label: Torrent TV.
- Original dark charcoal identity with restrained emerald/teal accents.
- Plex is the primary information-architecture and media-library reference. Netflix is secondary inspiration for cinematic discovery, artwork-led rails, and an expanding navigation rail.
- The result must be recognizably original rather than a trademark or palette imitation.

## Evidence on Hand

- Live FileList catalog data and parsed release fixtures.
- Confirmed working browser playback after download completion.
- Confirmed Tizen boot, server setup, 0.1.3 D-pad input, and completed-download playback on the target Samsung television. The redesigned 0.2.0 focus graph/player behavior is pending a physical-TV test.
- Existing application icon and Apps2Samsung-compatible WGT packaging workflow.
- No licensed editorial artwork beyond metadata-provider assets; clients must provide deliberate placeholders when artwork is missing.

## Product Principles

- Resume first, discover second.
- Media identity before tracker syntax.
- Every remote movement is predictable and reversible.
- Source quality and availability remain visible at the decision point.
- Metadata improves the experience but never blocks browsing or playback.

## Accessibility & Inclusion

All browser functionality must be keyboard accessible. Television focus must be immediately visible at viewing distance, never become trapped, and restore predictably across routes and overlays. Motion must respect reduced-motion preferences, and meaningful artwork must have text alternatives.
