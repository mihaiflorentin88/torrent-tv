# Native public and private torrent discovery

Date: 2026-09-08
Status: Written specification approved by the user on 2026-09-08; implementation planning authorized.

## Goal and scope

Enable public peer discovery for Pirate Bay downloads through the Native engine while keeping private torrents out of DHT and peer exchange. Preserve existing downloads, saved selections, piece completion, media paths, and application-level engine ownership.

The user selected isolated public/private clients over application-managed selective discovery. The isolation belongs inside the existing Native adapter: two underlying `torrent.Client` instances, not two application engines.

The user approved local verification followed by installing a fix build on the Pi, restarting `torrent-tv`, and checking the existing downloads. Retain the previous binary for rollback. Release publication, image publication, and update-feed announcements are not authorized by this design.

## Evidence and limitations

Read-only source and Pi investigation established:

- `internal/adapters/nativetorrent/client.go:126` sets `NoDHT=true` globally, including for public magnets.
- Pinned `github.com/anacrolix/torrent v1.61.0` parses `metainfo.Info.Private` but has no runtime private-flag enforcement in automatic DHT announcements or peer exchange. A global DHT enable on the existing shared client is unsafe. Private peer exchange must also be disabled explicitly.
- The Go module version listing ends at `v1.61.0`; no newer tagged release was available during investigation. Current upstream documentation is not evidence of behavior in this pinned version.
- The Pi has no global IPv6 route. This explains the UDP6 unreachable error without establishing an IPv4 failure.
- A transaction-matched IPv4 DHT ping response arrived from `dht.transmissionbt.com:6881`.
- IPv4 UDP tracker connect handshakes succeeded from the Pi with `tracker.opentrackr.org`, `open.stealth.si`, `tracker.dler.org`, and `open.demonii.com`. Single probes to `tracker.torrent.eu.org` and `exodus.desync.com` timed out; this does not establish that either service is dead.
- The live downloads API showed a completed Native download carrying a tracker DNS error and two Native Silo downloads with metadata, zero bytes, and zero peers. Tracker announce errors are captured separately from actual storage-write errors; they do not themselves set the Native torrent state to error.
- Magnet metadata resolution has a 90-second deadline. Playback piece waits default to 600 seconds and have a different timeout message. No complete runtime correlation establishes which attempt produced the user's literal `context deadline exceeded`.

Disabled DHT is a confirmed public-discovery limitation, not proof of the sole cause of the stalled torrents. Tracker handshakes do not establish per-infohash peer yield; DHT reachability does not establish a live swarm. Verification must measure discovery and bytes rather than warning disappearance.

## Alternatives considered

Selected: two underlying clients inside one Native adapter. This uses library-managed discovery for public torrents and global discovery disablement for private torrents. The cost is a second network client and internal hash routing.

Rejected: one client with automatic DHT announcements disabled and adapter-managed public-only announcement loops. Supported library APIs provide control points, but this adds discovery cadence and cancellation ownership. A global peer-exchange disable would also remove that capability from public torrents.

Rejected: a dependency fork or unreleased dependency update. There is no newer listed tagged release, and a privacy patch would carry maintenance and metadata-before-classification risks beyond this fix.

## Architecture and privacy boundary

### One adapter, two clients

The Native adapter owns:

- A private client with `NoDHT=true` and `DisablePEX=true`.
- A public client with DHT and peer exchange enabled and library-managed periodic DHT announcements.
- One session store, one piece-completion database owner, existing storage paths, and shared hash-level coordination.

Each infohash may be held by only one underlying client at a time. Add, Resolve, lookup, status, selection, streaming, pause/resume, removal, speed collection, and shutdown must respect this ownership. The existing per-hash gate coordinates admission; there must not be separate gates that allow the same hash into both clients.

Application routes remain `native:<hash>` and `qb:<hash>`. Native remains the acquisition default. No second application engine, new download root, media copy, reacquisition through another engine, or qBittorrent fallback is introduced.

### Complete metainfo

Parse and validate the info dictionary before admission to a network client:

- `private=1` selects the private client.
- Valid non-private metainfo selects the public client, including an absent optional private flag.
- Invalid metainfo fails before admission; it is not treated as public.

The same classification applies to new Add calls and saved session metainfo. The private bit is part of the hashed info dictionary, so valid metainfo supplies the classification on restore without a second persisted routing authority. Keep the existing hash-keyed data layout and session records.

This privacy boundary follows metainfo semantics. It does not infer privacy from the last tracker hostname, current settings, or a warning string.

### Magnets and source eligibility

A magnet does not supply a trustworthy private flag before metadata retrieval. The acquisition path must explicitly authorize public discovery based on a recognized public source; Pirate Bay is the currently supported source. Unknown sources must not receive public discovery by default. Pass this policy explicitly through the relevant application/engine boundary and migrate affected callers rather than infer it from URI text.

For an already-managed hash, return its existing metadata without moving it between clients or changing trackers, pause state, selection, or ownership. Do not introduce a public lookup for a hash already held privately.

For a new authorized public magnet, resolve metadata on the public client. Validate the resulting metainfo before exporting it for managed acquisition. If it is private, stop the transient torrent and return an explicit rejection rather than admit it as a managed public torrent.

