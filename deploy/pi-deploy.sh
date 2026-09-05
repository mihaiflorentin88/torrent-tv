#!/bin/sh
set -eu

binary=${1:?usage: pi-deploy.sh arm64-binary systemd-unit logrotate-config}
unit=${2:?missing systemd unit}
logrotate=${3:?missing logrotate config}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
profile=${FILELIST_DEPLOY_CONFIG:-$script_dir/.deploy.local.conf}

configured() {
	key=$1
	[ -f "$profile" ] || return 0
	awk -F= -v wanted="$key" '$1 == wanted {print substr($0, index($0, "=") + 1); exit}' "$profile"
}
prompt() {
	label=$1
	default=$2
	if [ "${DEPLOY_NON_INTERACTIVE:-false}" = true ] || [ ! -t 0 ]; then
		REPLY=$default
		return
	fi
	printf '%s [%s]: ' "$label" "$default" >/dev/tty
	IFS= read -r answer </dev/tty || answer=
	REPLY=${answer:-$default}
}
valid_atom() {
	case "$1" in '' | *[!A-Za-z0-9_./:@-]*) return 1 ;; *) return 0 ;; esac
}
valid_path() {
	case "$1" in /*) valid_atom "$1" ;; *) return 1 ;; esac
}

saved_host=$(configured PI_HOST || true)
prompt "Raspberry Pi SSH target" "${PI_HOST:-${saved_host:-user@server.lan}}"
host=$REPLY
saved_qb_service=$(configured QB_SERVICE || true)
prompt "qBittorrent systemd service" "${saved_qb_service:-qbittorrent-nox.service}"
qb_service=$REPLY
saved_qb_config=$(configured QB_CONFIG_PATH || true)
prompt "qBittorrent config path (auto or absolute)" "${saved_qb_config:-auto}"
qb_config=$REPLY
saved_download_root=$(configured DOWNLOAD_ROOT || true)
prompt "Application download root" "${saved_download_root:-/mnt/sda1/torrent}"
download_root=$REPLY
saved_qb_temp=$(configured QB_TEMP_PATH || true)
prompt "qBittorrent incomplete-download path" "${saved_qb_temp:-${download_root%/}/.incomplete}"
qb_temp=$REPLY
saved_qb_backup=$(configured QB_BACKUP_DIR || true)
prompt "qBittorrent config backup directory" "${saved_qb_backup:-/var/backups/torrent-tv/qbittorrent}"
qb_backup=$REPLY
saved_app_target=$(configured APP_TARGET || true)
# S7: the service-owned bin directory replaced /usr/local/bin. Fresh
# installs and installs whose saved path is an old default migrate to it;
# an explicitly configured custom path is preserved.
app_bin_default=/var/lib/torrent-tv/bin/torrent-tv
app_bin_legacy=/usr/local/bin/torrent-tv
case "$saved_app_target" in
"" | "$app_bin_legacy") prompt_default=$app_bin_default ;;
*) prompt_default=$saved_app_target ;;
esac
prompt "Application binary path" "$prompt_default"
app_target=$REPLY

valid_atom "$host" || {
	echo "SSH target contains unsupported characters" >&2
	exit 2
}
valid_atom "$qb_service" || {
	echo "qBittorrent service contains unsupported characters" >&2
	exit 2
}
[ "$qb_config" = auto ] || valid_path "$qb_config" || {
	echo "qBittorrent config must be 'auto' or an absolute path without spaces" >&2
	exit 2
}
valid_path "$download_root" || {
	echo "download root must be absolute and contain no spaces" >&2
	exit 2
}
valid_path "$qb_temp" || {
	echo "qBittorrent temp path must be absolute and contain no spaces" >&2
	exit 2
}
case "$qb_temp/" in "${download_root%/}/"*) ;; *)
	echo "qBittorrent temp path must be inside the application download root" >&2
	exit 2
	;;
esac
valid_path "$qb_backup" || {
	echo "qBittorrent backup path must be absolute and contain no spaces" >&2
	exit 2
}
valid_path "$app_target" || {
	echo "application path must be absolute and contain no spaces" >&2
	exit 2
}

profile_tmp=$profile.tmp.$$
umask 077
mkdir -p "$(dirname "$profile")"
{
	printf 'PI_HOST=%s\n' "$host"
	printf 'QB_SERVICE=%s\n' "$qb_service"
	printf 'QB_CONFIG_PATH=%s\n' "$qb_config"
	printf 'DOWNLOAD_ROOT=%s\n' "$download_root"
	printf 'QB_TEMP_PATH=%s\n' "$qb_temp"
	printf 'QB_BACKUP_DIR=%s\n' "$qb_backup"
	printf 'APP_TARGET=%s\n' "$app_target"
} >"$profile_tmp"
mv "$profile_tmp" "$profile"

stage="/tmp/torrent-tv-deploy-$$"
case "$stage" in /tmp/torrent-tv-deploy-[0-9]*) ;; *)
	echo "unsafe staging path" >&2
	exit 1
	;;
esac
# Preflight: a dynamically-linked desktop GUI binary needs WebKitGTK at
# runtime. Abort before any staging or installation if the shared library is
# missing; deploy-pi stays package-install-free (see
# deploy/bootstrap-server.sh for full provisioning). Statically-linked pure
# binaries (make build-arm64-headless) carry no webkit dependency, so the
# check would only false-abort — skip it for those.
if ! file "$binary" | grep -q 'statically linked'; then
	if ! ssh "$host" "ldconfig -p | grep -q 'libwebkit2gtk-4\.1\.so\.0'"; then
		echo "ERROR: $host is missing libwebkit2gtk-4.1.so.0 (required by the desktop GUI)." >&2
		echo "Install it with: sudo apt-get install -y libwebkit2gtk-4.1-0" >&2
		echo "Or provision the full runtime with: deploy/bootstrap-server.sh" >&2
		exit 1
	fi
fi

ssh "$host" "install -d -m 700 '$stage'"
cleanup() { ssh "$host" "rm -rf -- '$stage'" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
scp "$binary" "$host:$stage/torrent-tv"
scp "$unit" "$host:$stage/torrent-tv.service"
scp "$logrotate" "$host:$stage/torrent-tv.logrotate"
scp "$repo_root/deploy/qbittorrent/qBittorrent.streaming.conf" "$host:$stage/qBittorrent.streaming.conf"
scp "$repo_root/tools/qbittorrent_config.py" "$host:$stage/qbittorrent_config.py"

ssh "$host" "sudo sh -s -- '$stage' '$qb_service' '$qb_config' '$download_root' '$qb_temp' '$qb_backup' '$app_target'" <<'REMOTE'
set -eu
stage=$1
qb_service=$2
qb_config=$3
download_root=$4
qb_temp=$5
qb_backup=$6
target=$7
service=torrent-tv.service
service_user=torrent-tv
service_group=torrent-tv
app_bin_dir=/var/lib/torrent-tv/bin
owned_target=$app_bin_dir/torrent-tv
legacy_target=/usr/local/bin/torrent-tv
unit_path=/etc/systemd/system/$service
# Fresh installs and both historic defaults move to the service-owned bin
# directory; an explicitly configured custom path is preserved as-is.
legacy_present=false
if [ "$target" = "$legacy_target" ] || [ "$target" = "$owned_target" ]; then
	if [ -e "$legacy_target" ]; then
		legacy_present=true
	fi
	target=$owned_target
fi
previous=${target}.previous
previous_unit=
backup_file=
qb_owner=
qb_group=
qb_mode=
app_phase=false
had_unit=false
had_binary=false
success=false

rollback() {
	[ "$success" = true ] && return 0
	if [ -n "$backup_file" ] && [ -f "$backup_file" ]; then
		cp -p "$backup_file" "$qb_config" || true
		[ -z "$qb_owner" ] || chown "$qb_owner:$qb_group" "$qb_config" || true
		[ -z "$qb_mode" ] || chmod "$qb_mode" "$qb_config" || true
		systemctl restart "$qb_service" || true
	fi
	if [ "$app_phase" = true ]; then
		if [ "$had_unit" = true ]; then
			cp -p -- "$previous_unit" "$unit_path"
			rm -f -- "$previous_unit"
		else
			# A first install that already ran enable --now leaves a wants
			# symlink behind; disable and reload so no missing unit lingers.
			systemctl disable --now "$service" >/dev/null 2>&1 || true
			rm -f -- "$unit_path"
			systemctl daemon-reload || true
		fi
		if [ "$had_binary" = true ] && [ -f "$previous" ]; then
			mv -f -- "$previous" "$target"
		else
			rm -f -- "$target"
		fi
		if [ "$had_unit" = true ]; then
			systemctl daemon-reload || true
			systemctl restart "$service" || true
		fi
	fi
}
trap rollback EXIT INT TERM

test -f "$stage/torrent-tv"
test -f "$stage/torrent-tv.service"
test -f "$stage/torrent-tv.logrotate"
test -f "$stage/qBittorrent.streaming.conf"
test -f "$stage/qbittorrent_config.py"
command -v python3 >/dev/null
getent group qbittorrent >/dev/null

case "$(qbittorrent-nox --version 2>/dev/null | head -n 1)" in *"v4."*) ;; *) echo "Only qBittorrent 4.x config merging is currently supported" >&2; exit 1;; esac
if [ "$qb_config" = auto ]; then
	found=
	for candidate in /var/lib/qbittorrent/qBittorrent/config/qBittorrent.conf /var/lib/qbittorrent/.config/qBittorrent/qBittorrent.conf; do
		if [ -f "$candidate" ]; then
			[ -z "$found" ] || { echo "Multiple qBittorrent configs found; choose one explicitly" >&2; exit 1; }
			found=$candidate
		fi
	done
	[ -n "$found" ] || { echo "No qBittorrent config found" >&2; exit 1; }
	qb_config=$found
fi
test -f "$qb_config"

systemctl stop "$qb_service"
install -d -m 0700 "$qb_backup"
qb_owner=$(stat -c %U "$qb_config")
qb_group=$(stat -c %G "$qb_config")
qb_mode=$(stat -c %a "$qb_config")
backup_file="$qb_backup/qBittorrent.conf.$(date -u +%Y%m%dT%H%M%SZ).$$"
install -m 0600 "$qb_config" "$backup_file"
install -d -m 0770 -o "$qb_owner" -g "$qb_group" "$download_root"
install -d -m 0770 -o "$qb_owner" -g "$qb_group" "$qb_temp"
merged=$(mktemp "$(dirname "$qb_config")/.qBittorrent.conf.XXXXXX")
python3 "$stage/qbittorrent_config.py" --input "$qb_config" --output "$merged" --template "$stage/qBittorrent.streaming.conf" --temp-path "$qb_temp"
chown --reference="$qb_config" "$merged"
chmod --reference="$qb_config" "$merged"
mv -f "$merged" "$qb_config"
systemctl start "$qb_service"
systemctl is-active --quiet "$qb_service"

if ! id "$service_user" >/dev/null 2>&1; then
	useradd --system --home-dir /var/lib/torrent-tv --no-create-home --shell /usr/sbin/nologin "$service_user"
fi
usermod -a -G qbittorrent "$service_user"
install -d -m 0750 -o "$service_user" -g "$service_group" /var/lib/torrent-tv /var/lib/torrent-tv/data /var/lib/torrent-tv/data/logs
install -d -m 0755 -o "$service_user" -g "$service_group" "$app_bin_dir"

app_phase=true
if [ -f "$unit_path" ]; then
	previous_unit=$(mktemp /tmp/filelist-unit-previous.XXXXXX)
	cp -p -- "$unit_path" "$previous_unit"
	had_unit=true
fi
if [ -f "$target" ]; then
	cp -a -- "$target" "$previous"
	had_binary=true
fi

# Stage the new binary and validate it, and only then touch the unit: an
# unusable payload must never deactivate the current installation.
install -m 0755 "$stage/torrent-tv" "${target}.new"
test -x "${target}.new"
test -s "${target}.new"

# The native engine writes media and session state under the download
# root; ProtectSystem=strict must whitelist it inside the unit. The
# shipped ExecStart already points at the service-owned bin directory; a
# custom target is substituted so the unit keeps matching the binary.
sed -i "s|@DOWNLOAD_ROOT@|$download_root|" "$stage/torrent-tv.service"
if [ "$target" != "$owned_target" ]; then
	sed -i "s|^ExecStart=.*|ExecStart=$target serve --data-dir /var/lib/torrent-tv/data|" "$stage/torrent-tv.service"
fi
mv -f -- "${target}.new" "$target"

# Service-user ownership of the default install lets the in-application
# updater stage and swap the binary without privilege escalation. Custom
# targets give the binary the same owner; when the surrounding directory
# cannot follow, updates there are reported as manual-only.
if [ "$target" = "$owned_target" ]; then
	chown "$service_user:$service_group" "$target"
else
	chown "$service_user:$service_group" "$target" 2>/dev/null || true
	if [ "$(stat -c %U "$(dirname -- "$target")")" != "$service_user" ]; then
		echo "NOTE: $(dirname -- "$target") is not owned by $service_user; in-application updates for $target are manual-only (re-run pi-deploy.sh to update)." >&2
	fi
fi

install -m 0644 "$stage/torrent-tv.service" "$unit_path"
install -m 0644 "$stage/torrent-tv.logrotate" /etc/logrotate.d/torrent-tv
systemctl daemon-reload

if ! systemctl enable --now "$service" || ! systemctl restart "$service" || ! systemctl is-active --quiet "$service"; then
	echo "deployment failed; rollback will restore the previous unit and binary" >&2
	exit 1
fi

rm -f -- "$previous"
if [ -n "$previous_unit" ]; then
	rm -f -- "$previous_unit"
fi
if [ "$legacy_present" = true ]; then
	rm -f -- "$legacy_target"
fi
success=true
trap - EXIT INT TERM
rm -rf -- "$stage"
printf 'qBittorrent config backup: %s\n' "$backup_file"
systemctl --no-pager --full status "$service" | sed -n '1,18p'
REMOTE

trap - EXIT INT TERM
echo "Deployed to $host; remembered non-secret choices in $profile"
echo "Open http://<server-lan-address>:8097"
