# Portal Integration, Self-Update, and Release Publication Design

Date: 2026-09-05
Status: Revised written specification approved

## Goal

Provide server-side integration for accounts, household supporter status, promotions, project links, and server self-update. Publish new streaming-server releases to the external update feed automatically, without maintaining release notices by hand.

This revision preserves the previously approved product behavior and records the subsequently approved trust, release-publication, and macOS update decisions. It replaces the earlier specification's inaccurate claims about shared desktop frontend ownership, checksum filenames, container detection, and executable-only macOS bundle updates.

## Product behavior

- The desktop GUI automatically starts its embedded server when required settings are complete. Incomplete setup remains in the setup flow; no manual Start click is required after a configured launch.
- All clients call their configured streaming server. Only the server-side adapter calls the fixed external integration service. Its address is hardcoded, not configurable. UI copy and documentation do not name the external platform.
- Web and desktop have login and registration. Tizen does not. JWT identity is separate from the supporter API key; JWTs expire according to the upstream response, currently after 24 hours. Clients support sign-out and remove expired sessions.
- The household supporter API key is stored in the protected server settings file and redacted from all responses. The entire account Settings section, including the API-key field, disappears when accounts are disabled or unavailable. TV has no account settings controls.
- Donor status is queried with the API key, not the JWT. A valid donor hides promotions across the household. Expiry must take effect without waiting for the next hourly check. Failed donor lookup never invents donor privileges.
- Promotions occupy a compact area near the bottom of the left navigation. Their delivery remains live because delivery records an upstream impression. No background prefetching of unseen creatives. TV promotions are display-only.
- Other Projects contains the ordered public links. Web and desktop can open safe external destinations; TV opens a remote-navigable URL dialog with reliable Back/focus restoration, not an assumption that text selection works on a television.
- Feature failure is endpoint-specific: failed public settings disables accounts and promotions; failed links removes project links; failed update fetch clears update availability; failed promotion delivery removes the promotion surface. Failures produce no global warning or reserved empty panel. Subsequent successful probes restore the relevant capability.
- Every update notice includes version, release notes, the server-only warning, and https://github.com/mihaiflorentin88/torrent-tv/releases for manual TV-client updates. Updates interrupt active playback; explicit apply requires a warning/confirmation. Controls cover checking, current, available, applying, reconnecting, failed, and manual-only states.

## Architecture and ownership

`internal/application/portal` defines integration contracts and owns collapsed state. `internal/adapters/portalclient` implements the upstream HTTP boundary. `internal/application/updates` owns update selection and operations; platform installation and process handoff are separate from HTTP request handling. Composition injects build identity, settings access, events, and lifecycle dependencies. Application packages never import composition.

The web application and desktop shell are separate Preact entry points. Desktop lives in `desktop/src` and imports selected shared web components, including Settings, using the configured shared API origin. Both entry points require integration work. Tizen has its own navigation and presentation. Reuse common components where actual component ownership permits it; do not import the web App into the desktop shell.

The existing SSE journal and `domain.Event` envelope remain authoritative. Portal state and update notifications use it rather than a second event bus. Clients unwrap the event payload and refetch current snapshots on reconnect to avoid treating replayed historical state as current.

Hub refresh is cancellable and serialized. State starts inactive, including explicit absence of an update notice. Returned slices do not alias cached state. Credential changes invalidate in-flight donor responses. Settings access follows data-directory relocation rather than retaining an obsolete store.

## Self-update policy

### Timing

Ordinary startup binds and serves first, then performs one bounded, asynchronous newer-version check and automatic installation. An unavailable upstream cannot prevent serving. There is no setting to disable startup auto-install.

An hourly jittered check only notifies; it does not install. An explicit check performs a fresh fetch. An explicit apply refreshes before selecting an artifact. An already-current apply is a successful no-op.

The headless `--update` flow is update-and-serve: perform the blocking check/install before normal serving, then run the new binary when applicable. It is not a check-only command. Preserve arguments/data-directory identity across handoff without repeatedly applying the same operation.

### Trusted artifacts

