#!/bin/sh
# Bind the existing Relay engine. GoMobile tool dependencies are isolated from
# the desktop module, so desktop users retain the Go 1.25 minimum.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
if [ -z "$sdk" ] || [ ! -d "$sdk" ]; then
    printf 'Set ANDROID_HOME to an Android SDK with platform 35 and NDK 27.2.12479018.\n' >&2; exit 1
fi
export ANDROID_HOME="$sdk"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-"$sdk/ndk/27.2.12479018"}"
if [ ! -d "$ANDROID_NDK_HOME" ]; then printf 'Install ndk;27.2.12479018 with sdkmanager.\n' >&2; exit 1; fi
tools_dir=${RELAY_MOBILE_TOOLS:-"${XDG_CACHE_HOME:-$HOME/.cache}/relay/mobile-tools"}
mkdir -p "$tools_dir" "$root/mobile/android/app/libs"
cd "$root/mobile/bind"
go mod download
go build -o "$tools_dir/gobind" golang.org/x/mobile/cmd/gobind
if [ -z "${RELAY_GOMOBILE:-}" ]; then
    go build -o "$tools_dir/gomobile" golang.org/x/mobile/cmd/gomobile
    RELAY_GOMOBILE=$tools_dir/gomobile
fi
PATH="$tools_dir:$PATH" "$RELAY_GOMOBILE" bind \
    -target="${RELAY_ANDROID_TARGETS:-android/arm64,android/amd64}" \
    -androidapi 26 -trimpath \
    -ldflags '-s -w -extldflags=-Wl,-z,max-page-size=16384' \
    -o "$root/mobile/android/app/libs/relaycore.aar" \
    github.com/PLASMA-FR/relay/mobile/relaycore
printf 'Shared Relay core: mobile/android/app/libs/relaycore.aar\n'
