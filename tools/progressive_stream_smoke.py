#!/usr/bin/env python3
"""Controlled server-side smoke test for an incomplete qBittorrent stream.

The script reads qBittorrent credentials from the server's protected application
settings, never prints them, throttles only the newly-created test torrent, and
removes both the torrent and its files when the test finishes.
"""

from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request


def json_request(url: str, *, method: str = "GET", data: bytes | None = None) -> dict:
    request = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        request.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(request, timeout=45) as response:
        body = response.read()
        return json.loads(body) if body else {}


def qb_opener(settings: dict) -> tuple[urllib.request.OpenerDirector, str]:
    base = settings["qbittorrentUrl"].rstrip("/")
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(jar)
    )
    if not settings["qbittorrentUsername"] and not settings["qbittorrentPassword"]:
        # The bundled sidecar bypasses WebUI authentication for the LAN, so
        # the direct API calls below need no login session.
        return opener, base
    login = urllib.parse.urlencode(
        {
            "username": settings["qbittorrentUsername"],
            "password": settings["qbittorrentPassword"],
        }
    ).encode()
    request = urllib.request.Request(
        base + "/api/v2/auth/login", login, headers={"Referer": base}
    )
    with opener.open(request, timeout=45) as response:
        response.read(32)
        if response.status // 100 != 2 or not list(jar):
            raise RuntimeError("qBittorrent rejected the configured credentials")
    return opener, base


def qb_form(
    opener: urllib.request.OpenerDirector, base: str, endpoint: str, values: dict
) -> None:
    data = urllib.parse.urlencode(values).encode()
    request = urllib.request.Request(
        base + endpoint, data, headers={"Referer": base}
    )
    with opener.open(request, timeout=45) as response:
        if response.status // 100 != 2:
            raise RuntimeError(f"qBittorrent {endpoint} returned HTTP {response.status}")


def qb_json(opener: urllib.request.OpenerDirector, base: str, endpoint: str) -> object:
    with opener.open(base + endpoint, timeout=45) as response:
        return json.loads(response.read())


def fetch_range_bytes(url: str, start: int, end: int) -> bytes:
    """Fetch one byte range and return its body (the decoder's raw input)."""
    request = urllib.request.Request(url, headers={"Range": f"bytes={start}-{end}"})
    with urllib.request.urlopen(request, timeout=180) as response:
        body = response.read()
    expected = end - start + 1
    if response.status != 206 or len(body) != expected:
        raise RuntimeError(f"range {start}-{end} returned HTTP {response.status} and {len(body)} bytes")
    return body


def read_range(url: str, start: int, end: int) -> dict:
    request = urllib.request.Request(url, headers={"Range": f"bytes={start}-{end}"})
    started = time.monotonic()
    with urllib.request.urlopen(request, timeout=180) as response:
        body = response.read()
        expected = end - start + 1
        if response.status != 206 or len(body) != expected:
            raise RuntimeError(
                f"range {start}-{end} returned HTTP {response.status} and {len(body)} bytes"
            )
        return {
            "status": response.status,
            "bytes": len(body),
            "contentRange": response.headers.get("Content-Range"),
            "seconds": round(time.monotonic() - started, 3),
        }


def delete_with_retry(base: str, download_id: str) -> None:
    url = f"{base}/api/v1/downloads/{download_id}"
    for attempt in range(20):
        try:
            json_request(url, method="DELETE")
            return
        except urllib.error.HTTPError as error:
            if error.code != 409 or attempt == 19:
                raise
            time.sleep(0.25)


def parse_packets(probe_json: dict) -> list[tuple[int, int, int]]:
    """Parse ffprobe packet JSON into (stream_index, pts_ms, byte_offset).

    Entries without a readable timestamp are dropped; a missing byte position
    is kept as zero because the timestamp is still a valid measurement (the
    packet is only unclassifiable for window attribution).
    """
    packets: list[tuple[int, int, int]] = []
    for entry in probe_json.get("packets", []):
        pts_time = entry.get("pts_time")
        if pts_time is None:
            continue
        try:
            pts_ms = round(float(pts_time) * 1000)
        except (TypeError, ValueError):
            continue
        try:
            pos = int(entry.get("pos") or 0)
        except (TypeError, ValueError):
            pos = 0
        packets.append((int(entry.get("stream_index", 0)), pts_ms, pos))
    return packets


def window_span(
    packets: list[tuple[int, int, int]], stream_index: int, header_len: int
) -> tuple[int, int] | None:
    """First and last PTS, in packet order, of one stream inside the fetch
    window of a concatenated probe artifact (head bytes + window bytes).
    Packets belong to the window by byte position (pos >= header_len); PTS is
    never used for classification because head and window PTS ranges can
    overlap across discontinuities."""
    first = last = None
    for packet_stream, pts_ms, pos in packets:
        if packet_stream != stream_index or pos < header_len:
            continue
        if first is None:
            first = pts_ms
        last = pts_ms
    return None if first is None else (first, last)


