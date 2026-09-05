#!/bin/sh
# Fresh-server bootstrap. Do not use this for routine upgrades; make deploy-pi
# deliberately remains package-install-free.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

confirm=false
dry_run=false
download_root=${DOWNLOAD_ROOT:-/srv/filelist-downloads}
qb_temp_path=${QB_TEMP_PATH:-}
for argument in "$@"; do
	case "$argument" in
		--confirm-server-install) confirm=true ;;
		--dry-run) dry_run=true ;;
		--download-root=*) download_root=${argument#*=} ;;
		--qb-temp-path=*) qb_temp_path=${argument#*=} ;;
		*) echo "unknown argument: $argument" >&2; exit 2 ;;
	esac
done
[ -n "$qb_temp_path" ] || qb_temp_path=${download_root%/}/.incomplete

if [ "$confirm" != true ]; then
	echo "Refusing to modify this host without --confirm-server-install." >&2
	echo "Use --dry-run --confirm-server-install to review commands." >&2
	exit 2
fi
if [ "$(id -u)" -ne 0 ]; then echo "Run as root (for example: sudo $0 --confirm-server-install)." >&2; exit 2; fi

run() { if [ "$dry_run" = true ]; then printf '+ '; printf '%s ' "$@"; printf '\n'; else "$@"; fi; }
need() { command -v "$1" >/dev/null 2>&1; }

