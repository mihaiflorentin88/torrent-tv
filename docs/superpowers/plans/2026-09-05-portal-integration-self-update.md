# Portal Integration, Self-Update, and Release Publication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate accounts, household supporter status, promotions, project links, and recoverable server self-update across web, desktop, and Tizen, and automatically synchronize complete GitHub releases into the external update feed.

**Architecture:** Application-owned contracts separate the fixed upstream adapter, integration state, updater, and process lifecycle. Clients use the configured server's HTTP/SSE surface. The existing release workflow notifies the external service after asset upload; a five-minute reconciliation loop recovers missed publication.

**Tech Stack:** Go 1.26.0, Cobra v1.10.2, Wails v3.0.0-beta.16, Preact/TypeScript, Node >=24, PostgreSQL in the external service, SQLite in the streaming server, GitHub Actions, systemd.

**Spec:** `docs/superpowers/specs/2026-09-05-portal-integration-self-update-design.md`; decision: `docs/adr/0008-portal-integration-and-self-update.md`.

This replaces the withdrawn draft, including its invalid code and unsupported self-review claims. Code blocks below lock shared contracts and selected critical algorithms, not a claim that application changes have been implemented or run. Executors must read the specification and current source before editing. Use language-server references before changing exported symbols; do not assume line numbers remain stable.

## Global constraints

- External integration is server-side; the fixed address appears only in the Go adapter. No setting, environment override, or client-side upstream URL. Documentation/UI use neutral naming.
- Web and desktop have accounts; Tizen has no login, registration, or supporter-key control. Hide the entire account section when accounts are inactive, including the key field.
- JWT identity and server-stored supporter key are independent. Secrets are redacted from HTTP and native settings responses, SSE, errors, and diagnostics.
- Endpoint failures remove only their corresponding surface; public-settings failure removes accounts and promotions. Recovery restores capabilities. Donor expiry takes effect on time.
- Startup applies automatically after readiness and fails open. Hourly jittered checks only notify. `--update` is update-and-serve. There is no auto-update disable preference.
- Every update notice says server-only, warns that TV applications update manually, and links to https://github.com/mihaiflorentin88/torrent-tv/releases. Explicit apply warns about playback interruption.
- Only repository release artifacts are accepted, with mandatory matching `SHA256SUMS` from the same release. No provenance-verification service is required.
- Select OS, architecture, version, and GUI/headless/bundle flavor. Add the Linux ARM64 headless release asset. Full macOS bundle replacement is required; binary-only bundle mutation and local re-signing are prohibited.
- Keep the last working installation until the replacement confirms healthy startup. Native handoff and rollback require native runtime evidence.
- Systemd uses service-owned `/var/lib/filelist-streaming/bin/filelist-streaming`, `Restart=always`, and one-time migration. No sudo/pkexec updater. Containers and unsupported/non-writable installations are notification-only.
- GitHub environment `prod` and secret `FL_ADS_APIKEY` already exist according to the user. Reference the secret name only. Do not retrieve, print, commit, or put credential values in process arguments.
- No deployment, physical-TV operation, remote secret provisioning, or environment-protection changes without separate approval.

## Repositories and dependency order

`S` means this streaming repository. `E` means the adjacent external-service repository, not a new directory within S. Read E's applicable repository guidance at implementation time.

Dependency edges: S1 → S3; S2 → S3; S3 → S6; S4 → S5 → S6; S6 → S7 and S8; S8 → S9; S9 → S10; S8 → S11; E1 → E2; S4 → R1; E2 and R1 → R2; all → V1. E1 and R1 share the asset table in this document, not a runtime dependency on one another. S9 and S11 can run concurrently; S10 requires the shared components produced by S9.

Each task ends with its targeted verification and a scoped commit. Never commit generated assets or unrelated working-tree changes merely because a build touched them. Test names below identify behavioral regressions to add in the indicated files; fixtures must use real package constructors and existing HTTP test-server patterns, not undefined generic helpers.

## Shared local contracts

Application contract owner: `internal/application/portal/types.go`. These types are neutral local types; upstream snake-case DTOs are private to the adapter.

```go
package portal

import (
    "context"
    "time"
)

type Link struct {
    ID int64 `json:"id"`
    Title string `json:"title"`
    URL string `json:"url"`
    Description string `json:"description"`
}
type Snapshot struct {
    AccountsEnabled bool `json:"accountsEnabled"`
    AdsEnabled bool `json:"adsEnabled"`
    Donor bool `json:"donor"`
    Links []Link `json:"links"`
}
type Promotion struct {
    ID string `json:"id"`
    Provider string `json:"provider"`
    Title string `json:"title"`
    Text string `json:"text"`
    Image string `json:"image"`
    ScreenTime int `json:"screenTime"`
}
type Binary struct {
    Platform string `json:"platform"`
    DownloadURL string `json:"download_url"`
}
type Notice struct {
    Version string `json:"version"`
    Notes string `json:"notes"`
    ReleasedAt time.Time `json:"released_at"`
    DownloadURL string `json:"download_url"`
    Binaries []Binary `json:"binaries"`
}
type Session struct {
    Token string `json:"token"`
    ExpiresAt time.Time `json:"expires_at"`
}
type User struct {
    ID int64 `json:"id"`
    Email string `json:"email"`
    DisplayName string `json:"display_name"`
    Role string `json:"role"`
}
type AccountStatus struct {
    Donor bool
    DonorUntil *time.Time
}
type PublicSettings struct {
    AccountsEnabled bool
    AdsEnabled bool
}
type Client interface {
    Settings(context.Context) (PublicSettings, error)
    Links(context.Context) ([]Link, error)
    Notice(context.Context) (Notice, error)
    Promotions(context.Context, int) ([]Promotion, error)
    PromotionAvailability(context.Context) (bool, error)
    Click(context.Context, string, string) (string, error)
    AccountStatus(context.Context, string) (AccountStatus, error)
    Login(context.Context, string, string) (Session, error)
    Register(context.Context, string, string, string) error
    Me(context.Context, string) (User, error)
}
```

