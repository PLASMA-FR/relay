#!/bin/sh
set -eu
umask 022
version=${1:-0.3.0}
case "$version" in *[!a-zA-Z0-9._-]*|'') printf 'Invalid release version.\n' >&2; exit 2 ;; esac
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mkdir -p dist
work=$(mktemp -d "$root/dist/.relay-release.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT HUP INT TERM
for arch in amd64 arm64; do
    package=relay_${version}_linux_${arch}
    mkdir "$work/$package"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch "${GO:-go}" build -trimpath -buildvcs=false -ldflags "-s -w -X github.com/PLASMA-FR/relay/internal/model.Version=$version" -o "$work/$package/relay" ./cmd/relay
    install -m 0644 README.md LICENSE SECURITY.md config.example.toml "$work/$package/"
    install -m 0755 install.sh uninstall.sh "$work/$package/"
    cp -R docs "$work/$package/"
    if [ -f mobile/README.md ]; then
        mkdir "$work/$package/mobile"
        install -m 0644 mobile/README.md "$work/$package/mobile/README.md"
    fi
    find "$work/$package/docs" -type d -exec chmod 0755 {} +
    find "$work/$package/docs" -type f -exec chmod 0644 {} +
    tar --sort=name --mtime='UTC 2026-01-01' --owner=0 --group=0 --numeric-owner -C "$work" -cf "$work/package.tar" "$package"
    gzip -n -c "$work/package.tar" > "$work/$package.tar.gz"
done
(cd "$work" && sha256sum "relay_${version}_linux_amd64.tar.gz" "relay_${version}_linux_arm64.tar.gz" > checksums.txt)
mv "$work"/*.tar.gz "$work/checksums.txt" dist/
printf 'Release archives and checksums are in dist/.\n'
