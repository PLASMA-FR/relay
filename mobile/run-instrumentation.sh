#!/bin/sh
# Collect captures before emulator-runner tears the emulator down, even on a
# failed test. Screenshot fixtures contain synthetic device/transfer data.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root/mobile/android"
smoke_dir=
collect() {
    mkdir -p screenshots
    adb pull /sdcard/Pictures/RelayTests/. screenshots/ || true
    if [ -n "$smoke_dir" ]; then rm -rf -- "$smoke_dir"; fi
}
trap collect EXIT
./gradlew --no-daemon connectedDebugAndroidTest

# Exercise the release variant as well. CI uses its disposable development key;
# distributable release APKs are signed separately with the private release key.
smoke_dir=$(mktemp -d "${TMPDIR:-/tmp}/relay-android-smoke.XXXXXXXX")
keytool -genkeypair -keystore "$smoke_dir/smoke.p12" -storetype PKCS12 \
    -storepass android -keypass android -alias relay-smoke -keyalg RSA \
    -keysize 2048 -validity 2 -dname 'CN=Relay CI smoke test' -noprompt
"$ANDROID_HOME/build-tools/35.0.0/apksigner" sign \
    --ks "$smoke_dir/smoke.p12" --ks-key-alias relay-smoke \
    --ks-pass pass:android --key-pass pass:android \
    --out "$smoke_dir/relay-smoke.apk" \
    app/build/outputs/apk/release/app-release-unsigned.apk
adb install -r "$smoke_dir/relay-smoke.apk"
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