def sync_verdict(
    target_s: float,
    first_pts_ms: int,
    decoded_duration_ms: int,
    trim_ms: int,
    tolerance_ms: int,
) -> tuple[bool, int]:
    """Verdict for anchoring a seek on one measured artifact: trimming
    trim_ms off the decoded audio must place its first sample on the target,
    and the trim must be achievable (non-negative, within the decoded
    duration). Returns (ok, offset_ms) with offset = first_pts + trim -
    target."""
    target_ms = int(round(target_s * 1000))
    offset = first_pts_ms + trim_ms - target_ms
    ok = 0 <= trim_ms < decoded_duration_ms and abs(offset) <= tolerance_ms
    return ok, offset


def pcm_duration_ms(pcm_bytes: int) -> int:
    """Duration of raw 48 kHz stereo s16le PCM (the decode target format)."""
    return pcm_bytes * 1000 // 192_000


def check_sync(
    *,
    download_id: str,
    stream_index: int,
    target_s: float,
    hint_byte: int,
    total_bytes: int,
    window_bytes: int,
    head_bytes: int,
    fetch_range,
    probe_span,
    probe_packets,
    decode_ms,
    tolerance_ms: int = 250,
) -> dict:
    """Verify the anchoring contract end to end at one seek target.

    Fetches the same head+window artifact the decoder consumes, cross-checks
    the server's measured first PTS against an independent packet scan of that
    artifact, and verifies the measured trim is achievable within the
    window-only decoded duration (artifact decode minus head-only decode).
    All side effects are injected: fetch_range, probe_span, probe_packets, and
    decode_ms are runner callables.
    """
    start = max(0, min(hint_byte, total_bytes - window_bytes))
    head = fetch_range(0, head_bytes - 1)
    window = fetch_range(start, start + window_bytes - 1)
    span = probe_span(download_id, start, window_bytes, stream_index)
    local = window_span(parse_packets(probe_packets(head + window)), stream_index, head_bytes)
    server_first = int(span["firstPtsMs"])
    local_first = local[0] if local else None
    measured_match = local_first is not None and abs(local_first - server_first) <= tolerance_ms
    first_pts = local_first if local_first is not None else server_first
    window_ms = decode_ms(head + window) - decode_ms(head)
    trim_ms = int(round(target_s * 1000)) - first_pts
    ok, offset = sync_verdict(target_s, first_pts, window_ms, trim_ms, tolerance_ms)
    return {
        "targetSec": target_s,
        "startByte": start,
        "serverFirstPtsMs": server_first,
        "localFirstPtsMs": local_first,
        "measuredMatch": measured_match,
        "windowMs": window_ms,
        "trimMs": trim_ms,
        "ok": ok and measured_match,
        "offsetMs": offset,
    }


def ffprobe_artifact_packets(ffprobe: str, media: bytes) -> dict:
    """Independent packet scan of one artifact through an ffprobe runner."""
    probe = subprocess.run(
        [ffprobe, "-v", "error", "-show_packets", "-of", "json", "pipe:0"],
        input=media,
        check=False,
        capture_output=True,
        timeout=150,
    )
    if probe.returncode != 0:
        raise RuntimeError(f"ffprobe packet scan failed: {probe.stderr.decode(errors='replace').strip()[:300]}")
    return json.loads(probe.stdout or "{}")


