# Installation

## Requirements

| Requirement | Where to get it | Used for |
| --- | --- | --- |
| FileList account | [filelist.io](https://filelist.io) — username and passkey from your profile page | Browsing and downloading. The passkey authorizes the tracker; treat it like a password. |
| FFmpeg and ffprobe (optional) | macOS: `brew install ffmpeg` · Debian/Ubuntu/Raspberry Pi OS: `sudo apt install ffmpeg` · Fedora: `sudo dnf install ffmpeg` · Windows: `winget install Gyan.FFmpeg` or [gyan.dev](https://www.gyan.dev/ffmpeg/builds/) | Embedded subtitle probing/extraction and browser audio fallback. Detected automatically on PATH at first start. |
| TMDB API key (optional) | [themoviedb.org → Settings → API](https://www.themoviedb.org/settings/api) (free account) | Artwork and metadata. |
| SubDL API key (optional) | [subdl.com API panel](https://subdl.com/panel/api) (free account) | Extra subtitles. |
| qBittorrent (optional) | [qbittorrent.org](https://www.qbittorrent.org) | Only if you switch the download engine from the built-in one to qBittorrent in Settings. |

The server listens on `8097` (web) and `42069` (torrent peers). Both are configurable in Settings.

## Download

Prebuilt archives for every supported platform are on the [releases page](https://github.com/mihaiflorentin88/torrent-tv/releases). Names follow one standard — `torrent-tv-<version>-<platform>[-<flavor>].<ext>` — so the filename tells you what a build is and where it runs:

- **`app`** — the packaged macOS application (a `.app` bundle inside the zip).
- **`desktop`** — desktop app + server in one archive (GUI included).
- **`cli`** — a raw binary meant to run from a terminal; it can open the desktop UI or run the headless `serve` server.
- **`headless`** — pure server build, GUI excluded at compile time.
- TV clients carry their platform instead of a flavor: `samsung-tizen` (WGT) and `android-tv` (APK).

| Release asset | What it is | Pick it when |
| --- | --- | --- |
| `torrent-tv-<version>-macos-universal-app.zip` | The universal **Torrent TV.app** (Apple Silicon + Intel), zipped with `ditto` so the ad-hoc signature survives | **You want the normal macOS app.** Unzip and drag it to Applications — desktop app and `serve` server in one bundle. |
| `torrent-tv-<version>-macos-arm64-cli.tar.gz` | `torrent-tv` binary for the terminal: no arguments opens the desktop window, `serve` runs the headless server | You drive an Apple Silicon Mac from the terminal instead of using the .app. |
| `torrent-tv-<version>-macos-amd64-cli.tar.gz` | Same terminal binary for Intel Macs | Terminal use on Intel Macs. |
| `torrent-tv-<version>-linux-amd64-desktop.tar.gz` | Desktop app + server in one binary | 64-bit x86 Linux with WebKitGTK 4.1 installed (see [Linux runtime packages](#linux-runtime-packages)). |
| `torrent-tv-<version>-linux-arm64-desktop.tar.gz` | Desktop app + server in one binary | 64-bit ARM desktop distros (Raspberry Pi OS Bookworm, Ubuntu 24.04) with WebKitGTK 4.1. |
| `torrent-tv-<version>-linux-amd64-headless.tar.gz` | Pure static server binary (GUI excluded via the `headless` build tag) | 64-bit x86 servers without WebKitGTK. |
| `torrent-tv-<version>-linux-arm64-headless.tar.gz` | Pure static server binary (GUI excluded via the `headless` build tag) | 64-bit ARM servers without WebKitGTK (e.g. Raspberry Pi 4/5 on a minimal distro). |
| `torrent-tv-<version>-linux-armv7-headless.tar.gz` | Pure headless 32-bit ARM server binary (GUI unreachable on `linux/arm`) | Older 32-bit ARM boards (e.g. Raspberry Pi 2/3 on a 32-bit OS). |
| `torrent-tv-<version>-windows-amd64-desktop.zip` | `torrent-tv.exe` with desktop app + server | 64-bit x86 Windows. SmartScreen warns on first run — choose **More info → Run anyway**. |
| `torrent-tv-<version>-windows-arm64-desktop.zip` | `torrent-tv.exe` with desktop app + server | Windows on ARM (e.g. Snapdragon laptops). |

**macOS: .app or .tar.gz?** The `app` zip is the click-and-go desktop application. The `cli` archives hold the same GUI-capable binary for terminal workflows — remote SSH boxes, launchd scripts, or `serve`-only setups — and can always open the desktop window when a display is present.

Every release also ships:

- `SHA256SUMS` — verify with `sha256sum -c SHA256SUMS --ignore-missing`.
- CycloneDX/SPDX SBOMs for the Go and npm dependencies.
- The unsigned Tizen `torrent-tv-<version>-samsung-tizen.wgt` (see [Samsung Tizen application](#samsung-tizen-application)).
- The sideload `torrent-tv-<version>-android-tv.apk` with its `.apk.sha256` checksum (see [Android TV application](#android-tv-application-torrenttv)).

Every archive contains the binary — named `torrent-tv` (`torrent-tv.exe` on Windows), so the installed path stays stable across versions — plus a `README.md`. `desktop` and `cli` builds contain both the desktop app and the headless server; `headless` builds exclude the GUI at build time (armv7 via its architecture constraints, the others via the `headless` build tag). If you build from source instead, `make help` lists every target with the exact artifact it produces.

Headless arm64/amd64 servers whose distro lacks `libwebkit2gtk-4.1` should use the prebuilt `linux-arm64-headless` / `linux-amd64-headless` archives above. To build them from source instead, `make build-arm64-headless` produces `bin/torrent-tv-linux-arm64-headless` and `make build-amd64-headless` produces `bin/torrent-tv-linux-amd64-headless`; `make deploy-pi PI_HOST=user@host` builds and stages the arm64 one onto a Raspberry Pi in one step.

## First run

One binary runs two modes:

- Launch it without arguments (double-click, or run it from a terminal) to open the desktop app: a window plus a system-tray icon. See [Desktop app](#desktop-app) below.
- Run `torrent-tv serve` for the headless server. It creates its `data/` folder next to the executable — or wherever `--data-dir` points:

```sh
./torrent-tv serve           # Linux / macOS
.\torrent-tv.exe serve       # Windows PowerShell
```

The first headless start asks three questions:

```text
Download root [data/downloads]: /home/you/media
FileList username: you
FileList passkey:
```

- **Download root** — where downloads are stored; must be writable. Enter accepts the default. A relative root (like the default) resolves against the resolved data directory, not the working directory, and the same anchoring keeps the other default paths (`data/filelist.db`, `data/torrent-session`, `data/artwork`, `data/subtitles`) inside the data dir on every launch style.
- **FileList username / passkey** — from your filelist.io profile; the passkey is typed hidden.
- Answers are saved to `data/settings.json` (mode `0600`), and ffmpeg/ffprobe are auto-detected from PATH.

Then open `http://localhost:8097`. Press `Ctrl+C` to stop the server.

### Platform notes

**Linux (amd64, arm64, armv7).** Headless services without a terminal skip the prompts: set `TORRENT_TV_DOWNLOAD_ROOT`, `TORRENT_TV_FILE_LIST_USERNAME`, and `TORRENT_TV_FILE_LIST_PASSKEY` instead.

**macOS (Apple Silicon, Intel).** If a browser downloaded the binary, macOS may block it once. Remove the quarantine with `xattr -d com.apple.quarantine torrent-tv-darwin-*` or allow it under System Settings → Privacy & Security.

**Windows (amd64).** SmartScreen warns about the unsigned binary on first run: choose **More info → Run anyway**.

## Desktop app

Every binary except `torrent-tv-linux-armv7` includes the desktop app: a native window plus a system-tray icon, with the HTTP server running inside the same process. Launch the binary without arguments to open it.

### First launch

On the first launch the window opens on setup: the Settings page shows a banner with the required settings still missing (download root, FileList username, passkey). Saving a complete configuration auto-starts the server — no extra step. Afterwards the Server page shows the status with Start/Stop controls, and closing the window hides it to the tray; quit from the tray menu (*Quit*, or Cmd+Q on macOS).

### Autostart

Toggle **Start at login** on the GUI's Server page. It starts minimized to the tray. The entry lives at:

| OS | Entry |
| --- | --- |
| Windows | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, value `Torrent TV` |
| macOS | `~/Library/LaunchAgents/com.torrent-tv.plist` |
| Linux | `~/.config/autostart/torrent-tv.desktop` (XDG autostart) |

The OS artifact is the source of truth: removing it disables autostart, and the toggle always reflects the actual OS state.

### Data directory

The desktop app does not use `data/` next to the binary. Defaults:

| OS | Default data directory |
| --- | --- |
| Linux | `/var/lib/torrent-tv` when it exists and is writable, otherwise `~/.local/share/torrent-tv` |
| Windows | `%APPDATA%\Torrent TV` |
| macOS | `~/Library/Application Support/Torrent TV` |

Change the location from the GUI (Server page → data folder → *Change…*); the contents move and the new path is recorded in a `data.location` pointer file next to the executable. See [CONFIGURATION.md](CONFIGURATION.md) for the full resolution order and relocation rules.

### Linux runtime packages

The Linux binaries are dynamically linked against GTK 3 and WebKitGTK 4.1 and need those runtime packages installed — including for `serve`, because the dynamic linker loads them even when no window opens. Install the same packages the server bootstrap uses:

| Distro | Packages |
| --- | --- |
| Debian/Ubuntu/Raspberry Pi OS (`apt`) | `libgtk-3-0 libwebkit2gtk-4.1-0 libayatana-appindicator3-1` |
| Fedora/RHEL (`dnf`) | `gtk3 webkit2gtk4.1 libayatana-appindicator-gtk3` |
| Arch (`pacman`) | `gtk3 webkit2gtk-4.1 libayatana-appindicator` |
| openSUSE (`zypper`) | `gtk3 webkit2gtk-4_1 libayatana-appindicator3-1` |

`deploy/bootstrap-server.sh` installs these automatically on a fresh server.

### macOS Gatekeeper and Windows SmartScreen

The app bundles are ad-hoc signed but not notarized, so both operating systems warn once:

- **macOS:** if opening the app is blocked, either clear the quarantine flag once (`xattr -cr "/Applications/Torrent TV.app"`) or right-click the app and choose **Open**, then confirm in the dialog.
- **Windows:** SmartScreen shows **Windows protected your PC** on first run — choose **More info → Run anyway**.

The desktop app renders through the platform webview: WKWebView on macOS, WebView2 on Windows (preinstalled on Windows 11 and current Windows 10; on older systems install the [Evergreen WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)), WebKitGTK on Linux.

## Run as a service (Linux, optional)

For an always-on server, install the reviewed systemd and logrotate files from `deploy/systemd/`:

```bash
sudo useradd --system --home-dir /var/lib/torrent-tv --no-create-home --shell /usr/sbin/nologin torrent-tv
sudo install -d -m 0755 -o torrent-tv -g torrent-tv /var/lib/torrent-tv/bin
sudo install -m 0755 -o torrent-tv -g torrent-tv torrent-tv /var/lib/torrent-tv/bin/torrent-tv
sudo install -m 0644 deploy/systemd/torrent-tv.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/torrent-tv.logrotate /etc/logrotate.d/torrent-tv
sudo systemctl daemon-reload
sudo systemctl enable --now torrent-tv.service
```

Adjust the download root in the unit file first if it is not `/srv/filelist-downloads`. The unit runs the binary in headless mode — `torrent-tv serve --data-dir /var/lib/torrent-tv/data` — from the service-owned `/var/lib/torrent-tv/bin/torrent-tv` path, so the service can update its own binary and a bare launch on the server never opens a GUI. Because services run without a terminal, provide the required settings through environment variables (see the headless note above) or a prepared settings file.

### Upgrading

Older service files ran a bare `ExecStart=/usr/local/bin/torrent-tv` (with or without the `serve` argument). Re-run `make deploy-pi` once: it stages the corrected unit and migrates the binary from `/usr/local/bin` to the service-owned `/var/lib/torrent-tv/bin/torrent-tv`, which `Restart=always` keeps alive and the in-application updater can replace without root. To fix the unit by hand instead:

```bash
sudo systemctl edit --full torrent-tv.service
# ExecStart=/var/lib/torrent-tv/bin/torrent-tv serve --data-dir /var/lib/torrent-tv/data
sudo systemctl daemon-reload
sudo systemctl restart torrent-tv
```

### Fresh-server bootstrap

On a new dedicated Linux server, `deploy/bootstrap-server.sh` installs packages, creates service users, verifies and installs the exact Go toolchain, builds the versioned headless server (`-tags headless`, `composition.Version` from `VERSION`), installs it under the service-owned `/var/lib/torrent-tv/bin` directory, and enables the services:

```bash
git clone https://github.com/mihaiflorentin88/torrent-tv.git
cd torrent-tv
sudo sh deploy/bootstrap-server.sh --confirm-server-install --download-root=/mnt/sda1/torrent
```

Preview first with `--dry-run`. Supports `apt`, `dnf`, `pacman`, and `zypper`. Never run it on a workstation.

## Raspberry Pi deployment

To update an existing ARM64 Raspberry Pi from a development machine:

```bash
make deploy-pi PI_HOST=user@server.lan
```

The command cross-compiles the headless server, stages binary and service files, creates protected configuration backups, and rolls back automatically if startup fails. Installs that still live in `/usr/local/bin` migrate to the service-owned `/var/lib/torrent-tv/bin/torrent-tv` so the in-application updater works without root; an explicitly configured custom path is kept, and reported as manual-update-only when its directory cannot be owned by the service user. Answers are remembered in ignored `deploy/.deploy.local.conf`.

## qBittorrent engine (optional)

The built-in engine is the default and needs nothing external. To use qBittorrent instead: install it from [qbittorrent.org](https://www.qbittorrent.org), enable its Web UI with authentication (Tools → Options → Web UI), then switch **Download engine** in Settings → Storage and fill in the URL and credentials.

## Configuration and troubleshooting

- Settings reference: [CONFIGURATION.md](CONFIGURATION.md). Usage and playback: [USER_GUIDE.md](USER_GUIDE.md).
- Every provider field in browser Settings has a **?** help button with links to the official source of each credential and dependency.

## Upgrade and rollback

Back up before upgrading:

- `data/settings.json`
- `data/filelist.db` (and its WAL/SHM files, or use a SQLite-safe backup while the server is stopped)
- any custom systemd overrides

Replace the binary and restart; settings and catalog survive. On a Raspberry Pi, `make deploy-pi` rolls back automatically when startup fails.

## Migrating from filelist-streaming-service (pre-rename installs)

The project was renamed from `filelist-streaming-service` to **Torrent TV** (`torrent-tv`). Installations of version 0.3.0 or older keep working, but they carry the old names. A one-time move brings them onto the new identity:

1. **Binary and release assets.** New releases ship `torrent-tv-<version>-<platform>[-<flavor>]` archives (a `torrent-tv-<version>-samsung-tizen.wgt` and a `torrent-tv-<version>-android-tv.apk`), so the old self-updater stops finding assets — download the new archive from the [releases page](https://github.com/mihaiflorentin88/torrent-tv/releases) or redeploy with `make deploy-pi` once and upgrade in place afterwards.
2. **Linux data directory.** The default data location is now `/var/lib/torrent-tv` (was `/var/lib/filelist-streaming-service` or `/var/lib/filelist-streaming`) and XDG installs move from `~/.local/share/filelist-streaming` to `~/.local/share/torrent-tv`. Stop the server, move the directory, and update any `--data-dir` pointer or systemd override.
3. **systemd service.** The unit is now `torrent-tv.service` running as the `torrent-tv` user (`deploy/systemd/torrent-tv.service`; `make deploy-pi` installs it). Disable and remove the old `filelist-streaming.service` after the new one is up.
4. **macOS/Windows app data.** The desktop GUI now stores its data under `Application Support/Torrent TV` (macOS) or `%APPDATA%\Torrent TV` (Windows); move the old `FileList Streaming` folder if you want to keep settings and catalog.
5. **TV clients.** The server's discovery identity is now `Torrent TV`. Tizen TV clients updated together with the server pair up as before; a WGT installed before the rename must be reinstalled to rediscover the renamed server. The Android TV client is unaffected (`com.torrenttv.app`).

The Filelist tracker settings (username, passkey) are untouched by the rename.

## Samsung Tizen application

Use the unsigned `torrent-tv-<version>-samsung-tizen.wgt` from a tagged release, or build it with `make frontend` and `make validate-tizen-wgt`. The TV client runs on Samsung Tizen 5.0 and newer. See [TIZEN.md](TIZEN.md) for Developer Mode, TV pairing, and Apps2Samsung installation. The TV and server must share the same private LAN.

## Android TV application (TorrentTV)

Use the `torrent-tv-<version>-android-tv.apk` from a tagged release, or build it with `make torrenttv-apk`. The app runs on Android TV 8.0 (API 26, the 2018 Android TV baseline) and newer, installs by sideload (`adb install`, or a file manager after allowing unknown sources per device), and updates by installing the newer APK over the old one. The app displays as TorrentTV and runs the same screens and design as the Tizen client (see [ANDROIDTV.md](ANDROIDTV.md) and [ADR-0009](adr/0009-android-tv-client-torrenttv.md)). The TV and server must share the same private LAN.
