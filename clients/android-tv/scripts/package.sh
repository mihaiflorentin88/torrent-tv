#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$root"
# Release artifacts ride the repository's VERSION train (same as the Tizen
# WGT); the app's versionName is its own Android-versioning starting point.
version=$(tr -d '[:space:]' < "$root/../../VERSION")
[ -n "$version" ] || { echo "could not read VERSION" >&2; exit 1; }
# The Android Gradle plugin needs a JDK 17+ and an Android SDK. Prefer the
# caller's environment; fall back to the Homebrew locations used on the
# household build machine.
if [ -z "${JAVA_HOME:-}" ]; then
    for candidate in /opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home /opt/homebrew/opt/openjdk/libexec/openjdk.jdk/Contents/Home; do
        if [ -x "$candidate/bin/java" ]; then JAVA_HOME="$candidate"; break; fi
    done
fi
[ -n "${JAVA_HOME:-}" ] && export JAVA_HOME
if [ -z "${ANDROID_HOME:-}" ] && [ -d /opt/homebrew/share/android-commandlinetools ]; then
    export ANDROID_HOME=/opt/homebrew/share/android-commandlinetools
fi
./gradlew --no-daemon :app:syncWebApp :app:assembleRelease
mkdir -p "$root/.build/artifacts"
cp "$root/app/build/outputs/apk/release/app-release.apk" "$root/.build/artifacts/torrent-tv-$version-android-tv.apk"
(cd "$root/.build/artifacts" && sha256sum "torrent-tv-$version-android-tv.apk" > "torrent-tv-$version-android-tv.apk.sha256")
echo "$root/.build/artifacts/torrent-tv-$version-android-tv.apk"