`PromotionAvailability` uses the upstream non-impression availability/weights operation. It must not call delivery to recover a hidden slot. If no creative exists, the local result is an empty array and an absent slot, not a fabricated creative.

`Hub` methods: `Snapshot() Snapshot`, `Refresh(context.Context) error`, `RefreshNotice(context.Context) (Notice, bool, error)`, `Notice() (Notice, bool)`, `Run(context.Context) error`; gated client operations retain the signatures above. A bool distinguishes a missing notice from a valid zero value. Composition injects a live settings reader, clock, jitter function, HTTP client, and event sink; there is no application import of composition.

Updater contract owner: `internal/application/updates/types.go`.

```go
package updates

import "context"

type Identity struct {
    Version string
    GOOS string
    GOARCH string
    Flavor string // gui, headless, or bundle
}
type Status struct {
    CurrentVersion string `json:"currentVersion"`
    Available bool `json:"available"`
    Latest string `json:"latest,omitempty"`
    Notes string `json:"notes,omitempty"`
    ReleasedAt string `json:"releasedAt,omitempty"`
    ReleasesURL string `json:"releasesUrl"`
    SelfUpdate bool `json:"selfUpdate"`
    Applying bool `json:"applying"`
}
type ApplyResult struct {
    Accepted bool
    Status Status
}
type API interface {
    Current() Status
    Check(context.Context) (Status, error)
    Apply(context.Context) (ApplyResult, error)
}
```

HTTP errors distinguish busy/manual-only conflicts from upstream, verification, and installation failures. No invented progress percentage is exposed. Client-local checking/confirmation/reconnect state is separate from server status. A failed background operation emits a journaled `updates.failed` event carrying a neutral `message`, then an `updates.status` snapshot with `applying:false`. `portal.state` carries Snapshot; `updates.status` carries Status.

Existing SSE is `domain.Event{id, kind, payload, createdAt}` with `payload` a JSON string. Parse the envelope then the payload. Refetch current snapshots on reconnect before displaying replayed availability.

| Local endpoint | Success | Failure behavior |
|---|---|---|
| GET `/api/v1/portal/state` | 200 Snapshot, cached | inactive features represented as false/[] |
| GET `/api/v1/portal/promotions?count=N` | 200 Promotion[] | empty/inactive for delivery unavailability |
| GET `/api/v1/portal/promotions/{provider}/{id}/click` | 302 safe destination | no unsafe/open redirect fallback |
| POST `/api/v1/portal/session` | 200 Session | credential rejection stays a form error |
| POST `/api/v1/portal/session/register` | 201, no token | registration does not imply login |
| GET `/api/v1/portal/session/me` | 200 User | invalid/expired session clears client identity |
| GET `/api/v1/updates/current` | 200 Status, cached | no disk or network probe |
| POST `/api/v1/updates/check` | 200 Status | neutral problem; no stale availability |
| POST `/api/v1/updates/apply` | 202 accepted or 200 current | 409 busy/manual-only; other failures neutral problems |

Sign-out is client-local; do not add a server session cache or DELETE endpoint.

## S1: Supporter-key persistence and secret redaction

**Files, S:** modify `internal/platform/config/config.go`, `internal/platform/config/config_test.go`, `internal/adapters/httpapi/schema.go`, `internal/adapters/httpapi/api_test.go`; native settings responses must reuse the same redaction.

**Produces:** `config.Settings.PortalAPIKey` with JSON `portalAPIKey`, and `SettingsView.PortalAPIKeyConfigured` with JSON `portalAPIKeyConfigured`.

- [ ] Add `TestPortalAPIKeyPreservationAndRedaction`: load a temporary store with the real `config.LoadAt`, save a key, reload, save a blank key, verify the original remains; assert HTTP/native redacted settings contain no plaintext and indicate configured state. Follow existing secret file-permission tests.
- [ ] Run `go test ./internal/platform/config ./internal/adapters/httpapi -run PortalAPIKey`; verify a contract failure before changing production code.
- [ ] Add the field to existing secret merge/redaction paths. The merge rule is `if next.PortalAPIKey == "" { next.PortalAPIKey = old.PortalAPIKey }`. Redaction first records configured state, then blanks the value. Schema marks the field sensitive and not restart-required. No environment alias or separate store.
- [ ] Run both packages' existing tests. Ensure key changes trigger hub refresh through the current settings-change seam, without leaking the submitted value into events. Commit S1.

