# Server-side integration, self-update, and automatic release publication

---
status: accepted
---

## Decision

All external integration runs through `internal/adapters/portalclient`. Application contracts and state belong to `internal/application/portal`; clients use neutral local HTTP routes and the existing SSE journal. Web, the separate desktop shell, and Tizen never call the upstream directly. The upstream address is fixed in the adapter, not configurable. UI and documentation use neutral names.

Accounts exist on web and desktop only. JWT identity is separate from the server-stored household supporter key. Accounts-disabled or account-unavailable state hides the complete account surface, including the key input. Valid household donor status hides promotions on all clients. Endpoint failures disable only the corresponding feature; public-settings failure disables accounts and promotions. Donor expiry is time-sensitive, not deferred until the next hourly refresh.

The updater runs one asynchronous automatic attempt after readiness, checks hourly with jitter for notification only, and supports explicit apply on all clients. Headless `--update` means update-and-serve. Already-current is a successful no-op. Applying interrupts playback and restarts the whole desktop process when the server is embedded. Every notice explains that TV applications update manually and links to the repository releases page.

Only release assets from `mihaiflorentin88/torrent-tv` are trusted. Matching entries in the same release's `SHA256SUMS` are mandatory. Missing or mismatched verification data aborts installation. Published provenance attestations are not required by this policy.

Build identity includes version, OS, architecture, and flavor. Add a Linux ARM64 headless release artifact so Pi servers cannot receive a GUI binary. macOS `.app` installs replace the complete signed bundle through a helper, not just its executable. Use staged, validated, recoverable platform-specific installation and process handoff. Keep the previous installation until new-process health is confirmed. Never restart synchronously inside the apply HTTP handler.

Systemd installations use a service-owned `/var/lib/filelist-streaming/bin/filelist-streaming` and `Restart=always`; one redeploy migrates existing installations. Update bootstrap and Pi deployment together. No privilege-escalating updater is introduced. Containers and unsupported/non-writable installs are notification-only; writability is not a reliable container detector. ARMv7 is notification-only while the feed cannot represent its artifact, not an unconditional permanent limitation.

The existing GitHub release workflow notifies a narrowly authenticated external synchronization endpoint after successful asset publication. The external service resolves metadata from the fixed GitHub repository, validates stable complete releases, and publishes through its existing domain service. A five-minute reconciliation loop and startup reconciliation recover missed notifications and temporary outages. Duplicate delivery is harmless, and older releases cannot replace newer notices. Automatic synchronization owns the GitHub-derived notice fields; the admin editor must not silently compete with it.

Use a separate notification job with `environment: prod` and `${{ secrets.FL_ADS_APIKEY }}`. The user has configured that environment secret. No secret value belongs in the repository, process arguments, URLs, or logs. The external sync endpoint needs the matching credential configured separately. Do not modify environment protection rules to obtain faster notification. Reconciliation remains independent of those gates.

## Evidence

- Upstream donor lookup authenticates an API key, not a JWT.
- The existing release job generates `SHA256SUMS` and uploads it with the release assets.
- Published Linux ARM64 assets are GUI builds, while Pi deployment builds headless locally.
- The macOS distribution is a packaged, ad-hoc-signed bundle; executable-only replacement would invalidate its seal.
- Existing systemd configuration uses a root-owned executable and `Restart=on-failure`, which cannot support the approved unprivileged clean-exit update flow without migration.
- External update publication currently uses an admin-session-protected form. A public read route is not a machine publication API.
- GitHub documents that events produced with `GITHUB_TOKEN` generally do not trigger another workflow. Notification therefore follows the existing publication job rather than depending on a second `release: published` workflow.

## Rejected alternatives

- Direct client integration: duplicates state/degradation logic and prevents centralized household donor handling.
- Arbitrary HTTPS executable URLs or optional checksums: give an announcement service unnecessary code-execution authority.
- Binary-only macOS bundle modification or local re-signing: do not preserve the released bundle as approved.
- GUI-only Linux ARM64 selection for a headless Pi: installs an incompatible dependency flavor.
- sudo/pkexec installation: unnecessarily weakens the service boundary.
- Polling-only publication: adds delay to every normal release.
- Webhook plus reconciliation: viable, but requires separate repository webhook setup; the existing workflow already knows when asset upload is complete.
- Admin-form automation or an embedded admin credential: couples CI to browser authentication and exposes more authority than release synchronization needs.

## Consequences

This supersedes the earlier GUI auto-update non-goal and expands scope to changes in the external service for release-notice synchronization. It does not add automatic deployment or binary self-update to that service.

Release publication normally reaches the feed shortly after asset upload; outages and environment approval gates can delay it. Failed synchronization preserves the last valid external notice, while a streaming server that fails to fetch updates clears its local availability state.

Native macOS/Windows update handoff and actual UI surfaces require runtime verification. Cross-compilation alone is not evidence that restart and recovery work. Planning does not authorize deployment to the Pi, interaction with physical TVs, or remote configuration changes.

Full specification: `docs/superpowers/specs/2026-09-05-portal-integration-self-update-design.md`.
