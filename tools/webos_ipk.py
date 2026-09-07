#!/usr/bin/env python3
"""Build and validate a webOS IPK package using official LG tooling."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile

MAX_UNCOMPRESSED_SIZE = 128 * 1024 * 1024

PACKAGE_ID_PATTERN = re.compile(r"^[a-z0-9]+(?:\.[a-z0-9]+)+$")
PROHIBITED_ID_PREFIXES = ("com.palm.", "com.webos.", "com.lge.")

# Exact pin of @webos-tools/cli in clients/webos/package.json. Every
# ares-package binary used for packing must self-report this version; any
# other CLI (global install, newer/older release) must fail loudly instead
# of silently producing a package with different tooling behavior.
ARES_CLI_VERSION = "3.2.5"
ARES_VERSION_PATTERN = re.compile(r"Version:\s*(\S+)")

REQUIRED_APPINFO_FIELDS: frozenset[str] = frozenset({
    "id",
    "version",
    "vendor",
    "type",
    "main",
    "title",
    "icon",
    "largeIcon",
    "bgImage",
    "splashBackground",
    "disableBackHistoryAPI",
})

PROHIBITED_SAMSUNG_KEYS: frozenset[str] = frozenset({
    "tizen",
    "widget",
    "privilege",
    "feature",
    "config.xml",
    "wgt",
    "avplay",
})

REQUIRED_RUNTIME_SCRIPTS: frozenset[str] = frozenset({
    "app.js",
    "app.css",
    "index.html",
    "fatal-error.js",
    "startup.js",
    "webOSTV.js",
    "webOSTV-dev.js",
})

VENDORED_SDK_FILES: frozenset[str] = frozenset({
    "webOSTV.js",
    "webOSTV-dev.js",
})

ICON_DIMENSIONS: dict[str, tuple[int, int] | tuple[tuple[int, int], ...]] = {
    "icon.png": (80, 80),
    "largeIcon.png": (130, 130),
    "splashBackground.png": (1920, 1080),
    "bgImage.png": ((1920, 1080), (960, 540)),
}

MODULE_SCRIPT = re.compile(rb"<script\b[^>]*\btype\s*=\s*[\"']module[\"']", re.I)
MODULE_PRELOAD = re.compile(rb"<link\b[^>]*\brel\s*=\s*[\"']modulepreload[\"']", re.I)

ES_MODULE_PATTERNS: tuple[re.Pattern[bytes], ...] = (
    re.compile(rb"^\s*export\s+(?:default|const|let|var|function|class|async|\*|\{)", re.M),
    re.compile(rb"^\s*import\s+(?:(?:\*|[\w{},*\s]+)\s+from|[\"'])", re.M),
    re.compile(rb"\bimport\s*\("),
    re.compile(rb"\bimport\.meta\b"),
)

DEV_ARTIFACT_PATTERNS: tuple[re.Pattern[str], ...] = (
    re.compile(r"(?:^|/)\.build(?:/|$)"),
    re.compile(r"(?:^|/)\.git(?:/|$)"),
    re.compile(r"(?:^|/)node_modules(?:/|$)"),
    re.compile(r"\.ts$"),
    re.compile(r"\.map$"),
    re.compile(r"(?:^|/)tests?(?:/|$)"),
    re.compile(r"(?:^|/)__tests__(?:/|$)"),
    re.compile(r"(?:^|/)\.DS_Store$"),
    re.compile(r"~$"),
)


class WebosIpkError(ValueError):
    """An invalid or incompatible webOS IPK package."""


IPKError = WebosIpkError


def parse_png_dimensions(data: bytes) -> tuple[int, int]:
    if len(data) < 24 or data[:8] != b"\x89PNG\r\n\x1a\n":
        raise WebosIpkError("asset is not a valid PNG image")
    width, height = struct.unpack(">II", data[16:24])
    return width, height


def read_root_version(version_file: Path | None = None) -> str:
    if version_file is None:
        version_file = Path(__file__).resolve().parents[1] / "VERSION"
    if not version_file.is_file():
        raise WebosIpkError(f"root VERSION file not found at {version_file}")
    version = version_file.read_text(encoding="utf-8").strip()
    if not version:
        raise WebosIpkError(f"root VERSION file {version_file} is empty")
    return version


def read_ar_archive(data: bytes) -> dict[str, bytes]:
    if not data.startswith(b"!<arch>\n"):
        raise WebosIpkError("package is not an ar archive (missing !<arch>\\n magic)")

    members: dict[str, bytes] = {}
    offset = 8
    total_len = len(data)

    while offset < total_len:
        header = data[offset : offset + 60]
        if len(header) < 60:
            if not header.strip():
                break
            raise WebosIpkError("truncated ar archive member header")

        name = header[:16].decode("latin1").strip().rstrip("/")
        size_str = header[48:58].decode("latin1").strip()
        trailer = header[58:60]

        if trailer != b"`\n":
            raise WebosIpkError(f"corrupt ar header trailer for member {name!r}")

        try:
            size = int(size_str)
        except ValueError as exc:
            raise WebosIpkError(f"invalid ar member size {size_str!r} for member {name!r}") from exc

        offset += 60
        if offset + size > total_len:
            raise WebosIpkError(f"ar member {name!r} exceeds archive bounds")

        members[name] = data[offset : offset + size]
        offset += size
        if size % 2 != 0:
            offset += 1

    expected_members = ["debian-binary", "control.tar.gz", "data.tar.gz"]
    actual_members = list(members.keys())
    if actual_members != expected_members:
        raise WebosIpkError(
            f"ar members must be exactly {expected_members}, got {actual_members}"
        )

    return members


def parse_control_fields(control_bytes: bytes) -> dict[str, str]:
    fields: dict[str, str] = {}
    for line in control_bytes.decode("utf-8", errors="replace").splitlines():
        if not line or line.startswith("#"):
            continue
        if ":" in line:
            key, val = line.split(":", 1)
            fields[key.strip()] = val.strip()
    return fields


def validate_control_fields(
    control_bytes: bytes,
    expected_id: str | None = None,
    expected_version: str | None = None,
) -> dict[str, str]:
    fields = parse_control_fields(control_bytes)

    pkg = fields.get("Package")
    if not pkg:
        raise WebosIpkError("control file missing required 'Package' field")
    if expected_id and pkg != expected_id:
        raise WebosIpkError(f"control Package {pkg!r} does not match expected {expected_id!r}")

    if not PACKAGE_ID_PATTERN.fullmatch(pkg):
        raise WebosIpkError(f"invalid package ID {pkg!r} in control file")
    for prefix in PROHIBITED_ID_PREFIXES:
        if pkg.startswith(prefix):
            raise WebosIpkError(f"package ID {pkg!r} starts with prohibited prefix {prefix!r}")

    version = fields.get("Version")
    if not version:
        raise WebosIpkError("control file missing required 'Version' field")
    if expected_version and version != expected_version:
        raise WebosIpkError(
            f"control Version {version!r} does not match expected {expected_version!r}"
        )

    arch = fields.get("Architecture")
    if arch != "all":
        raise WebosIpkError(f"control Architecture must be 'all', got {arch!r}")

    return fields


def validate_appinfo(
    appinfo_bytes: bytes,
    expected_id: str | None = None,
    expected_version: str | None = None,
) -> dict:
    try:
        appinfo = json.loads(appinfo_bytes.decode("utf-8"))
    except Exception as exc:
        raise WebosIpkError(f"appinfo.json is not valid JSON: {exc}") from exc

    if not isinstance(appinfo, dict):
        raise WebosIpkError("appinfo.json root must be a JSON object")

    missing = REQUIRED_APPINFO_FIELDS - set(appinfo.keys())
    if missing:
        raise WebosIpkError(f"appinfo.json missing required fields: {sorted(missing)}")

    pkg_id = appinfo["id"]
    if not isinstance(pkg_id, str) or not PACKAGE_ID_PATTERN.fullmatch(pkg_id):
        raise WebosIpkError(f"appinfo.json id {pkg_id!r} is invalid")
    for prefix in PROHIBITED_ID_PREFIXES:
        if pkg_id.startswith(prefix):
            raise WebosIpkError(f"appinfo.json id {pkg_id!r} starts with prohibited prefix {prefix!r}")
    if expected_id and pkg_id != expected_id:
        raise WebosIpkError(f"appinfo.json id {pkg_id!r} does not match expected {expected_id!r}")

    version = appinfo["version"]
    if not isinstance(version, str):
        raise WebosIpkError(f"appinfo.json version must be a string, got {type(version).__name__}")
    if expected_version and version != expected_version:
        raise WebosIpkError(
            f"appinfo.json version {version!r} does not match expected {expected_version!r}"
        )

    if appinfo.get("type") != "web":
        raise WebosIpkError(f"appinfo.json type must be 'web', got {appinfo.get('type')!r}")

    if appinfo.get("main") != "index.html":
        raise WebosIpkError(f"appinfo.json main must be 'index.html', got {appinfo.get('main')!r}")

    if not isinstance(appinfo.get("vendor"), str) or not appinfo["vendor"].strip():
        raise WebosIpkError("appinfo.json vendor must be a non-empty string")

    if appinfo.get("disableBackHistoryAPI") is not True:
        raise WebosIpkError("appinfo.json disableBackHistoryAPI must be true")

    samsung_keys = set(appinfo.keys()) & PROHIBITED_SAMSUNG_KEYS
    if samsung_keys:
        raise WebosIpkError(f"appinfo.json contains prohibited Samsung/Tizen fields: {sorted(samsung_keys)}")

    return appinfo


def validate_archive(
    file: Path,
    target_version: str | None = None,
    version_file: Path | None = None,
) -> str:
    if not file.is_file():
        raise WebosIpkError(f"package file {file} does not exist")
    if file.suffix != ".ipk":
        raise WebosIpkError(f"package filename must end in .ipk, got {file.name}")

    if target_version is None:
        target_version = read_root_version(version_file)

    data = file.read_bytes()
    members = read_ar_archive(data)

    debian_binary = members["debian-binary"].decode("latin1").strip()
    if debian_binary != "2.0":
        raise WebosIpkError(f"debian-binary member content must be '2.0', got {debian_binary!r}")

    try:
        with tarfile.open(fileobj=io.BytesIO(members["control.tar.gz"]), mode="r:gz") as ctar:
            control_member = next((m for m in ctar.getmembers() if m.name in ("control", "./control")), None)
            if control_member is None:
                raise WebosIpkError("control.tar.gz does not contain 'control' member")
            control_file = ctar.extractfile(control_member)
            if control_file is None:
                raise WebosIpkError("cannot read 'control' file in control.tar.gz")
            control_bytes = control_file.read()
    except (tarfile.TarError, OSError) as exc:
        raise WebosIpkError(f"cannot read control.tar.gz: {exc}") from exc

    control_fields = validate_control_fields(control_bytes, expected_version=target_version)
    pkg_id = control_fields["Package"]

    data_entries: dict[str, bytes] = {}
    total_uncompressed = 0
    app_prefix = f"usr/palm/applications/{pkg_id}/"

    try:
        with tarfile.open(fileobj=io.BytesIO(members["data.tar.gz"]), mode="r:gz") as dtar:
            for member in dtar.getmembers():
                name = member.name
                path = PurePosixPath(name)
                if not name or name.startswith("/") or "\\" in name or ".." in path.parts:
                    raise WebosIpkError(f"unsafe archive path {name!r}")

                for pattern in DEV_ARTIFACT_PATTERNS:
                    if pattern.search(name):
                        raise WebosIpkError(f"package contains dev artifact {name!r}")

                if member.isreg():
                    total_uncompressed += member.size
                    if total_uncompressed > MAX_UNCOMPRESSED_SIZE:
                        raise WebosIpkError("uncompressed package exceeds 128 MiB validation limit")
                    f = dtar.extractfile(member)
                    if f is not None:
                        data_entries[name] = f.read()
    except (tarfile.TarError, OSError) as exc:
        raise WebosIpkError(f"cannot read data.tar.gz: {exc}") from exc

    app_files: dict[str, bytes] = {}
    for name, content in data_entries.items():
        if name.startswith(app_prefix):
            rel = name[len(app_prefix):]
            app_files[rel] = content

    appinfo_key = "appinfo.json"
    if appinfo_key not in app_files:
        raise WebosIpkError(f"appinfo.json missing from application path {app_prefix}")

    appinfo = validate_appinfo(
        app_files[appinfo_key],
        expected_id=pkg_id,
        expected_version=target_version,
    )

    for asset_field in ("main", "icon", "largeIcon", "bgImage", "splashBackground"):
        asset_name = appinfo[asset_field]
        if asset_name not in app_files:
            raise WebosIpkError(
                f"appinfo.json references {asset_field} {asset_name!r} which is not packaged"
            )

    for icon_name, expected_dims in ICON_DIMENSIONS.items():
        if icon_name in app_files:
            actual_dims = parse_png_dimensions(app_files[icon_name])
            if isinstance(expected_dims[0], tuple):
                if actual_dims not in expected_dims:
                    raise WebosIpkError(
                        f"{icon_name} must be one of {expected_dims} pixels, got {actual_dims}"
                    )
            else:
                if actual_dims != expected_dims:
                    raise WebosIpkError(
                        f"{icon_name} must be {expected_dims[0]}x{expected_dims[1]} pixels, got {actual_dims[0]}x{actual_dims[1]}"
                    )

    for script in REQUIRED_RUNTIME_SCRIPTS:
        if script not in app_files:
            raise WebosIpkError(f"required runtime script or file {script!r} is missing from package")

    for sdk_file in VENDORED_SDK_FILES:
        if sdk_file not in app_files:
            raise WebosIpkError(f"vendored SDK file {sdk_file!r} is missing from package")
    main_entry = appinfo.get("main", "index.html")
    if main_entry in app_files:
        main_content = app_files[main_entry]
        if MODULE_SCRIPT.search(main_content) or MODULE_PRELOAD.search(main_content):
            raise WebosIpkError(
                f"{main_entry} must use classic scripts, not ES module launcher tags"
            )


    for filename, content in app_files.items():
        if filename.endswith(".js"):
            for pattern in ES_MODULE_PATTERNS:
                if pattern.search(content):
                    raise WebosIpkError(
                        f"{filename} still contains an ES module import or export"
                    )

    for filename, content in app_files.items():
        if filename.endswith((".js", ".html", ".css")):
            if b"$WEBAPIS" in content or b"webapis.js" in content:
                raise WebosIpkError(
                    f"{filename} references Samsung $WEBAPIS"
                )

    return (
        f"Compatible webOS package structure: id={pkg_id}, version={appinfo['version']}, "
        f"vendor={appinfo['vendor']}, architecture=all"
    )


def write_checksum(file: Path) -> str:
    digest = hashlib.sha256(file.read_bytes()).hexdigest()
    file.with_name(file.name + ".sha256").write_text(f"{digest}  {file.name}\n", encoding="utf-8")
    return digest


def query_ares_version(cmd: list[str]) -> str:
    try:
        res = subprocess.run(
            [*cmd, "--version"],
            capture_output=True,
            text=True,
            check=False,
            timeout=10,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise WebosIpkError(f"failed to execute ares-package at {cmd[0]}: {exc}") from exc

    combined = (res.stdout + "\n" + res.stderr).strip()
    match = ARES_VERSION_PATTERN.search(combined)
    if not match:
        match = re.search(r"\b([0-9]+\.[0-9]+\.[0-9]+)\b", combined)
    if not match:
        raise WebosIpkError(
            f"ares-package at {cmd[0]} did not report a recognizable version; output was {combined!r}"
        )
    return match.group(1)


def find_ares_package(custom_bin: str | None = None) -> list[str]:
    if custom_bin:
        custom_path = Path(custom_bin)
        cmd = [str(custom_path)] if custom_path.is_file() and os.access(custom_path, os.X_OK) else [custom_bin]
        version = query_ares_version(cmd)
        if version != ARES_CLI_VERSION:
            raise WebosIpkError(
                f"ares-package at {cmd[0]} must report version {ARES_CLI_VERSION} (pinned @webos-tools/cli), got {version!r}"
            )
        return cmd

    repo_root = Path(__file__).resolve().parents[1]
    candidates: list[Path] = [
        repo_root / "clients/webos/node_modules/.bin/ares-package",
        repo_root / "node_modules/.bin/ares-package",
    ]
    which = shutil.which("ares-package")
    if which:
        candidates.append(Path(which))

    tested: list[str] = []
    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            cmd = [str(candidate)]
            try:
                version = query_ares_version(cmd)
            except WebosIpkError as exc:
                tested.append(f"{candidate} (error: {exc})")
                continue
            if version == ARES_CLI_VERSION:
                return cmd
            tested.append(f"{candidate} (version {version!r} != {ARES_CLI_VERSION!r})")

    details = "; ".join(tested) if tested else "no candidate binaries found"
    raise WebosIpkError(
        f"ares-package CLI matching pinned version {ARES_CLI_VERSION} not found ({details}); "
        "install the pinned @webos-tools/cli via npm install in clients/webos"
    )


def pack(
    source: Path,
    appinfo: Path,
    icons: dict[str, Path],
    output: Path,
    target_version: str | None = None,
    version_file: Path | None = None,
    ares_bin: str | None = None,
) -> str:
    if not source.is_dir():
        raise WebosIpkError(f"frontend source {source} is not a directory; build it first")
    if not appinfo.is_file():
        raise WebosIpkError(f"appinfo file {appinfo} does not exist")

    for name, path in icons.items():
        if not path.is_file():
            raise WebosIpkError(f"required icon asset {name} not found at {path}")

    if output.suffix != ".ipk":
        raise WebosIpkError("output filename must end in .ipk")

    if target_version is None:
        target_version = read_root_version(version_file)

    ares_cmd = find_ares_package(ares_bin)

    with tempfile.TemporaryDirectory(prefix="webos-stage-") as tmpdir:
        stage_dir = Path(tmpdir) / "app"
        stage_dir.mkdir(parents=True)
        out_dir = Path(tmpdir) / "out"
        out_dir.mkdir(parents=True)

        for item in sorted(source.rglob("*")):
            if item.is_file():
                rel = item.relative_to(source)
                dest = stage_dir / rel
                dest.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(item, dest)

        try:
            appinfo_raw = json.loads(appinfo.read_text(encoding="utf-8"))
        except Exception as exc:
            raise WebosIpkError(f"appinfo file {appinfo} is not valid JSON: {exc}") from exc
        appinfo_raw["version"] = target_version
        (stage_dir / "appinfo.json").write_text(json.dumps(appinfo_raw, indent=2), encoding="utf-8")

        for name, path in icons.items():
            dest = stage_dir / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, dest)

        cmd = [*ares_cmd, str(stage_dir), "-o", str(out_dir)]
        try:
            res = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                check=False,
            )
        except OSError as exc:
            raise WebosIpkError(f"failed to run ares-package: {exc}") from exc

        if res.returncode != 0:
            err = (res.stderr or res.stdout or "").strip()
            raise WebosIpkError(f"ares-package failed (exit code {res.returncode}): {err}")

        generated_ipks = list(out_dir.glob("*.ipk"))
        if not generated_ipks:
            raise WebosIpkError(
                f"ares-package succeeded but produced no .ipk files in {out_dir}; stdout: {res.stdout.strip()}"
            )

        pkg_file = generated_ipks[0]
        try:
            report = validate_archive(pkg_file, target_version=target_version, version_file=version_file)
            output.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(pkg_file, output)
            return report
        except Exception:
            if output.is_file():
                try:
                    output.unlink()
                except OSError:
                    pass
            raise


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)

    pack_cmd = commands.add_parser("pack")
    pack_cmd.add_argument("--source", type=Path, required=True, help="Path to dist directory")
    pack_cmd.add_argument("--appinfo", type=Path, required=True, help="Path to appinfo.json")
    pack_cmd.add_argument("--icon", type=Path, default=None, help="Path to icon.png")
    pack_cmd.add_argument("--large-icon", type=Path, default=None, help="Path to largeIcon.png")
    pack_cmd.add_argument("--bg-image", type=Path, default=None, help="Path to bgImage.png")
    pack_cmd.add_argument("--splash-background", type=Path, default=None, help="Path to splashBackground.png")
    pack_cmd.add_argument("--output", type=Path, required=True, help="Output .ipk path")
    pack_cmd.add_argument("--target-version", type=str, default=None, help="Target webOS version (default: root VERSION)")
    pack_cmd.add_argument("--version-file", type=Path, default=None, help="Path to VERSION file")
    pack_cmd.add_argument("--ares-bin", type=str, default=None, help="Path to ares-package executable")

    validate_cmd = commands.add_parser("validate")
    validate_cmd.add_argument("--file", type=Path, required=True, help="Path to .ipk package")
    validate_cmd.add_argument("--target-version", type=str, default=None, help="Expected version (default: root VERSION)")
    validate_cmd.add_argument("--version-file", type=Path, default=None, help="Path to VERSION file")

    args = parser.parse_args()

    try:
        if args.command == "pack":
            appinfo_dir = args.appinfo.parent
            icons: dict[str, Path] = {
                "icon.png": args.icon or (appinfo_dir / "icon.png"),
                "largeIcon.png": args.large_icon or (appinfo_dir / "largeIcon.png"),
                "bgImage.png": args.bg_image or (appinfo_dir / "bgImage.png"),
                "splashBackground.png": args.splash_background or (appinfo_dir / "splashBackground.png"),
            }
            report = pack(
                source=args.source,
                appinfo=args.appinfo,
                icons=icons,
                output=args.output,
                target_version=args.target_version,
                version_file=args.version_file,
                ares_bin=args.ares_bin,
            )
            digest = write_checksum(args.output)
            print(f"Created {args.output}\nSHA-256: {digest}\n{report}")
        else:
            report = validate_archive(
                file=args.file,
                target_version=args.target_version,
                version_file=args.version_file,
            )
            print(report)
    except WebosIpkError as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
