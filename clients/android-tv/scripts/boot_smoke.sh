#!/usr/bin/env bash
# ATV boot smoke: install the debug APK, start the shell, wait for the app's
# own ready log (onPageFinished), and capture the UI hierarchy. Every stage
# carries an explicit deadline so a slow emulator fails loudly with evidence
# instead of hanging or racing a fixed sleep.
# Runs from the repository root (the CI workflow's working directory).
set -u

APK="clients/android-tv/app/build/outputs/apk/debug/app-debug.apk"
READY_BUDGET=${READY_BUDGET:-600}
DUMP_BUDGET=${DUMP_BUDGET:-180}

adb wait-for-device || exit 1
adb install -r "$APK" || exit 1
adb shell am start -n com.torrenttv.app/.MainActivity || exit 1

deadline=$(( $(date +%s) + READY_BUDGET ))
until adb logcat -d -s TorrentTV 2>/dev/null | grep -q "ready"; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "app never reported ready within ${READY_BUDGET}s" >&2
    adb logcat -d | tail -2000 > torrenttv-logcat.txt
    adb exec-out screencap -p > torrenttv-failure.png
    exit 1
  fi
  sleep 5
done

# The "ready" log fires at page load, before the WebView populates its
# accessibility tree, so a dump taken immediately can be valid XML with an
# empty hierarchy. Wait until the app's own brand actually appears.
deadline=$(( $(date +%s) + DUMP_BUDGET ))
until adb shell uiautomator dump /sdcard/torrenttv.xml >/dev/null 2>&1 \
   && adb pull /sdcard/torrenttv.xml torrenttv-smoke.xml >/dev/null 2>&1 \
   && grep -q -e "TorrentTV" -e "Torrent TV" torrenttv-smoke.xml; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "app hierarchy never exposed the TorrentTV shell within ${DUMP_BUDGET}s" >&2
    adb logcat -d | tail -2000 > torrenttv-logcat.txt
    adb exec-out screencap -p > torrenttv-failure.png
    exit 1
  fi
  sleep 5
done

# D-pad probe: platform-bridge.js mirrors the page's focused control into
# logcat (native bridge), so remote navigation is provable from adb without
# seeing inside the WebView. The first press also primes the probe: the
# page's engine focuses a control in response, the poll reports it, and
# every later press must move that focus.
focus_key() {
  adb logcat -d 2>/dev/null | grep -o 'TVFOCUS [A-Za-z0-9_-]*' | tail -1 | cut -d' ' -f2
}

fail_with_evidence() {
  adb logcat -d | tail -2000 > torrenttv-logcat.txt
  adb exec-out screencap -p > torrenttv-failure.png
  exit 1
}

adb shell input keyevent KEYCODE_DPAD_RIGHT
deadline=$(( $(date +%s) + 60 ))
base=""
while [ -z "$base" ]; do
  base="$(focus_key)"
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "d-pad press never focused a page control" >&2
    fail_with_evidence
  fi
  sleep 2
done

wait_focus_change() {
  from="$1"
  deadline=$(( $(date +%s) + 30 ))
  while :; do
    current="$(focus_key)"
    if [ -n "$current" ] && [ "$current" != "$from" ]; then
      echo "$current"
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "d-pad press never moved focus away from ${from}" >&2
      fail_with_evidence
    fi
    sleep 1
  done
}

# RIGHT primed the focus onto a control; LEFT and RIGHT must walk it back
# and forth (adjacent columns exist on every screen, including Setup).
adb shell input keyevent KEYCODE_DPAD_LEFT
left_target="$(wait_focus_change "$base")"
adb shell input keyevent KEYCODE_DPAD_RIGHT
wait_focus_change "$left_target" >/dev/null
echo "d-pad navigation verified: ${base} -> ${left_target} -> $(focus_key)"