def ffmpeg_decode_ms(ffmpeg: str, media: bytes) -> int:
    """Decode one artifact to the worker's PCM format and return its duration."""
    proc = subprocess.run(
        [ffmpeg, "-v", "error", "-i", "pipe:0", "-map", "0:a:0", "-ar", "48000", "-ac", "2", "-f", "s16le", "pipe:1"],
        input=media,
        check=False,
        capture_output=True,
        timeout=300,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"ffmpeg decode failed: {proc.stderr.decode(errors='replace').strip()[:300]}")
    return pcm_duration_ms(len(proc.stdout))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--release-id", required=True)
    parser.add_argument("--settings")
    parser.add_argument("--base-url", default="http://127.0.0.1:8097")
    parser.add_argument("--limit-bytes", type=int, default=2 * 1024 * 1024)
    parser.add_argument("--ffprobe", action="store_true")
    parser.add_argument("--check-sync", help="comma-separated seek targets in seconds, e.g. 60,980.65,1400")
    parser.add_argument("--stream-index", type=int, default=1, help="audio stream index used with --check-sync")
    parser.add_argument("--window-bytes", type=int, default=16 * 1024 * 1024)
    parser.add_argument("--head-bytes", type=int, default=2 * 1024 * 1024)
    args = parser.parse_args()

    base = args.base_url.rstrip("/")
    settings: dict = {}
    if args.settings:
        with open(args.settings, encoding="utf-8") as handle:
            settings = json.load(handle)
    settings.update(
        {
            "qbittorrentUrl": os.environ.get("TORRENT_TV_QBITTORRENT_URL", settings.get("qbittorrentUrl", "")),
            "qbittorrentUsername": os.environ.get("TORRENT_TV_QBITTORRENT_USERNAME", settings.get("qbittorrentUsername", "")),
            "qbittorrentPassword": os.environ.get("TORRENT_TV_QBITTORRENT_PASSWORD", settings.get("qbittorrentPassword", "")),
        }
    )
    if not settings["qbittorrentUrl"]:
        raise RuntimeError("qBittorrent URL is required; username and password are optional")

    existing = json_request(base + "/api/v1/downloads").get("items", [])
    if any(str(item.get("releaseId")) == args.release_id for item in existing):
        raise RuntimeError("refusing to reuse or delete an existing download")

    prepared: dict | None = None
    opener: urllib.request.OpenerDirector | None = None
    qb_base = ""
    torrent_hash = ""
    try:
        prepared = json_request(
            f"{base}/api/v1/releases/{urllib.parse.quote(args.release_id)}/prepare",
            method="POST",
            data=b"{}",
        )
        engine_id = str(prepared["engineId"])
        if not engine_id.startswith("qb:"):
            raise RuntimeError("prepared download is not managed by qBittorrent")
        torrent_hash = engine_id.removeprefix("qb:")
        opener, qb_base = qb_opener(settings)
        qb_form(
            opener,
            qb_base,
            "/api/v2/torrents/setDownloadLimit",
            {"hashes": torrent_hash, "limit": str(args.limit_bytes)},
        )
        torrent_info = qb_json(
            opener,
            qb_base,
            "/api/v2/torrents/info?hashes=" + urllib.parse.quote(torrent_hash),
        )
        if not isinstance(torrent_info, list) or not torrent_info:
            raise RuntimeError("qBittorrent did not return the prepared torrent")
        if not torrent_info[0].get("seq_dl") or not torrent_info[0].get("f_l_piece_prio"):
            raise RuntimeError("qBittorrent progressive scheduling flags are not enabled")

        stream_url = base + prepared["streamUrl"]
        size = int(prepared["sizeBytes"])
        chunk = min(1024 * 1024, size)
        result = {
            "downloadId": prepared["id"],
            "preparedProgress": prepared["progress"],
            "sequentialDownload": True,
            "firstLastPiecePriority": True,
            "startup": read_range(stream_url, 0, chunk - 1),
            "tail": read_range(stream_url, size - chunk, size - 1),
        }

        if args.ffprobe or args.check_sync:
            probe = subprocess.run(
                [
                    "ffprobe",
                    "-v",
                    "error",
                    "-rw_timeout",
                    "120000000",
                    "-show_entries",
                    "format=format_name,duration",
                    "-of",
                    "json",
                    stream_url,
                ],
                check=False,
                capture_output=True,
                text=True,
                timeout=150,
            )
            result["ffprobe"] = {
                "exitCode": probe.returncode,
                "output": json.loads(probe.stdout or "{}"),
                "error": probe.stderr.strip()[:500],
            }
            if probe.returncode != 0:
                raise RuntimeError("ffprobe could not parse the progressive stream")

        if args.check_sync:
            duration_output = result.get("ffprobe", {}).get("output", {}).get("format", {}).get("duration")
            if not duration_output:
                raise RuntimeError("--check-sync needs the stream duration, but the format probe did not report one")
            duration_s = float(duration_output)
            checks = []
            for target_s in [float(part) for part in str(args.check_sync).split(",") if part.strip()]:
                checks.append(
                    check_sync(
                        download_id=prepared["id"],
                        stream_index=args.stream_index,
                        target_s=target_s,
                        hint_byte=int(round(target_s * size / duration_s)),
                        total_bytes=size,
                        window_bytes=args.window_bytes,
                        head_bytes=args.head_bytes,
                        fetch_range=lambda start, end: fetch_range_bytes(stream_url, start, end),
                        probe_span=lambda download_id, start_byte, length_bytes, stream_index: json_request(
                            f"{base}/api/v1/downloads/{download_id}/audio-anchor"
                            f"?startByte={start_byte}&lengthBytes={length_bytes}&streamIndex={stream_index}"
                        ),
                        probe_packets=lambda media: ffprobe_artifact_packets("ffprobe", media),
                        decode_ms=lambda media: ffmpeg_decode_ms("ffmpeg", media),
                    )
                )
            result["checkSync"] = checks
            if not all(check["ok"] for check in checks):
                raise RuntimeError("audio anchor sync check failed; see checkSync offsets in the report")

        current = json_request(base + "/api/v1/downloads").get("items", [])
        match = next(item for item in current if item["id"] == prepared["id"])
        result["verifiedProgress"] = match["progress"]
        result["playbackMode"] = match["playbackMode"]
        if float(match["progress"]) >= 1 or match["playbackMode"] != "progressive":
            raise RuntimeError("torrent completed before progressive playback was verified")
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    finally:
        if opener is not None and qb_base and torrent_hash:
            try:
                qb_form(
                    opener,
                    qb_base,
                    "/api/v2/torrents/setDownloadLimit",
                    {"hashes": torrent_hash, "limit": "0"},
                )
            except Exception:
                # Deletion below remains authoritative; resetting first is a
                # safeguard if deletion is temporarily blocked by a lease.
                pass
        if prepared is not None:
            delete_with_retry(base, prepared["id"])


if __name__ == "__main__":
    raise SystemExit(main())