## S2: Typed, bounded upstream adapter

**Files, S:** create `internal/application/portal/types.go`, `internal/adapters/portalclient/client.go`, `internal/adapters/portalclient/client_test.go`.

**Consumes/produces:** implements the Client contract above. Private upstream DTOs map `screen_time` to `screenTime`, nested flags to booleans, and donor expiry to `time.Time`.

- [ ] Add transport-backed tests for malformed/oversized JSON, deadline/503, update 404, empty promotion pool, rejected credentials, and JWT versus API-key authorization. An injected `http.Client` transport intercepts the fixed host; no configurable production base URL.
- [ ] Implement five-second request deadlines, bounded body reads with overflow detection, safe status errors, and context propagation. Treat a missing notice separately from network failure. Never include credentials or upstream response bodies in user errors.
- [ ] Decode upstream contracts without conflating registration and login. The click operation observes the upstream redirect without automatically following it, validates HTTP(S) destination and host presence, then returns only that destination. Validate path segments before constructing requests.
- [ ] Preserve project order, return non-nil empty arrays, render remote content as text, and validate external URL/image formats against the actual upstream contract. Availability checks must not record impressions.
- [ ] Run `go test ./internal/adapters/portalclient`; commit S2.

## S3: Cancellable integration hub and event lifetime

**Files, S:** create `internal/application/portal/hub.go`, `internal/application/portal/hub_test.go`; modify `internal/application/service.go` and its existing tests.

**Consumes:** Client, live settings reader, clock, jitter, event sink. **Produces:** Hub operations and `portal.state`; notice refresh feeds updater checks without controlling install eligibility.

- [ ] Add regression scenarios for initial inactive state, independent failure/recovery, donor expiry, key replacement while a request is in flight, caller mutation of snapshot slices, cancellation during refresh, and shutdown before any refresh began.
- [ ] Serialize refresh state changes. Do not hold state mutexes over network calls. Use generation checks so stale key/settings responses cannot resurrect old donor state. Keep cached notice presence explicit and clear it on a failed notice fetch.
- [ ] Drive the hourly refresh with bounded nonzero jitter; schedule donor expiry at its actual time. Public-settings failure clears accounts/promotions; links/update errors leave unrelated state alone. A 401 login is not a service outage. Any true account transport outage hides account controls; a later successful health probe restores the gate.
- [ ] On a promotion delivery failure hide the slot and recover via the non-impression probe. Refresh after settings changes without waiting an hour. `Run(ctx)` is blocking/cancellable; composition owns and joins it, avoiding a Stop-before-Start deadlock.
- [ ] Expose the existing service publisher as the event-sink boundary. Add cancellation and joins for title-refresh, tracker-search, metadata workers, scheduler, and in-flight catalog/job goroutines; no journal write may occur after repository close. Implement idempotent `Service.Close(ctx context.Context) error`. A join timeout aborts handoff rather than closing the database under active writers. Close ordering is workers, engine, repository.
- [ ] Run `go test -race ./internal/application/...`; commit S3.

## S4: Release identity and trusted asset resolution

**Files, S:** create `internal/application/updates/types.go`, `version.go`, `release.go`, `release_test.go`; modify build identity injection in `internal/composition/container.go`, `Makefile`, and `.github/workflows/release.yml` through R1.

**Consumes:** Identity and a portal Notice. **Produces:** an exact repository/tag/asset/checksum selection, never an arbitrary feed URL.

- [ ] Add release-resolution regressions for current/no-op, older release, malformed version, prerelease precedence, wrong repository, wrong tag, missing checksum, duplicate checksum entry, and GUI/headless mismatch. Use a temporary GitHub-response transport, not live GitHub.
- [ ] Add `golang.org/x/mod/semver` as a pinned direct dependency in go.mod/go.sum (use the same version in E). Validate with `semver.IsValid` and compare with `semver.Compare`; normalize a single leading `v`. Require full major.minor.patch release versions before accepting the library comparison. Preserve prerelease precedence. Do not compare arrays or strip prerelease components. `dev`/invalid build identity is manual-only.
- [ ] Resolve repository release metadata from the fixed repository. Reject draft/prerelease for automatic stable updates. Select assets from R1's table, require an exact `SHA256SUMS` entry, validate 64 hexadecimal digits, and reject duplicate filenames. Do not silently continue without a manifest.
- [ ] Keep the public notice as an announcement hint. Match selected version/tag and release asset identity against GitHub; do not download a different URL merely because the notice says so. Bound redirects to expected GitHub release-download infrastructure, require HTTPS throughout, and never forward integration credentials.
- [ ] Run `go test ./internal/application/updates -run 'Release|Version|Checksum'`; commit S4. R1 implements build plumbing against the identity contract above and is not needed to test resolver fixtures.

## S5: Bounded staging, archive validation, and recoverable install