Rejecting private metadata cannot undo a public lookup made before metadata arrived. The guarantee therefore depends on the public-source eligibility decision: unknown or private-source magnets are not eligible. Do not describe metadata validation as retroactive privacy protection.

## Lifecycle and networking

### Ports and configuration

Retain the existing configured peer port for the private client. Add a separate configurable public peer port, defaulting to zero for an OS-assigned port. Accept zero or a valid port number and reject a duplicate fixed private/public port before startup. Port changes require restart under existing configuration conventions.

An OS-assigned public port avoids collisions on existing installations but does not supply stable manual forwarding. A fixed public port remains available for deployments that need it. Neither client performs automatic router port forwarding. Do not globally disable IPv6 as part of this fix.

Preserve the established private tracker identity behavior. Each underlying client receives its own configuration instance; shared application storage ownership does not mean reusing mutable library configuration objects.

### Startup, restore, and shutdown

Initialize both clients before restoring saved torrents. Restore each valid session entry into its classified client and reapply the existing selection and paused state. Preserve the piece-completion database so already-known pieces do not require a new acquisition or full rehash solely because the network client changed.

If either client initialization or session restoration fails, close all initialized clients and release shared resources. Return the real error instead of silently running only one client or falling back to another engine.

On normal shutdown, stop adapter background work and both clients before closing shared piece-completion storage. Shared resources must be closed by one owner, not independently by both clients.

### Magnet lifecycle

Retain the bounded metadata-only operation. Do not select payload files or persist a placeholder session during transient resolution. Cancellation or completion drops only a newly created transient torrent. An existing managed torrent remains intact.

Retain the current transient-to-managed handoff. Peer-retention or promotion machinery is outside this change; new public discovery must work after managed Add as well as during resolution.

## Error handling and exclusions

Malformed metainfo, disallowed magnet sources, unexpected private magnet metadata, bind failures, and restoration failures remain explicit errors. Preserve the current metadata and piece-wait deadlines.

Keep actual storage-write failures distinct from tracker announce diagnostics. This acquisition fix leaves the current tracker-error presentation unchanged; it does not hide warnings, state-gate them away, or treat their disappearance as success.

Excluded: speculative tracker-list pruning, public tracker injection into private metainfo, blanket IPv6 disablement, retry-policy changes, timeout inflation, tracker-health telemetry, UI redesign, title reconciliation, and release publication.

## Verification and acceptance criteria

### Controlled discovery and privacy regression

Use a controlled DHT network and seeder, with no working tracker and no direct peer-address injection into the downloader. Supply DHT bootstrap information rather than an `x.pe` shortcut.

The public path must discover a peer, resolve metadata, then transfer hash-verified payload bytes after managed Add. The regression must expose the pre-fix tracker-only limitation and pass with public discovery enabled.

Run private metainfo alongside public activity. Observe DHT traffic and peer-exchange behavior: the private hash must not enter DHT lookups or announcements, and the private client must neither send nor accept peer exchange. Demonstrate this across fresh admission and session restore, not just by asserting configuration fields.

Exercise the public-source eligibility boundary, existing privately managed hashes, and rejection of unexpected private metadata. Keep the source-trust limitation explicit in test claims.

### Persistence, concurrency, and streaming

Exercise concurrent Add/Resolve for one hash, cancellation, mixed public/private restore, paused selections, and removal. Check observable ownership and state preservation; no duplicate torrent or competing storage owner may appear.

Restart after verified pieces have arrived and confirm those bytes and selections survive. Exercise the real HTTP range path and confirm verified bytes can be served before the whole torrent completes.

Run affected Native, application, HTTP, configuration, and composition checks. Retain behavior-focused regressions for the discovery/privacy boundary and concurrency risks; do not add tests that merely pin source text or field wiring.

### Real network and Pi rollout

Use a known-live legally distributable torrent to exercise real public discovery and verified byte transfer. Keep test data isolated from household downloads and remove only verification data created for this task.

After local checks pass, build for the Pi, retain its previous binary, install the local fix build, and restart `torrent-tv`. Preserve the settings, session, media, and household database. Roll back the binary if startup or existing-download access regresses; do not roll back by deleting household state.

Inspect the existing downloads at `/library/downloads` on the user-authorized Pi base URL. Keep connection details in session environment variables, not committed files. Do not delete, re-create, or switch ownership of the stalled downloads. Record metadata availability, peers, received bytes, and progressive serving separately. Check existing private and qBittorrent-owned media remain accessible.

A successful DHT handshake alone is not acceptance. If the specific stalled swarms still yield no usable peers, continue investigating that path and report the limitation rather than declare the user's failure resolved. Distinguish a proven engine capability from an unproven live-swarm diagnosis.

## Affected boundaries and documentation

Primary changes belong in `internal/adapters/nativetorrent/`. Source eligibility crosses the existing application engine port and acquisition path; all affected implementations, composition wiring, and callers must be migrated together. Public-port configuration follows `internal/platform/config/` and existing settings propagation.

Update the existing Native engine documentation and relevant configuration/changelog entries after verification. Do not alter unrelated engine-ownership or catalog behavior. Remove throwaway verification artifacts without touching household data.

## Review gate

The user approved all three design sections and then reviewed and approved this written specification. Proceed with the writing-plans workflow and its execution choice. The deployment authorization remains limited to the local Pi rollout described above.
