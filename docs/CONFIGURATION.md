# Configuration

Configuration is managed in the browser and persisted to `data/settings.json`. A terminal first run asks for the required settings; headless starts continue unconfigured so the first-run settings page is always available. The page is tabbed — Tracker, Storage, Playback, Server, Maintenance, and Test — with the active tab encoded in the URL hash (for example `/settings#playback`; an unknown hash reopens Tracker). One **Save changes** button on the sticky bar at the bottom of the page saves the whole settings object.

Every JSON setting can also be supplied as an uppercase `TORRENT_TV_...` environment variable; camel-case boundaries become underscores (for example, `instanceName` becomes `TORRENT_TV_INSTANCE_NAME`). `TORRENT_TV_SETTINGS_PATH` selects the settings file itself. Environment values are authoritative, are marked read-only in browser Settings, and are never copied back into that file.

## Data directory

The data directory holds `settings.json`, the SQLite database, `logs/`, the artwork cache, and the engine session. It is resolved in this order:

1. The `--data-dir` flag (absolute or relative to the working directory).
2. A `data.location` pointer file next to the executable (written only after a GUI relocation).
   - **Desktop app:** Linux uses `/var/lib/torrent-tv` when it exists and is writable, otherwise `~/.local/share/torrent-tv`; Windows uses `%APPDATA%\Torrent TV`; macOS uses `~/Library/Application Support/Torrent TV`.
   - **`serve`:** `data/` next to the executable (not the working directory).

The `serve` fallback moved in this release: earlier builds resolved the default `data/` relative to the working directory, so launching from a different directory silently created a second, empty data directory. Data from an older deployment is not lost — keep using it by launching with `--data-dir /old/path/data`, or write that path into the `data.location` pointer file next to the executable (the GUI's relocation writes the same file).

Change the location from the desktop app's Server page (data folder → **Change…**). The change requires the server stopped — a running server is stopped first and restarted afterwards — and the target directory must not exist or must be empty; a non-empty target is refused and directories are never merged. All contents move (same volume: atomic rename; cross volume: copy, verify each file's size and SHA-256, then delete the source), the pointer file is written atomically, and any failure rolls back leaving the original data untouched.

`TORRENT_TV_SETTINGS_PATH` keeps its existing precedence: when set it selects the settings file itself regardless of the data directory above. Systemd deployments pin `--data-dir /var/lib/torrent-tv/data` in the unit, so the platform defaults never apply to them.

## Required dependencies

- FileList URL, username, and passkey. Never enter the account password.
- Download root for the built-in engine (default). Selecting the optional qBittorrent engine instead takes the qBittorrent Web UI URL, username, and password in the same Settings fields; keep qBittorrent's authentication enabled.

Each dependency has a separate diagnostic API route. Browser Settings gathers the FileList, qBittorrent, storage, TMDB, and SubDL tests on its Test tab. SubDL uses `https://api.subdl.com`; the public website address is rejected with a corrective error. Every provider field has a hover help icon; selecting it opens copyable credential guidance. Save credentials before testing them. The TV exposes safe playback/subtitle and background-worker settings plus server connection management, while secrets and storage configuration remain browser-only.

SubDL needs an API key generated from the API section of `https://subdl.com/panel`. It provides individual subtitle files, avoiding the RAR response produced by Subs.ro and the paid Consumer API key required by OpenSubtitles. Preferred and fallback languages are combined into one provider search, and successful results are reused for one hour for the same media query to conserve the daily allowance. Signed query parameters returned in unpack-file URLs are stripped from client-visible candidate IDs; authentication is applied only by the server adapter. The repository `.env` remains a developer test aid and is intentionally not imported into runtime settings.

The Maintenance tab shows observed catalog coverage above the **Fetch latest** and **Rebuild catalog** cards. Both actions are append-only and never remove rows. Fetch latest appends the newest tracker window; Rebuild catalog refreshes every enabled category's latest window and rebuilds local projections over everything ever observed, and asks for confirmation first on Settings because it sweeps every category. The FileList API cannot page through all historical releases, so live searches are also an intentional cache-growth path. The same actions are available on Events, where the cards remain one-click; latest runs hourly and rebuild weekly.