**Files, S:** create `internal/application/updates/stage.go`, `stage_test.go`, `install_unix.go`, `install_windows.go`, `install_darwin.go`, `recovery.go`, `recovery_test.go`.

**Produces:** verified staged installation plus durable transaction metadata. Destination is derived from the running executable/bundle, never user-submitted HTTP paths.

- [ ] Reproduce corrupt download, oversized stream, truncated archive, traversal, absolute path, escaping link, duplicate member, wrong executable architecture/flavor, interrupted replacement, and rollback failure using temporary installation directories. A plausible failure must preserve either the old live path or a recoverable durable backup.
- [ ] Download to a unique mode-0600 file in the destination filesystem. Hash while streaming to disk; do not hash then read an exhausted response or retain the full executable in memory. Enforce a 512 MiB compressed limit and 1 GiB expanded limit without an invented minimum size.
- [ ] Verify checksum before extraction. Raw archives allow only expected regular files/directories; reject links. macOS bundles preserve required internal framework symlinks only when their resolved targets remain inside the extracted bundle. Reject devices, traversal, absolute names, duplicate paths, excessive entries, and cumulative expanded-size overflow.
- [ ] Verify PE/ELF/Mach-O identity and the embedded build flavor/version before replacing the running install. Validation must not execute unverified archive content. For `.app`, verify bundle identity and `codesign --verify --deep --strict` on the staged bundle; do not re-sign or indiscriminately remove quarantine attributes.
- [ ] For Unix files, preserve the previous file using a same-filesystem backup before atomically renaming the staged file over the live name; never rename the live file away first. Flush file and directory metadata. Persist an operation ID and recovery phase before mutations. Use exclusive staging/operation ownership, not a fixed writable-probe filename.
- [ ] For Windows and full macOS bundles, use a helper outside the replaced path that waits for old-process exit. The helper performs swap, launch, bounded health acknowledgement, and rollback. On macOS use atomic directory exchange where supported; if unavailable, declare manual-only rather than silently use a crash-unsafe directory gap. On Windows restore the old executable if replacement/launch fails.
- [ ] Health acknowledgement identifies operation and new version and occurs after readiness. Timeout or early process death restores the backup and relaunches the previous installation with startup-update suppression for that recovery operation only. Successful health removes backup and transaction debris.
- [ ] Run `go test ./internal/application/updates -run 'Stage|Archive|Recovery|Install'`. Native platform smoke gates remain mandatory in V1. Commit S5.

## S6: Update coordinator, readiness, and process ownership

**Files, S:** create `internal/application/updates/manager.go`, `manager_test.go`; modify `internal/composition/container.go`, `cmd/server/main.go`, `internal/gui/runner.go`, `internal/gui/supervisor.go`, `internal/gui/bindings.go` and their existing tests.

**Consumes:** S3 lifetime, S4 selection, S5 installer. **Produces:** API implementation, `updates.status`, `updates.failed`, startup/explicit handoff.

- [ ] Add regressions for simultaneous applies, already-current success, startup upstream failure while serving, hourly check that never installs, accepted apply surviving client disconnect, and worker shutdown preceding repository close. Use a real listener and subprocess fixture for lifecycle behavior; mocks cannot prove lock release/restart.
- [ ] Construct all dependencies before binding. After listener readiness schedule one startup apply independently of notification watermarks. Hourly checks call Check only. `Current()` returns immutable cached status and never probes disk/network. Probe supported install capability at initialization, not on GET.
- [ ] Apply owns a single operation token. Request cancellation may cancel pre-acceptance checking, but once accepted the process context owns installation. Publish applying state before returning 202. The HTTP handler flushes the response before releasing a handoff barrier; the coordinator drains HTTP before closing workers/resources. No shutdown inside the handler.
- [ ] For explicit `--update`, resolve/install before normal serving and then serve from the resulting installation. Preserve data directory and original invocation; remove only internal operation markers. Do not double-restart through both CLI and manager.
- [ ] Desktop supervisor access is synchronized. The actual `appAdapter` supplies lifecycle methods; do not add an accessor on an unrelated wrapper or a field/method name collision. On configured GUI launch call the existing supervisor start path exactly once; incomplete settings remain in setup. Release the single-instance lock before helper relaunch.
- [ ] Systemd exits cleanly for Restart=always; plain headless uses appropriate exec/handoff; GUI restarts the whole application. Handoff errors become observable operation failures, not ignored log lines.
- [ ] Run `go test -race ./internal/application/... ./internal/composition/... ./internal/gui/... ./cmd/server/...`; launch configured and incomplete GUI paths in V1. Commit S6.

## S7: Service-owned installation migration

**Files, S:** modify `deploy/systemd/filelist-streaming.service`, `deploy/pi-deploy.sh`, `deploy/bootstrap-server.sh`, `Makefile`, and existing deployment documentation.

**Consumes:** S6 clean-exit supervisor behavior. **Produces:** one-time migration to a writable service-owned install without privilege escalation at update time.

