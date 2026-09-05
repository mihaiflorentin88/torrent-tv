# Raspberry Pi service deployment

Nothing in this directory is applied automatically. Production installation requires explicit approval because it creates a system user, changes group membership/access, copies a binary and unit, and enables a service.

After reviewing the unit, `make deploy-pi` performs the complete workflow. Override the destination with `make deploy-pi PI_HOST=user@host`. The command cross-builds the headless ARM64 binary, stages files under a narrow `/tmp` directory, creates the dedicated account only when missing, preserves settings/database state, installs the binary under the service-owned `/var/lib/torrent-tv/bin` directory (migrating older `/usr/local/bin` installs; explicit custom paths are kept), atomically replaces the binary, restarts the daemon, and restores the previous unit and binary if startup fails.

Before installation:

1. Build `bin/torrent-tv-linux-arm64-headless` with `make build-arm64-headless`, or let `make deploy-pi` build it.
2. Confirm qB runs as `qbittorrent`, its actual download root, and that group read/traverse permissions apply to newly created files.
3. Review the unit paths and Raspberry Pi memory/disk headroom.
4. Back up `data/settings.json` and the SQLite database if upgrading.

The service is intentionally a member of the `qbittorrent` supplementary group and receives read-only systemd access to qB's download tree. It writes only its own `/var/lib/torrent-tv` state. qBittorrent itself writes media files.

The initial settings file can be created through `http://server.lan:8097` after startup. Keep port 8097 restricted to the private LAN.
