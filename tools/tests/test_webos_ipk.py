#!/usr/bin/env python3
"""Unit tests for webOS IPK packager and validator."""

from __future__ import annotations

import io
import json
from pathlib import Path
import re
import struct
import tarfile
import tempfile
import unittest
import zlib

import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import webos_ipk

ROOT_VERSION = (Path(__file__).resolve().parents[2] / "VERSION").read_text().strip()


def png(width: int, height: int) -> bytes:
    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    header = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    rows = b"".join(b"\0" + b"\0\0\0\xff" * width for _ in range(height))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b"")


def make_tar_gz(files: dict[str, bytes]) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for name, content in sorted(files.items()):
            ti = tarfile.TarInfo(name=name)
            ti.size = len(content)
            ti.mtime = 1600000000
            ti.mode = 0o644
            tar.addfile(ti, io.BytesIO(content))
    return buf.getvalue()


def make_ar(members: list[tuple[str, bytes]]) -> bytes:
    buf = bytearray(b"!<arch>\n")
    for name, content in members:
        header = f"{name:<16}{1600000000:<12}{0:<6}{0:<6}{100644:<8}{len(content):<10}`\n".encode("latin1")
        buf.extend(header)
        buf.extend(content)
        if len(content) % 2 != 0:
            buf.extend(b"\n")
    return bytes(buf)


VALID_APPINFO = {
    "id": "com.torrenttv.app",
    "version": ROOT_VERSION,
    "vendor": "torrent-tv",
    "type": "web",
    "main": "index.html",
    "title": "Torrent TV",
    "icon": "icon.png",
    "largeIcon": "largeIcon.png",
    "bgImage": "bgImage.png",
    "splashBackground": "splashBackground.png",
    "disableBackHistoryAPI": True,
}

VALID_CONTROL = (
    "Package: com.torrenttv.app\n"
    f"Version: {ROOT_VERSION}\n"
    "Architecture: all\n"
    "Maintainer: torrent-tv\n"
    "Description: Torrent TV\n"
).encode("utf-8")