The user selected repository releases only. The external feed announces the version; it does not gain permission to choose arbitrary executable code.

Accept update artifacts only from the GitHub releases of `mihaiflorentin88/torrent-tv`. Require the exact selected asset to have a valid entry in the same release's `SHA256SUMS`, and verify its bytes before extraction or installation. A missing manifest, missing entry, wrong repository/tag, malformed hash, or mismatch fails closed. HTTPS alone and a locally computed hash without an expected value are not verification. Build-provenance attestations are published by the existing workflow but are not required by the selected policy.

Normalize valid release tags consistently and use proper semantic-version precedence. Do not strip prerelease suffixes to compare version triples. Unversioned development builds cannot safely auto-install based on a working-directory VERSION file.

### Platform and build flavor

Selection includes running version, OS, architecture, and build/install flavor. A GUI build cannot silently replace a headless build. Add a release artifact for the Pi's Linux ARM64 headless flavor, since the existing Linux ARM64 release artifact is a GUI build with dynamic WebKit dependencies.

The current upstream platform set lacks ARMv7. Without a representable matching artifact it remains notification-only; do not bake permanent incapability into generic version comparison or pretend another architecture is compatible.

The user selected full macOS `.app` replacement. Install the complete released bundle with its signature intact, using an external restart helper and recovery procedure. Do not replace only `Contents/MacOS` or locally re-sign the bundle. Raw macOS executable distributions remain a separate install flavor. Verify real macOS signature, quarantine, relaunch, and recovery behavior before claiming support.

Containers are explicitly notification-only. Writability alone does not identify a container. Non-writable or unsupported installs show the manual-update route rather than attempt privilege escalation.

### Installation and process handoff

Stream downloads to bounded staging files; do not allocate or copy entire binaries in memory. Stage on the destination filesystem. Validate archive member paths, kinds, sizes, expected executable/bundle identity, OS, architecture, and flavor. An archive is never written directly to the executable path.

Use platform-specific replacement primitives and durable recovery metadata. Preserve a usable previous installation until a new process confirms healthy startup. A failed download, verification, staging, or handoff must not be reported as a successful update. Rollback and backup cleanup follow confirmed health, not merely entering main.

Applying is an operation owned by the process coordinator, not the initiating HTTP request. The accepted response reaches the client before shutdown drains handlers. Cancel and join background integration/application work before closing engine and repository resources. GUI teardown releases its single-instance lock before the new instance starts; Windows requires an old-process-exit handoff, not racing a new process against a still-held lock.

Systemd installations move to `/var/lib/filelist-streaming/bin/filelist-streaming`, with service-user ownership and `Restart=always`. One approved redeploy migrates existing installations. Both bootstrap and Pi deployment paths, saved historic defaults, custom paths, and rollback behavior must be updated consistently. No sudo/pkexec updater path is introduced. Plain headless and GUI processes use the appropriate platform-specific handoff; systemd exits cleanly for its supervisor to relaunch.

## Local API

- `GET /api/v1/portal/state`: cached collapsed capabilities, donor state, ordered links; arrays are never null.
- `GET /api/v1/portal/promotions?count=N`: gated live delivery, normalized `screenTime` on the local wire.
- `GET /api/v1/portal/promotions/{provider}/{id}/click`: server resolves tracking and redirects to a validated destination without exposing the upstream platform URL.
- `POST /api/v1/portal/session`, `POST /api/v1/portal/session/register`, `GET /api/v1/portal/session/me`: gated identity operations. Credential rejection is a form error; transport failure is capability unavailability.
- `GET /api/v1/updates/current`: cached status, no hidden network or filesystem probes.
- `POST /api/v1/updates/check`: fresh check, no installation.
- `POST /api/v1/updates/apply`: 202 accepted operation, 200 current no-op, 409 busy/manual-only; neutral problem responses for failures.

OpenAPI and AsyncAPI must describe the actual DTOs and event envelope. Settings remain secret-redacted on both HTTP and native GUI bindings.

## Automatic release publication