- [ ] Exercise deployment scripts against a temporary fake remote-command harness: fresh install, old `/usr/local/bin` default, saved historic config, custom executable path, interrupted install, and redeploy. Assert generated service paths and ownership intent, not exact incidental log wording.
- [ ] Change executable path to `/var/lib/filelist-streaming/bin/filelist-streaming`, user/group ownership to the service user, and `Restart=always`. Ensure sandbox write paths permit staging/backup alongside it and preserve `NoNewPrivileges`.
- [ ] Migrate saved default paths as well as fresh defaults; preserve explicit custom-path intent and report manual-only capability when ownership cannot safely be changed. Stage a valid binary before changing the unit. On deployment failure restore previous unit and binary references.
- [ ] Quote expanded target variables correctly across local and remote shell boundaries; do not emit a literal `$target` destination. Use the new ARM64 headless artifact for Pi deployment. Bootstrap builds with `-tags headless`, injects the VERSION-derived linker version and headless flavor, and installs under the service-owned bin directory; it must not produce an unversioned GUI-flavor server. Add AMD64 headless release coverage for the bootstrap-supported AMD64 server as well.
- [ ] Run `bash -n` on changed shell entry points and the deployment harness. Do not invoke `make deploy-pi` or contact the Pi during this task. Commit S7.

## S8: Local HTTP routes, SSE, and documented schemas

**Files, S:** modify `internal/adapters/httpapi/api.go`, `api_test.go`, `schema.go`, `api/openapi.yaml`, `api/asyncapi.yaml`, and composition wiring; create `internal/adapters/httpapi/portal.go` and `updates.go` for cohesive handlers.

**Consumes:** exact shared contracts and S6 API. **Produces:** the route table above; no alternate route aliases.

- [ ] Add HTTP regressions for absent gates, registration without JWT, bearer forwarding only to identity, promotion click destination safety, cached GET with no outbound request, current 200 versus accepted 202 versus conflict 409, and SSE envelope decoding. Use existing real `httptest.Server` construction.
- [ ] Mount leading-slash local routes, apply existing request decoding/problem conventions, and cap body/query values. Account transport failure updates the account gate; normal credential rejection does not. No API key ever enters a response.
- [ ] Return Snapshot/Status directly with agreed tags. Register returns 201 with no token; clients explicitly sign in afterward. A successful sign-in may call `/session/me` to obtain identity, rather than pretend Login returned User.
- [ ] Wire accepted-response flush/barrier to the coordinator. Describe failure events, response status alternatives, server-only warning semantics, and neutral schemas in OpenAPI/AsyncAPI. Reuse the existing journal transport rather than invent a bare-state SSE stream.
- [ ] Run `go test ./internal/adapters/httpapi ./internal/composition`; exercise the actual HTTP server with a throwaway upstream transport and observe SSE/state transitions. Commit S8.

## S9: Shared client and web integration

**Files, S:** modify `clients/shared/src/index.ts`, `clients/shared/src/routes.ts`, `web/shared-api.ts`, `web/src.tsx`, `web/settings.tsx`, and existing styles; create `web/portal.tsx` for shared account/promotion/project/update components.

**Consumes:** S8 routes and DTOs. **Produces:** web surface and reusable components for S10, without importing the web App into desktop.

- [ ] Add shared TypeScript DTOs matching the exact Go JSON tags. `FileListClient.call` already prefixes `/api/v1`; every new method passes a leading slash such as `this.call<PortalState>("/portal/state")`. Do not double-prefix the API path.
- [ ] Keep JWT state scoped to configured server origin. Clear it on expiry/sign-out/invalid `/session/me`, and never confuse it with household donor state. Registration goes to sign-in after 201. Account capability loss unmounts the dialog and complete Settings group without erasing the stored server key.
- [ ] Add shared components with explicit props: Snapshot, Status, existing API client, and an injected safe external-link opener. Compact promotion content anchors near the sidebar bottom without displacing navigation. Deliver only when visible, advance after `screenTime`, cancel timers on unmount/hidden document, and avoid prefetch impressions. Clicks use the local tracking route, not a direct upstream URL.
- [ ] Preserve ordered Other Projects links. Build account dialogs with password autocomplete, validation errors, Escape/focus return, request cancellation, and double-submit protection. Remote text is text, not HTML.
- [ ] Add update status/check/apply UI to Settings and an availability notice. Applying requires playback-warning confirmation. Handle accepted, no-op, busy/manual-only, failed operation, disconnect/reconnect, and refreshed current-version state. Every notice contains the server-only/manual-TV warning and releases link.
- [ ] Parse SSE envelope payload and refetch snapshots on connection recovery. Failed optional integrations leave no empty card/global alert. Use the existing typography, spacing, buttons, modal, and sidebar tokens; no new visual system.
- [ ] Run `npm run test:clients` and `npm run build:web`. Verify enabled/disabled/failure/recovery and account/update flows in a real browser tab; screenshots/focus interaction are the UI proof. Add permanent tests only for plausible behavioral regressions such as expiry or stale SSE overriding a refreshed state. Commit S9.

## S10: Separate desktop shell integration

**Files, S:** modify `desktop/src/App.tsx`, `desktop/src/pages/ServerPage.tsx`, `desktop/src/pages/SettingsPage.tsx`, `desktop/src/lib/state.ts`, and `internal/gui/bindings.go`.