class WebosIpkTests(unittest.TestCase):
    def make_ipk(
        self,
        *,
        appinfo_dict: dict | None = None,
        control_bytes: bytes | None = None,
        debian_binary: bytes = b"2.0\n",
        extra_data: dict[str, bytes] | None = None,
        remove_data: set[str] | None = None,
        ar_members_override: list[tuple[str, bytes]] | None = None,
        pkg_id: str = "com.torrenttv.app",
    ) -> bytes:
        if ar_members_override is not None:
            return make_ar(ar_members_override)

        appinfo = dict(VALID_APPINFO if appinfo_dict is None else appinfo_dict)
        control = VALID_CONTROL if control_bytes is None else control_bytes

        pkg_prefix = f"usr/palm/applications/{pkg_id}/"
        data_files: dict[str, bytes] = {
            pkg_prefix + "appinfo.json": json.dumps(appinfo).encode("utf-8"),
            pkg_prefix + "index.html": b"<!doctype html><html><body></body></html>",
            pkg_prefix + "app.js": b"(function() { var x = 1; })();",
            pkg_prefix + "app.css": b"body { margin: 0; }",
            pkg_prefix + "startup.js": b"window.FileListBoot = {};",
            pkg_prefix + "fatal-error.js": b"window.FileListFatalError = {};",
            pkg_prefix + "webOSTV.js": b"window.webOS = {};",
            pkg_prefix + "webOSTV-dev.js": b"window.webOSDev = {};",
            pkg_prefix + "icon.png": png(80, 80),
            pkg_prefix + "largeIcon.png": png(130, 130),
            pkg_prefix + "splashBackground.png": png(1920, 1080),
            pkg_prefix + "bgImage.png": png(1920, 1080),
        }

        if extra_data:
            data_files.update(extra_data)
        if remove_data:
            for k in remove_data:
                data_files.pop(k, None)

        control_files = {"control": control}

        return make_ar([
            ("debian-binary", debian_binary),
            ("control.tar.gz", make_tar_gz(control_files)),
            ("data.tar.gz", make_tar_gz(data_files)),
        ])

    def write_ipk_file(self, content: bytes, directory: Path, name: str = "app.ipk") -> Path:
        p = directory / name
        p.write_bytes(content)
        return p

    def test_valid_fixture_passes_validation(self):
        with tempfile.TemporaryDirectory() as td:
            ipk_path = self.write_ipk_file(self.make_ipk(), Path(td))
            report = webos_ipk.validate_archive(ipk_path, target_version=ROOT_VERSION)
            self.assertIn("Compatible webOS package structure", report)
            self.assertIn("id=com.torrenttv.app", report)
            self.assertIn(f"version={ROOT_VERSION}", report)
            self.assertIn("architecture=all", report)

    def test_rejects_missing_or_wrong_extension(self):
        with tempfile.TemporaryDirectory() as td:
            missing = Path(td) / "missing.ipk"
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "does not exist"):
                webos_ipk.validate_archive(missing)

            wrong_ext = Path(td) / "app.wgt"
            wrong_ext.write_bytes(b"data")
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "must end in \\.ipk"):
                webos_ipk.validate_archive(wrong_ext)

    def test_rejects_non_ar_archive(self):
        with tempfile.TemporaryDirectory() as td:
            bad = self.write_ipk_file(b"PK\x03\x04not-an-ar", Path(td))
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "missing !<arch>"):
                webos_ipk.validate_archive(bad)

    def test_rejects_unexpected_ar_members(self):
        with tempfile.TemporaryDirectory() as td:
            # Missing debian-binary
            content = make_ar([
                ("control.tar.gz", make_tar_gz({"control": VALID_CONTROL})),
                ("data.tar.gz", make_tar_gz({})),
            ])
            bad = self.write_ipk_file(content, Path(td))
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "ar members must be exactly"):
                webos_ipk.validate_archive(bad)

            # Extra member
            content_extra = make_ar([
                ("debian-binary", b"2.0\n"),
                ("control.tar.gz", make_tar_gz({"control": VALID_CONTROL})),
                ("data.tar.gz", make_tar_gz({})),
                ("extra-member", b"surprise"),
            ])
            bad_extra = self.write_ipk_file(content_extra, Path(td))
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "ar members must be exactly"):
                webos_ipk.validate_archive(bad_extra)

    def test_rejects_invalid_debian_binary_version(self):
        with tempfile.TemporaryDirectory() as td:
            bad = self.write_ipk_file(self.make_ipk(debian_binary=b"1.0\n"), Path(td))
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "debian-binary member content must be '2.0'"):
                webos_ipk.validate_archive(bad)

    def test_rejects_missing_control_file(self):
        with tempfile.TemporaryDirectory() as td:
            content = make_ar([
                ("debian-binary", b"2.0\n"),
                ("control.tar.gz", make_tar_gz({"other": b"stuff"})),
                ("data.tar.gz", make_tar_gz({})),
            ])
            bad = self.write_ipk_file(content, Path(td))
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "does not contain 'control' member"):
                webos_ipk.validate_archive(bad)

    def test_rejects_control_field_violations(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)

            # Missing package field
            bad1 = self.write_ipk_file(
                self.make_ipk(control_bytes=f"Version: {ROOT_VERSION}\nArchitecture: all\n".encode()),
                root, name="c1.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "missing required 'Package' field"):
                webos_ipk.validate_archive(bad1)

            # Prohibited package ID prefix (com.palm, com.webos, com.lge)
            for prefix in ("com.palm.app", "com.webos.app", "com.lge.app"):
                bad_prefix = self.write_ipk_file(
                    self.make_ipk(
                        control_bytes=f"Package: {prefix}\nVersion: {ROOT_VERSION}\nArchitecture: all\n".encode(),
                        pkg_id=prefix,
                    ),
                    root, name=f"{prefix}.ipk"
                )
                with self.assertRaisesRegex(webos_ipk.WebosIpkError, "prohibited prefix"):
                    webos_ipk.validate_archive(bad_prefix)

            # Architecture not "all"
            bad_arch = self.write_ipk_file(
                self.make_ipk(control_bytes=f"Package: com.torrenttv.app\nVersion: {ROOT_VERSION}\nArchitecture: arm\n".encode()),
                root, name="c2.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "control Architecture must be 'all'"):
                webos_ipk.validate_archive(bad_arch)

            # Version mismatch
            bad_ver = self.write_ipk_file(
                self.make_ipk(control_bytes=b"Package: com.torrenttv.app\nVersion: 1.0.0\nArchitecture: all\n"),
                root, name="c3.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "does not match expected '0.6.0'"):
                webos_ipk.validate_archive(bad_ver, target_version="0.6.0")

    def test_rejects_missing_appinfo_json(self):
        with tempfile.TemporaryDirectory() as td:
            bad = self.write_ipk_file(
                self.make_ipk(remove_data={"usr/palm/applications/com.torrenttv.app/appinfo.json"}),
                Path(td),
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "appinfo\\.json missing"):
                webos_ipk.validate_archive(bad)

    def test_rejects_appinfo_missing_required_fields(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for field in ("type", "main", "vendor", "icon", "largeIcon", "bgImage", "splashBackground"):
                broken = dict(VALID_APPINFO)
                broken.pop(field)
                bad = self.write_ipk_file(self.make_ipk(appinfo_dict=broken), root, name=f"miss_{field}.ipk")
                with self.subTest(field=field):
                    with self.assertRaisesRegex(webos_ipk.WebosIpkError, f"missing required fields.*{field}"):
                        webos_ipk.validate_archive(bad)

    def test_rejects_disable_back_history_api_not_true(self):
        with tempfile.TemporaryDirectory() as td:
            broken = dict(VALID_APPINFO, disableBackHistoryAPI=False)
            bad = self.write_ipk_file(self.make_ipk(appinfo_dict=broken), Path(td))
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "disableBackHistoryAPI must be true"):
                webos_ipk.validate_archive(bad)

    def test_rejects_samsung_fields_in_appinfo(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for sam_key in ("tizen", "widget", "privilege", "feature", "config.xml", "wgt", "avplay"):
                broken = dict(VALID_APPINFO, **{sam_key: "forbidden"})
                bad = self.write_ipk_file(self.make_ipk(appinfo_dict=broken), root, name=f"sam_{sam_key}.ipk")
                with self.subTest(key=sam_key):
                    with self.assertRaisesRegex(webos_ipk.WebosIpkError, "prohibited Samsung/Tizen fields"):
                        webos_ipk.validate_archive(bad)

    def test_rejects_missing_referenced_assets(self):
        with tempfile.TemporaryDirectory() as td:
            bad = self.write_ipk_file(
                self.make_ipk(remove_data={"usr/palm/applications/com.torrenttv.app/icon.png"}),
                Path(td),
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "references icon 'icon\\.png' which is not packaged"):
                webos_ipk.validate_archive(bad)

    def test_rejects_invalid_icon_dimensions(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)

            # icon.png wrong dimensions
            bad_icon = self.write_ipk_file(
                self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/icon.png": png(117, 117)}),
                root, name="icon_dim.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "icon\\.png must be 80x80 pixels"):
                webos_ipk.validate_archive(bad_icon)

            # largeIcon.png wrong dimensions
            bad_large = self.write_ipk_file(
                self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/largeIcon.png": png(80, 80)}),
                root, name="large_dim.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "largeIcon\\.png must be 130x130 pixels"):
                webos_ipk.validate_archive(bad_large)

            # splashBackground.png wrong dimensions
            bad_splash = self.write_ipk_file(
                self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/splashBackground.png": png(800, 600)}),
                root, name="splash_dim.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "splashBackground\\.png must be 1920x1080 pixels"):
                webos_ipk.validate_archive(bad_splash)

            # bgImage.png wrong dimensions
            bad_bg = self.write_ipk_file(
                self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/bgImage.png": png(500, 500)}),
                root, name="bg_dim.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "bgImage\\.png must be one of"):
                webos_ipk.validate_archive(bad_bg)

    def test_allows_both_bg_image_dimensions(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for dims in ((1920, 1080), (960, 540)):
                ipk = self.write_ipk_file(
                    self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/bgImage.png": png(*dims)}),
                    root, name=f"bg_{dims[0]}.ipk"
                )
                report = webos_ipk.validate_archive(ipk, target_version=ROOT_VERSION)
                self.assertIn("Compatible webOS package structure", report)

    def test_rejects_missing_runtime_scripts(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for script in ("app.js", "app.css", "index.html", "fatal-error.js", "startup.js", "webOSTV.js", "webOSTV-dev.js"):
                key = f"usr/palm/applications/com.torrenttv.app/{script}"
                bad = self.write_ipk_file(self.make_ipk(remove_data={key}), root, name=f"rm_{script}.ipk")
                with self.subTest(script=script):
                    with self.assertRaisesRegex(webos_ipk.WebosIpkError, rf"(?:required runtime script or file '{re.escape(script)}' is missing|references main '{re.escape(script)}')"):
                        webos_ipk.validate_archive(bad)

    def test_rejects_es_module_syntax_in_packaged_js(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            cases = [
                (b"export default function foo() {}", "export default"),
                (b"export const x = 1;", "export const"),
                (b"import { foo } from 'bar';", "import from"),
                (b"import('bar');", "dynamic import"),
                (b"console.log(import.meta.url);", "import.meta"),
            ]
            for script_content, desc in cases:
                bad = self.write_ipk_file(
                    self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/app.js": script_content}),
                    root, name=f"es_{desc}.ipk"
                )
                with self.subTest(desc=desc):
                    with self.assertRaisesRegex(webos_ipk.WebosIpkError, "still contains an ES module import or export"):
                        webos_ipk.validate_archive(bad)

    def test_rejects_webapis_reference(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            bad = self.write_ipk_file(
                self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/app.js": b"var w = window.$WEBAPIS;"}),
                root, name="webapis.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "references Samsung \\$WEBAPIS"):
                webos_ipk.validate_archive(bad)

    def test_rejects_dev_artifacts_leaked_into_package(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dev_files = [
                "usr/palm/applications/com.torrenttv.app/.build/smoke.json",
                "usr/palm/applications/com.torrenttv.app/node_modules/pkg/index.js",
                "usr/palm/applications/com.torrenttv.app/src/index.ts",
                "usr/palm/applications/com.torrenttv.app/app.js.map",
                "usr/palm/applications/com.torrenttv.app/tests/test.js",
            ]
            for dev_path in dev_files:
                bad = self.write_ipk_file(
                    self.make_ipk(extra_data={dev_path: b"dev content"}),
                    root, name="dev.ipk"
                )
                with self.subTest(path=dev_path):
                    with self.assertRaisesRegex(webos_ipk.WebosIpkError, "package contains dev artifact"):
                        webos_ipk.validate_archive(bad)

    def test_rejects_unsafe_archive_paths(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            bad = self.write_ipk_file(
                self.make_ipk(extra_data={"../escaped.txt": b"danger"}),
                root, name="unsafe.ipk"
            )
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "unsafe archive path"):
                webos_ipk.validate_archive(bad)

    def test_checksum_emission(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            ipk = self.write_ipk_file(self.make_ipk(), root, name="torrent-tv-0.6.0-webos.ipk")
            digest = webos_ipk.write_checksum(ipk)
            self.assertEqual(64, len(digest))
            sha_file = root / "torrent-tv-0.6.0-webos.ipk.sha256"
            self.assertTrue(sha_file.is_file())
            content = sha_file.read_text(encoding="utf-8")
            self.assertEqual(f"{digest}  torrent-tv-0.6.0-webos.ipk\n", content)

    def test_rejects_module_launcher_tags_in_packaged_html(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for tag, desc in (
                (b'<script type="module" src="app.js"></script>', "module_script"),
                (b'<link rel="modulepreload" href="app.js">', "modulepreload"),
            ):
                bad_html = b"<!doctype html><html><head>" + tag + b"</head></html>"
                bad = self.write_ipk_file(
                    self.make_ipk(extra_data={"usr/palm/applications/com.torrenttv.app/index.html": bad_html}),
                    root,
                    name=f"bad_{desc}.ipk",
                )
                with self.subTest(desc=desc):
                    with self.assertRaisesRegex(webos_ipk.WebosIpkError, "must use classic scripts, not ES module launcher tags"):
                        webos_ipk.validate_archive(bad)

    def test_find_ares_package_with_valid_version_stub(self):
        with tempfile.TemporaryDirectory() as td:
            stub = Path(td) / "fake-ares-package"
            stub.write_text("#!/bin/sh\necho 'Version: 3.2.5'\n")
            stub.chmod(0o755)
            cmd = webos_ipk.find_ares_package(str(stub))
            self.assertEqual([str(stub)], cmd)

    def test_find_ares_package_rejects_wrong_version_stub(self):
        with tempfile.TemporaryDirectory() as td:
            stub = Path(td) / "fake-ares-package"
            stub.write_text("#!/bin/sh\necho 'Version: 1.0.0'\n")
            stub.chmod(0o755)
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "must report version 3.2.5.*got '1.0.0'"):
                webos_ipk.find_ares_package(str(stub))

    def test_find_ares_package_rejects_unrecognizable_output(self):
        with tempfile.TemporaryDirectory() as td:
            stub = Path(td) / "fake-ares-package"
            stub.write_text("#!/bin/sh\necho 'unrecognized output'\n")
            stub.chmod(0o755)
            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "did not report a recognizable version"):
                webos_ipk.find_ares_package(str(stub))

    def test_pack_rejects_malformed_appinfo_json_with_webos_ipk_error(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            src = root / "src"
            src.mkdir()
            (src / "index.html").write_text("<!doctype html><html><body></body></html>")
            appinfo = root / "appinfo.json"
            appinfo.write_text("{ not valid json: true }")
            icons = {
                "icon.png": root / "icon.png",
                "largeIcon.png": root / "largeIcon.png",
                "splashBackground.png": root / "splashBackground.png",
                "bgImage.png": root / "bgImage.png",
            }
            icons["icon.png"].write_bytes(png(80, 80))
            icons["largeIcon.png"].write_bytes(png(130, 130))
            icons["splashBackground.png"].write_bytes(png(1920, 1080))
            icons["bgImage.png"].write_bytes(png(1920, 1080))
            stub = root / "fake-ares-package"
            stub.write_text("#!/bin/sh\necho 'Version: 3.2.5'\n")
            stub.chmod(0o755)
            out_ipk = root / "out.ipk"

            with self.assertRaisesRegex(webos_ipk.WebosIpkError, "appinfo file .* is not valid JSON"):
                webos_ipk.pack(src, appinfo, icons, out_ipk, target_version="0.6.0", ares_bin=str(stub))
            self.assertFalse(out_ipk.exists())

    def test_pack_cleans_up_and_does_not_leave_output_on_archive_validation_failure(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            src = root / "src"
            src.mkdir()
            (src / "index.html").write_text("<!doctype html><html><body></body></html>")
            appinfo = root / "appinfo.json"
            appinfo.write_text(json.dumps(VALID_APPINFO))
            icons = {
                "icon.png": root / "icon.png",
                "largeIcon.png": root / "largeIcon.png",
                "splashBackground.png": root / "splashBackground.png",
                "bgImage.png": root / "bgImage.png",
            }
            icons["icon.png"].write_bytes(png(80, 80))
            icons["largeIcon.png"].write_bytes(png(130, 130))
            icons["splashBackground.png"].write_bytes(png(1920, 1080))
            icons["bgImage.png"].write_bytes(png(1920, 1080))

            # Stub reports correct version, and on pack writes an invalid IPK archive into out_dir
            stub = root / "fake-ares-package"
            stub.write_text("""#!/bin/sh
if [ "$1" = "-v" ] || [ "$1" = "--version" ]; then
    echo 'Version: 3.2.5'
    exit 0
fi
# Output directory is argument 4: $1=app, $2=-o, $3=out_dir
out_dir="$3"
echo "corrupt-archive" > "$out_dir/bad.ipk"
""")
            stub.chmod(0o755)
            out_ipk = root / "out.ipk"

            with self.assertRaises(webos_ipk.WebosIpkError):
                webos_ipk.pack(src, appinfo, icons, out_ipk, target_version="0.6.0", ares_bin=str(stub))
            self.assertFalse(out_ipk.exists())

    def test_pack_unlinks_preexisting_output_on_archive_validation_failure(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            src = root / "src"
            src.mkdir()
            (src / "index.html").write_text("<!doctype html><html><body></body></html>")
            appinfo = root / "appinfo.json"
            appinfo.write_text(json.dumps(VALID_APPINFO))
            icons = {
                "icon.png": root / "icon.png",
                "largeIcon.png": root / "largeIcon.png",
                "splashBackground.png": root / "splashBackground.png",
                "bgImage.png": root / "bgImage.png",
            }
            icons["icon.png"].write_bytes(png(80, 80))
            icons["largeIcon.png"].write_bytes(png(130, 130))
            icons["splashBackground.png"].write_bytes(png(1920, 1080))
            icons["bgImage.png"].write_bytes(png(1920, 1080))

            stub = root / "fake-ares-package"
            stub.write_text("""#!/bin/sh
if [ "$1" = "-v" ] || [ "$1" = "--version" ]; then
    echo 'Version: 3.2.5'
    exit 0
fi
out_dir="$3"
echo "corrupt-archive" > "$out_dir/bad.ipk"
""")
            stub.chmod(0o755)
            out_ipk = root / "out.ipk"
            out_ipk.write_text("pre-existing stale file")

            with self.assertRaises(webos_ipk.WebosIpkError):
                webos_ipk.pack(src, appinfo, icons, out_ipk, target_version="0.6.0", ares_bin=str(stub))
            self.assertFalse(out_ipk.exists())

if __name__ == "__main__":
    unittest.main()