The user selected workflow push plus reconciliation. This explicitly expands the earlier non-goal that excluded changes to the external service.

### Normal path

1. The existing streaming repository release workflow builds and uploads all expected distribution assets and `SHA256SUMS`.
2. Only after successful release publication/upload, it notifies a narrowly authenticated synchronization endpoint on the external service, identifying the release tag.
3. The service fetches authoritative metadata from the fixed streaming GitHub repository. The caller cannot supply arbitrary download URLs, repository identities, release notes, or platform mappings.
4. Validate a published, non-draft, non-prerelease semantic version, expected asset set, and checksum-manifest membership. Obtain notes and publication time from GitHub, then publish through the existing update domain service.
5. The public update endpoint exposes the new notice immediately after the atomic save. Target latency is seconds after completed publication under normal network conditions, not a guarantee during outages.

Do not automate browser login or post an admin dashboard form. Add a machine-to-machine endpoint with a dedicated secret, independent of user JWTs and supporter API keys. Keep the secret in GitHub Actions secrets and the external service's server-side configuration; use constant-time comparison, bounded requests, and no secret logging. Missing credentials disable the endpoint rather than making it public.

### Recovery and ordering

Run reconciliation on service startup and every five minutes, with bounded GitHub requests and conditional fetching where useful. This recovers missed notifications, temporary outages, incomplete uploads, and releases created outside the workflow. Workflow notification failures get bounded retries and a visible CI failure; reconciliation prevents requiring a human rerun for eventual publication.

Use one serialized/idempotent publication path for notifications and reconciliation. Repeated delivery of the same release is harmless. Older events cannot replace a newer notice. Incomplete assets do not replace the last valid notice; retry on subsequent reconciliation. Synchronization failure preserves the last successfully published notice on the external service; this differs from a streaming client failing to fetch its current upstream snapshot.

The public notice's version, notes, timestamp, and asset mapping are GitHub-owned in automatic mode. Manual editing must not compete silently with the next reconciliation; make source ownership visible in the existing editor and prevent editing automatically managed fields. No additional manual promotion step is required per release.

A release created using the workflow's `GITHUB_TOKEN` does not reliably trigger a second `release: published` Actions workflow. Put notification in the existing successful publication path, not in a dependent event workflow requiring a broader personal token.

### Operating boundary

One initial deployment/configuration of the external service and one secret installation are required. Do not deploy either service, set remote secrets, or change GitHub webhook/settings configuration during planning. The external service is not being given binary self-update or automatic redeployment by this feature; it synchronizes release notices only.

GitHub configuration supplied by the user: environment `prod`, secret `FL_ADS_APIKEY`. A separate notification job declares `environment: prod` and reads `${{ secrets.FL_ADS_APIKEY }}` only through its environment. Never embed the value in YAML, scripts, documentation, URLs, command arguments, or logs. Environment protection rules may delay notification; reconciliation remains the recovery path. Do not change those protection rules automatically. The external service must separately be configured with the matching release-sync credential.

## Verification and acceptance

Plan review must map every requirement above to a concrete task. Source implementation has not started.

- Updater regressions exercise corrupt/missing checksums, wrong flavor/architecture, archive traversal, failed swaps, concurrent applies, current no-op, failed handoff, and recovery with real subprocess fixtures.
- Release-publication scenarios exercise duplicate/out-of-order notices, stable versus draft/prerelease, incomplete upload followed by recovery, GitHub outage, wrong credentials, and push versus reconciliation racing. Observe the public GET endpoint after successful publication.
- Run the real GUI to prove configured auto-start and setup behavior. Exercise all three UI surfaces for enabled, disabled, donor, endpoint-failure/recovery, applying/reconnect, and manual-only states. Browser verification does not substitute for native restart or physical-TV verification.
- macOS bundle and Windows process-handoff claims require native runtime evidence. Do not claim them based only on cross-compilation. Physical TV/deployment access requires approval; record unavailable runtime evidence as a blocker, not a pass.
- Documentation uses neutral naming. Apply the same server-only/manual-TV update notice everywhere.
