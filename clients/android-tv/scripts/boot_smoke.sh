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

deadline=$(( $(date +%s) + DUMP_BUDGET ))
until adb shell uiautomator dump /sdcard/torrenttv.xml >/dev/null 2>&1 \
   && adb pull /sdcard/torrenttv.xml torrenttv-smoke.xml >/dev/null 2>&1; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "uiautomator dump never succeeded within ${DUMP_BUDGET}s" >&2
    adb logcat -d | tail -2000 > torrenttv-logcat.txt
    adb exec-out screencap -p > torrenttv-failure.png
    exit 1
  fi
  sleep 5
done

if ! grep -q "TorrentTV" torrenttv-smoke.xml; then
  adb exec-out screencap -p > torrenttv-failure.png
  echo "UI hierarchy lacks the TorrentTV shell" >&2
  exit 1
fi