**Consumes:** S9 shared components, S6 supervisor lifecycle, configured shared API origin.

- [ ] Mount shared account/promotion/project/update components in the actual desktop shell. Configure the shared client against the current embedded-server origin and rebuild subscriptions when it changes. Do not retain a settings pointer from before data-directory relocation.
- [ ] Gate account Settings fields identically to web. Clear client JWT identity when the server origin changes. Server update controls remain unavailable while the embedded server is stopped, without pretending a different server is running.
- [ ] Use the existing native external-link opening convention; if it lacks an exported binding, add `OpenURL(raw string) error` on the real bound service with HTTP(S)/host validation and error propagation. Keep neutral destinations and use the local promotion tracking URL when recording clicks.
- [ ] Connect update state to ServerPage and shell notices. Apply confirms playback interruption and whole-app restart. Reconnection/version confirmation is driven by the restarted server, not an optimistic success toast before exit.
- [ ] Run `npm run build:desktop` and the affected Go GUI tests. Launch the actual Wails application to prove configured auto-start, incomplete setup, dialogs, account gating, and full-process restart/lock release. Browser rendering alone is not desktop lifecycle proof. Commit S10.

## S11: Tizen display-only promotions and server update controls

**Files, S:** modify `clients/tizen/src/main.tsx`, `clients/tizen/src/navigation.ts`, `clients/tizen/src/tv.css`, and existing navigation tests.

**Consumes:** S8 routes and shared client DTOs. **Produces:** no account controls; ordered project URL dialog and server update UI.

- [ ] Reuse `eventPayload` for SSE rather than parsing an envelope as Status. Add Snapshot and Status state without undefined local variables or duplicate listeners.
- [ ] Render promotions as display-only and non-focusable. Do not record clicks from TV or introduce invisible focus targets. Respect visibility and screen-time delivery rules.
- [ ] Add Other Projects navigation only when links exist. The URL dialog traps remote focus, has a usable Close/Back action, displays the URL with readable wrapping, and restores previous focus on exit. It must not depend on mouse text selection.
- [ ] Add check/apply controls in TVSettings after the existing rows 1–15, using distinct region/row/column/key identities (rows 16/17). Use an update confirmation dialog warning about playback interruption. Keep the server-only/manual-TV warning and releases URL visible for both available and manual-only notices.
- [ ] Update navigation recovery when gated items disappear and while dialogs open/close. Handle accepted/current/conflict/failure/reconnect without leaving focus in an unmounted element. No login/API-key routes or fields on TV.
- [ ] Run `npm run build:tizen` and `npm run test:clients`; exercise arrow/Enter/Back behavior in the browser Tizen build. Physical-TV verification requires approval and remains separately reported. Commit S11.

## E1: GitHub-owned, atomic release notice synchronization

**Files, E:** modify `port/contract/updates.go`, `domain/updates/service.go`, `domain/updates/service_test.go`, `infrastructure/postgres/updates.go`, `port/dto/response/updates.go`, `container/updates.go`, and existing update editor/schema files; create `domain/updates/sync.go`, `sync_test.go`, and `infrastructure/github/releases.go`.

**Consumes:** fixed streaming repository and R1's artifact names. **Produces:** `Sync(ctx context.Context, tag string) (SyncResult, error)` where `SyncResult` is `{Version string; Changed bool}`; `Reconcile(ctx context.Context) error` selects the newest eligible complete stable release by semantic version, not arbitrary API ordering.

- [ ] Add behavioral tests for valid publish visible through public GET, duplicate delivery, out-of-order versions, draft/prerelease, incomplete assets becoming complete, wrong repository, GitHub outage, and two concurrent publishers. Preserve the last valid notice until an entire new notice is committed.
- [ ] Implement a bounded GitHub metadata/manifest adapter for the fixed repository. It accepts a validated tag only; JSON notification cannot supply repository, URLs, notes, timestamp, or platform mappings. Enumerate/paginate releases during reconciliation so a newer draft/incomplete release does not hide the newest eligible complete stable release.
- [ ] Validate exact expected assets and checksum entries. Resolve notes/time/URLs from GitHub. Normalize tags for precedence but preserve actual release tag for URL construction. Require published stable releases; reject moving an already-published tag to different executable assets rather than silently replacing equal-version code.
- [ ] Extend the existing platform set/DTO/editor for explicit headless and bundle flavor. Add `linux-arm64-headless`, `linux-amd64-headless`, `darwin-universal-bundle`, and `linux-armv7`; existing GUI/raw Darwin keys keep their meaning. Persist GitHub release ID and an asset-ID/digest fingerprint with the managed notice in the existing migration system so equal-version replacement is detected after process restarts. Update all store fakes and response/editor mappings without exposing credentials.
- [ ] Make compare-and-save atomic in PostgreSQL, not just a process mutex. Serialize on a database advisory transaction lock or singleton row lock, re-read current version under that lock, compare semantic versions in the domain, and save notice plus binaries in the same transaction. Route notifications/reconciliation through this path. Expose source ownership and lock GitHub-managed fields in the admin editor while automatic mode is enabled.
- [ ] Run `go test ./domain/updates ./infrastructure/postgres ./cmd/http/app/updates` in E with its existing isolated database test setup. Demonstrate duplicate/out-of-order requests do not change public GET. Commit E1 in E, not S.