## Streaming defaults

Routine Pi deployment prompts for the SSH target, qBittorrent service/config path, application download root, incomplete-download path, protected backup directory, and application binary path. The last non-secret answers are stored in ignored `deploy/.deploy.local.conf` and offered as defaults next time. Credentials and tokens are never prompted or stored there.

The sanitized qBittorrent template enables its incomplete directory, disables preallocation and the `.!qB` suffix, and contains no WebUI or tracker credentials. It does not set global, alternative, or per-torrent speed limits. The production default is `/mnt/sda1/torrent/.incomplete/`, inside the download root on the large disk. Every deployment stops qBittorrent, creates a new mode-`0600` timestamped config backup, merges only those four download keys, and rolls back on failure. Existing credentials, tokens, ports, bindings, save paths, and unknown settings are preserved.

| Setting | Default |
| --- | ---: |
| Initial buffer | 128 MiB |
| Read-ahead | 256 MiB |
| Piece wait timeout | 600 seconds |
| Managed download allocation (GB) | 15 |
| Free-space reserve (GB) | 8 |
| Catalog maximum age | 24 hours |
| Watched threshold | 90% |
| Preferred audio language | `en` |
| Preferred subtitle language | `ro` |
| Fallback subtitle language | `en` |
| Metadata language | `ro-RO` |
| Metadata fallback language | `en-US` |
| Artwork cache | `data/artwork` |
| Artwork cache ceiling | 512 MiB |
| Concurrent background jobs | 10 |
| FileList concurrent requests | 1 |
| Title refresh active timeout | 30 minutes |

Buffer values are limited to 2 GiB. Allocation and free-space reserve are configured in binary gigabytes (GiB) and accept fractional values; 0 disables each check. An hourly retention job enforces them: it evicts one torrent at a time through the manual-delete path (season-pack siblings included) until the allocation holds and the reserve is met, then publishes a `downloads.evicted` event (reason `cap` or `reserve`) for each eviction. Eviction order follows the configured rule list (`evictionRules`; default `oldest-completed`; atoms: `oldest-completed`, `newest-completed`, `least-recently-played`, `most-recently-played`, `watched-first`, `never-watched-first`, `largest`, `smallest`). Protection toggles (`protectIncomplete`, `protectLeased`, `protectFavorites`, `protectNeverWatched`) default to on/on/off/off. Downloads that cannot fit after evicting everything unprotected are refused. Browser settings expose the rules and toggles; every key also works as an environment variable (`TORRENT_TV_EVICTION_RULES`, `TORRENT_TV_PROTECT_INCOMPLETE`, `TORRENT_TV_PROTECT_LEASED`, `TORRENT_TV_PROTECT_FAVORITES`, `TORRENT_TV_PROTECT_NEVER_WATCHED`).

The global job limit and title-refresh timeout are browser-configurable and require a service restart. Queue and rate-limit waiting do not consume the title-refresh execution timeout. FileList stays serialized even when metadata jobs use the other worker slots.

## Metadata

The optional TMDB API key is entered in browser settings and stored only in `data/settings.json`. Blank secret submissions preserve the existing key. Without a key, parsed titles, hierarchy, filters, and source selection remain functional; posters, backdrops, localized titles, and synopses use their fallback states. Clients never call TMDB directly.

## Network boundary

The initial listener is `:8097`; requests are accepted only from loopback and RFC1918 private network ranges. Narrow the trusted CIDRs to the actual LAN when practical. Keep the service behind the home firewall and never port-forward it. Changing the listener requires restart.

`instanceName` identifies the server in Tizen discovery results. Choose a short household-friendly name when more than one server exists on the LAN. Discovery validates `/api/v1/system/info`; it does not broadcast credentials or settings.

## Logs

Structured logs are written to stdout/journald and `data/logs/server.log`. On an interactive terminal the console renders human-readable colored lines; piped output and the log file stay plain JSON, so machine readers are unaffected. Raspberry Pi deployment installs `/etc/logrotate.d/torrent-tv`: rotate daily or at 10 MiB, retain 14 archives, compress, and use `copytruncate` so the daemon does not need to reopen the file.
