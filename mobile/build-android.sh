#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
case "${1:-debug}" in
    debug) task=assembleDebug ;;
    release) task=assembleRelease
        if [ -z "${RELAY_ANDROID_KEYSTORE:-}" ]; then
            printf 'Set RELAY_ANDROID_KEYSTORE and signing password variables for a signed release.\n' >&2; exit 1
        fi ;;
    *) printf 'Usage: mobile/build-android.sh [debug|release]\n' >&2; exit 2 ;;
esac
"$root/mobile/build-core.sh"
cd "$root/mobile/android"
./gradlew --no-daemon testDebugUnitTest lintDebug "$task"
