#!/bin/sh
# Collect captures before emulator-runner tears the emulator down, even on a
# failed test. Screenshot fixtures contain synthetic device/transfer data.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root/mobile/android"
collect() {
    mkdir -p screenshots
    adb pull /sdcard/Android/data/io.github.plasmafr.relay.debug/files/screenshots/. screenshots/ || true
}
trap collect EXIT
./gradlew --no-daemon connectedDebugAndroidTest

# Exercise the release variant as well. CI uses its disposable development key;
# distributable release APKs are signed separately with the private release key.
"$ANDROID_HOME/build-tools/35.0.0/apksigner" sign \
    --ks "$HOME/.android/debug.keystore" --ks-key-alias androiddebugkey \
    --ks-pass pass:android --key-pass pass:android \
    --out app/build/outputs/apk/release/release-smoke.apk \
    app/build/outputs/apk/release/app-release-unsigned.apk
adb install -r app/build/outputs/apk/release/release-smoke.apk
adb shell am start -W -n io.github.plasmafr.relay/.MainActivity
sleep 3
adb shell uiautomator dump /sdcard/relay-release-window.xml
mkdir -p screenshots
adb pull /sdcard/relay-release-window.xml screenshots/release-window.xml
if ! grep -q 'Your devices.' screenshots/release-window.xml; then
    printf 'Release activity did not render its devices screen.\n' >&2
    exit 1
fi
adb exec-out screencap -p > screenshots/release-app-startup.png