## E2: Authenticated sync endpoint and reconciliation lifetime

**Files, E:** modify `cmd/http/app/updates/routes.go`, `container/updates.go`, and existing configuration/lifecycle ownership; create `cmd/http/app/updates/handler/sync.go` and `sync_test.go`; update existing service setup/update documentation without naming the external platform in S's docs.

**Produces:** `POST /api/v1/updates/sync`, body `{"tag":"v1.2.3"}`, 200 `{version, changed}` for publish/no-op. This is not an account-admin API.

- [ ] Add endpoint regressions for absent/wrong credential, malformed/oversized body, arbitrary repository/URL fields, duplicate notification, invalid tag, and upstream failure. Assert denied requests never call synchronization and no response contains submitted credentials.
- [ ] Configure a dedicated release-sync bearer credential through E's existing secret configuration mechanism. Missing configuration disables the endpoint. Compare fixed-length SHA-256 digests with `subtle.ConstantTimeCompare`, never ordinary secret string comparison. Cap requests at 4 KiB, reject unknown JSON fields, rate-limit using existing middleware if available, and never log headers/body/credential-bearing errors.
- [ ] Authenticate the sync credential directly, not via an admin session or supporter-key identity lookup. The GitHub secret and service-side sync configuration must match. Do not assume an account admin API key automatically grants access to this new endpoint.
- [ ] Run startup reconciliation plus a cancellable five-minute timer. Bound each GitHub operation, use conditional requests where appropriate, serialize local attempts, and rely on E1's database transaction for cross-process ordering. Failure preserves the published notice and retries on the next tick. Shutdown cancels and joins the loop.
- [ ] Run E's update HTTP/domain tests and launch the real external HTTP service against a throwaway GitHub transport and test database. Send a local notification, inspect public GET, then simulate missed push and observe reconciliation. No live credential is needed for this smoke.
- [ ] Document deployment prerequisites, secret rotation without value examples, current source ownership, and normal/recovery latency. Commit E2.

## R1: Flavor-complete release assets and checksum contract

**Files, S:** modify `.github/workflows/release.yml`, `Makefile`, build identity source, and release documentation.

**Consumes:** S4 identity and the shared asset table below. **Produces:** complete stable releases that both sync and updater can resolve.

| Install | Release asset |
|---|---|
| Linux AMD64 GUI | `filelist-streaming-${version}-linux-amd64.tar.gz` |
| Linux ARM64 GUI | `filelist-streaming-${version}-linux-arm64.tar.gz` |
| Linux ARM64 headless | `filelist-streaming-${version}-linux-arm64-headless.tar.gz` (new) |
| Linux AMD64 headless | `filelist-streaming-${version}-linux-amd64-headless.tar.gz` (new) |
| Linux ARMv7 headless | `filelist-streaming-${version}-linux-armv7.tar.gz` |
| Windows AMD64 / ARM64 | `filelist-streaming-${version}-windows-${arch}.zip` |
| Darwin raw AMD64 / ARM64 | `filelist-streaming-${version}-darwin-${arch}.tar.gz` |
| Darwin application bundle | `filelist-streaming-${version}-darwin-universal.zip` |

`${version}` is the trimmed repository VERSION value without a leading `v`, as used by the existing workflow. For tagged publication require `GITHUB_REF_NAME == "v" + VERSION`; asset resolution strips that one tag prefix and uses the exact version text. Do not generate alternative optional-v filenames.

- [ ] Extend the matrix with CGO-disabled `-tags headless` ARM64 and AMD64 and embed flavor/version identity consistently in release and local builds. Keep Linux GUI artifacts separate. Bundle identity comes from actual `.app` installation as well as embedded build identity.
- [ ] Preserve Wails universal bundle packaging and signature metadata (`ditto` in the existing release job). Include verified helper support without mutating the bundle after signing. Assert every required archive exists before checksumming.
- [ ] Generate `SHA256SUMS` for exactly release payload files, excluding the manifest itself. Do not accidentally include stale prior-run assets. Keep existing SBOM/provenance publication intact.
- [ ] Verify the complete matrix with local archive-inspection fixtures and CI build jobs. Do not create a public test release merely to verify planning. Commit R1.

## R2: Production release notification job

**Files, S:** modify `.github/workflows/release.yml`; create `scripts/notify-release.mjs`; update existing release setup documentation.

**Consumes:** successful `publish` job and E2 endpoint; production secret reference only. **Produces:** bounded immediate notification, independent of build job approval gates.

```yaml
  notify-update-feed:
    name: Synchronize update feed
    needs: publish
    if: startsWith(github.ref, 'refs/tags/v')
    runs-on: ubuntu-latest
    environment: prod
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262
        with:
          persist-credentials: false
      - uses: actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38
        with:
          node-version: 24
          package-manager-cache: false
      - name: Notify completed release
        env:
          RELEASE_SYNC_TOKEN: ${{ secrets.FL_ADS_APIKEY }}
          RELEASE_TAG: ${{ github.ref_name }}
        run: node scripts/notify-release.mjs
```