case "$download_root" in /*) ;; *) echo "download root must be absolute" >&2; exit 2;; esac
case "$qb_temp_path" in /*) ;; *) echo "qBittorrent temp path must be absolute" >&2; exit 2;; esac
case "$download_root$qb_temp_path" in *[!A-Za-z0-9_./:@-]*) echo "download paths cannot contain spaces or shell metacharacters" >&2; exit 2;; esac
case "$qb_temp_path/" in "${download_root%/}/"*) ;; *) echo "qBittorrent temp path must be inside the download root" >&2; exit 2;; esac

if need apt-get; then package_manager=apt; packages="ca-certificates curl ffmpeg logrotate python3 qbittorrent-nox tar libgtk-3-0 libwebkit2gtk-4.1-0 libayatana-appindicator3-1";
elif need dnf; then package_manager=dnf; packages="ca-certificates curl ffmpeg logrotate python3 qbittorrent-nox tar gtk3 webkit2gtk4.1 libayatana-appindicator-gtk3";
elif need pacman; then package_manager=pacman; packages="ca-certificates curl ffmpeg logrotate python qbittorrent-nox tar gtk3 webkit2gtk-4.1 libayatana-appindicator";
elif need zypper; then package_manager=zypper; packages="ca-certificates curl ffmpeg logrotate python3 qbittorrent-nox tar gtk3 webkit2gtk-4_1 libayatana-appindicator3-1";
else echo "Supported package managers: apt, dnf, pacman, zypper." >&2; exit 1; fi

echo "Torrent TV fresh-server setup"
echo "  package manager: $package_manager"
echo "  packages: $packages"
echo "  qB Web UI: http://127.0.0.1:8080"
echo "  downloads: $download_root"
echo "  incomplete downloads: $qb_temp_path"
echo "  application state: /var/lib/torrent-tv"
echo "  application binary: /var/lib/torrent-tv/bin/torrent-tv (headless build)"
echo "No firewall rules or application secrets will be changed."

case "$package_manager" in
	apt) run apt-get update; run apt-get install -y $packages ;;
	dnf) run dnf install -y $packages ;;
	pacman) run pacman -Sy --needed --noconfirm $packages ;;
	zypper) run zypper --non-interactive install $packages ;;
esac

architecture=$(uname -m)
case "$architecture" in x86_64) go_arch=amd64;; aarch64|arm64) go_arch=arm64;; *) echo "Unsupported Go architecture: $architecture" >&2; exit 1;; esac
version=$(tr -d '[:space:]' < "$repo_root/VERSION")
go_version=$(awk '$1=="go"{print $2;exit}' "$repo_root/go.mod")
go_archive="go${go_version}.linux-${go_arch}.tar.gz"
go_url="https://go.dev/dl/${go_archive}"
go_checksum_url="${go_url}.sha256"
go_root="/var/lib/torrent-tv/toolchains/go${go_version}"
build_root="/var/lib/torrent-tv/build"

run install -d -m 0750 /var/lib/torrent-tv /var/lib/torrent-tv/data /var/lib/torrent-tv/data/logs /var/lib/torrent-tv/bin "$build_root" "$download_root"
if [ "$dry_run" = true ]; then
	echo "+ download $go_url and $go_checksum_url; verify SHA-256; extract privately to $go_root"
else
	tmp_dir=$(mktemp -d /tmp/filelist-bootstrap.XXXXXX)
	trap 'rm -rf -- "$tmp_dir"' EXIT INT TERM
	curl --fail --location --proto '=https' --tlsv1.2 "$go_url" -o "$tmp_dir/$go_archive"
	curl --fail --location --proto '=https' --tlsv1.2 "$go_checksum_url" -o "$tmp_dir/$go_archive.sha256"
	expected=$(tr -d '[:space:]' < "$tmp_dir/$go_archive.sha256")
	actual=$(sha256sum "$tmp_dir/$go_archive" | awk '{print $1}')
	[ -n "$expected" ] && [ "$expected" = "$actual" ] || { echo "Go archive checksum mismatch" >&2; exit 1; }
	rm -rf -- "$go_root.new"
	install -d -m 0755 "$go_root.new"
	tar -C "$go_root.new" --strip-components=1 -xzf "$tmp_dir/$go_archive"
	rm -rf -- "$go_root"
	mv "$go_root.new" "$go_root"
	GOCACHE="$build_root/go-cache" CGO_ENABLED=0 "$go_root/bin/go" build -trimpath -tags headless -ldflags="-s -w -X github.com/mihaiflorentin88/torrent-tv/internal/composition.Version=${version}" -o "$build_root/torrent-tv" ./cmd/server
fi

if ! getent group qbittorrent >/dev/null 2>&1; then run groupadd --system qbittorrent; fi
if ! id qbittorrent >/dev/null 2>&1; then run useradd --system --gid qbittorrent --home-dir /var/lib/qbittorrent --create-home --shell /usr/sbin/nologin qbittorrent; fi
if ! id torrent-tv >/dev/null 2>&1; then run useradd --system --home-dir /var/lib/torrent-tv --no-create-home --shell /usr/sbin/nologin torrent-tv; fi
run usermod -a -G qbittorrent torrent-tv
run chown -R torrent-tv:torrent-tv /var/lib/torrent-tv
run chown -R qbittorrent:qbittorrent "$download_root"
run install -d -m 0770 -o qbittorrent -g qbittorrent "$qb_temp_path"
qb_config=/var/lib/qbittorrent/.config/qBittorrent/qBittorrent.conf
if [ "$dry_run" = true ]; then
	echo "+ create $qb_config when absent; merge deploy/qbittorrent/qBittorrent.streaming.conf with temp path $qb_temp_path"
elif [ ! -f "$qb_config" ]; then
	install -d -m 0750 -o qbittorrent -g qbittorrent "$(dirname "$qb_config")"
	printf '%s\n' '[LegalNotice]' 'Accepted=true' '[Preferences]' 'WebUI\Address=127.0.0.1' 'WebUI\Port=8080' "Downloads\\SavePath=${download_root%/}/" > "$qb_config"
	merged=$(mktemp "$(dirname "$qb_config")/.qBittorrent.conf.XXXXXX")
	python3 "$repo_root/tools/qbittorrent_config.py" --input "$qb_config" --output "$merged" --template "$repo_root/deploy/qbittorrent/qBittorrent.streaming.conf" --temp-path "$qb_temp_path"
	mv "$merged" "$qb_config"
	chown qbittorrent:qbittorrent "$qb_config"
	chmod 0640 "$qb_config"
fi
run install -m 0755 -o torrent-tv -g torrent-tv "$build_root/torrent-tv" /var/lib/torrent-tv/bin/torrent-tv
run install -m 0644 deploy/systemd/qbittorrent-nox.service /etc/systemd/system/qbittorrent-nox.service
run install -m 0644 deploy/systemd/torrent-tv.service /etc/systemd/system/torrent-tv.service
run sed -i "s#/srv/filelist-downloads#$download_root#g; s|@DOWNLOAD_ROOT@|$download_root|g" /etc/systemd/system/qbittorrent-nox.service /etc/systemd/system/torrent-tv.service
run install -m 0644 deploy/systemd/torrent-tv.logrotate /etc/logrotate.d/torrent-tv
run systemctl daemon-reload
run systemctl enable --now qbittorrent-nox.service
run systemctl enable --now torrent-tv.service

echo "Setup complete. Configure qBittorrent's save path as $download_root and bind its Web UI to 127.0.0.1:8080."
echo "Find qBittorrent's temporary password with: journalctl -u qbittorrent-nox --no-pager | grep -i password"
echo "Then configure Torrent TV in a browser at http://SERVER_LAN_IP:8097."