- [ ] Add the notification script with a fixed sync endpoint, `fetch`, AbortSignal timeout, JSON body `{tag: process.env.RELEASE_TAG}`, and bearer header read from `process.env.RELEASE_SYNC_TOKEN`. Validate nonempty token/tag before requesting. Never use shell interpolation of tag or token, `set -x`, curl command-line headers, or raw response/error logging.
- [ ] Retry transport/429/5xx at most three attempts with bounded backoff; fail immediately on authentication/validation errors. On success log only tag and synchronized/no-op status. On failure exit nonzero with a neutral message; never append the credential or response body.
- [ ] Add the job above after publish. A failed notify job does not delete or recreate an already-valid GitHub release. Do not rerun `gh release create` just to retry synchronization. The five-minute service reconciliation is automatic recovery.
- [ ] Exercise the real script with a throwaway intercepted fetch or local transport fixture and a dummy secret: successful call, missing token, authentication rejection, transient retry, and exhausted timeout. Verify captured stdout/stderr contains no dummy secret. Validate workflow structure with the project's available YAML/Actions tooling.
- [ ] Record that the user already configured `prod`/`FL_ADS_APIKEY`; do not query or overwrite it. Explain that matching E-side credential and endpoint deployment are still needed, and environment approvals can delay push. Commit R2.

## V1: End-to-end verification and release gate

**Files:** affected source/docs only; no synthetic passing checkmarks. This task does not authorize deployment.

- [ ] Run `go test ./...`, affected race suites, `npm run test:clients`, `npm run build:web`, `npm run build:tizen`, and `npm run build:desktop`. Record commands and actual results, including native dependency blockers.
- [ ] Launch the real headless server with a temporary data directory and exercise cached current, fresh check, current apply no-op, accepted apply, SSE, failure/recovery, and update-and-serve. Subprocess verification must prove old-process exit and new-version readiness.
- [ ] Launch macOS raw and full `.app` update flows. Exercise valid update, failed checksum, malformed bundle, old-process lock release, quarantine/signature validation, launch failure rollback, and health-confirmed backup cleanup. Verify actual app launch, not only `codesign` output.
- [ ] Run Windows native executable/helper lock/replacement/relaunch/recovery scenarios on Windows. Run systemd migration and clean-exit relaunch in an isolated Linux environment. Cross-compiles cannot mark these gates passed.
- [ ] Browser-verify web and Tizen states and keyboard/remote focus; native-verify desktop shell. Cover disabled public settings, account failure, donor expiry, promotion failure/recovery, empty/failed links, update conflict, failed background update, and reconnect. Every notice contains the same server-only/manual-TV explanation. No global outage warning or empty feature panels.
- [ ] With local external service + GitHub fixture, publish complete release A, retry A, deliver older release, announce incomplete B, complete B, and observe reconciliation publish B atomically. Assert public GET never exposes a partial binary set. Verify missing/wrong sync credential does not publish.
- [ ] Review spec coverage using the table below and review changed callers/docs. Only after these smoke gates pass, remove throwaway scripts/fixtures not earning permanent regression status and update existing release/deployment/changelog documentation. Keep `scripts/notify-release.mjs`, which is production workflow code.
- [ ] Report native/physical-device gates that could not run as unresolved evidence, not success. Ask before any Pi deployment or physical-TV operation. Commit verified scoped work; do not claim a release is operational until E-side deployment/configuration is complete.

## Coverage and review record

| Requirement | Owner |
|---|---|
| Server-owned secrets, whole account gate, JWT/key separation | S1–S3, S8–S10 |
| Promotion delivery impressions, recovery, donor expiry | S2–S3, S9–S11 |
| Ordered projects and TV focus-safe URL dialog | S2, S9–S11 |
| Separate desktop shell and configured auto-start | S6, S10 |
| All update surfaces, server-only/manual-TV copy | S8–S11 |
| Startup auto-apply, hourly notify, update-and-serve | S6 |
| Mandatory repository checksum and correct flavor | S4–S5, R1 |
| macOS whole bundle, Windows locks, health rollback | S5–S6, V1 |
| Background cancellation before engine/database close | S3, S6 |
| Unprivileged systemd migration including saved defaults | S7 |
| Atomic stable-release publication and outage recovery | E1–E2 |
| Production environment secret and post-upload timing | R2 |
| HTTP/AsyncAPI contracts and honest runtime evidence | S8, V1 |

Inline review corrections incorporated: no application-to-composition cycle; no array comparison; no prerelease stripping; no optional/wrong checksum filename; no hash-then-read exhausted stream; no archive-as-executable install; no rename-away gap for Unix files; no immutable GUI assumption; no server-side logout cache; no registration JWT; one agreed Snapshot and Status shape; actual string-payload SSE envelope; leading-slash client paths; no unconditional supporter-key field; no fabricated GUI constructors or TV variables; no claim that a writable container is supported; no secret value in plan/workflow.

Application verification has not run during planning. This document defines execution and acceptance work; it is not evidence that the described features already work.
