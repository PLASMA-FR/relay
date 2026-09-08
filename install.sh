#!/bin/sh
# Relay user-local installer. No sudo, downloaded scripts, or configuration resets.
set -eu
umask 077

fail() { printf 'relay install: %s\n' "$*" >&2; exit 1; }
usage() {
    cat <<'EOF'
Usage: ./install.sh [--user] [--prefix DIR] [--binary PATH] [--service] [--force]

Build from this checkout (Go 1.25+), or install a supplied release binary.
Default prefix: ~/.local. Existing configuration and device identity are preserved.
--service  Install and start a Linux user systemd service.
--force    Back up an existing, unmanaged relay binary before replacing it.
EOF
}

prefix=${RELAY_INSTALL_PREFIX:-"$HOME/.local"}
binary=
enable_service=false
force=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --user) shift ;;
        --prefix|--binary)
            [ "$#" -ge 2 ] || fail "$1 requires a value"
            [ -n "$2" ] || fail "$1 requires a nonempty value"
            if [ "$1" = --prefix ]; then prefix=$2; else binary=$2; fi
            shift 2 ;;
        --service) enable_service=true; shift ;;
        --force) force=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) fail "unknown option: $1 (see --help)" ;;
    esac
done
case "$prefix" in /*) ;; *) fail '--prefix must be an absolute path' ;; esac
config_base=${XDG_CONFIG_HOME:-"$HOME/.config"}
state_base=${XDG_STATE_HOME:-"$HOME/.local/state"}
cache_base=${XDG_CACHE_HOME:-"$HOME/.cache"}
for path in "$config_base" "$state_base" "$cache_base"; do case "$path" in /*) ;; *) fail 'XDG base paths must be absolute' ;; esac; done
case "$(uname -s)" in Linux) ;; *) fail 'This installer currently supports Linux.' ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail 'Supported architectures: amd64 and arm64.' ;; esac
for dependency in install mktemp sha256sum cmp; do command -v "$dependency" >/dev/null 2>&1 || fail "missing required command: $dependency"; done
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/relay-install.XXXXXXXX")
stage=
cleanup() { rm -rf -- "$work"; if [ -n "$stage" ]; then rm -f -- "$stage"; fi; }
trap cleanup EXIT HUP INT TERM

if [ -z "$binary" ]; then
    if [ -f "$script_dir/go.mod" ]; then
        command -v go >/dev/null 2>&1 || fail 'Go 1.25+ is needed to build this checkout. Install Go from https://go.dev/dl/, or use --binary PATH.'
        printf 'Building Relay for linux/%s…\n' "$arch"
        (cd "$script_dir" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-s -w -X github.com/PLASMA-FR/relay/internal/model.Version=0.2.0' -o "$work/relay" ./cmd/relay)
        binary=$work/relay
    elif [ -f "$script_dir/relay" ]; then
        binary=$script_dir/relay
    else
        fail 'No source checkout or release binary found. Use --binary PATH.'
    fi
fi
if [ ! -f "$binary" ] || [ ! -x "$binary" ]; then fail "not an executable binary: $binary"; fi
version=$("$binary" version) || fail 'binary cannot run on this machine'
case "$version" in 'relay '*) ;; *) fail 'binary is not Relay' ;; esac
bin_dir=$prefix/bin
share_dir=$prefix/share/relay
dest=$bin_dir/relay
manifest=$share_dir/install-manifest
if [ -L "$dest" ] || { [ -e "$dest" ] && [ ! -f "$dest" ]; }; then
    fail "$dest is not a regular file; preserve or move it before installing"
fi
if [ -L "$manifest" ] || { [ -e "$manifest" ] && [ ! -f "$manifest" ]; }; then
    fail "$manifest is not a regular file"
fi
mkdir -p -- "$bin_dir" "$share_dir"
if [ -e "$dest" ] || [ -L "$dest" ]; then
    if cmp -s -- "$binary" "$dest"; then
        printf 'Relay binary is already current.\n'
    elif [ -f "$manifest" ] && [ "$(head -n 1 "$manifest")" = 'Relay installation v1' ]; then
        recorded=$(sed -n '2p' "$manifest")
        actual=$(sha256sum < "$dest" | cut -d ' ' -f 1)
        [ "$recorded" = "$actual" ] || $force || fail "existing $dest changed outside this installer; rerun with --force to back it up"
    elif ! $force; then
        fail "$dest already exists and is not managed by this installer. Use --force to preserve a backup and replace it."
    fi
    if $force && ! cmp -s -- "$binary" "$dest"; then
        backup=$(mktemp "$bin_dir/relay.backup.XXXXXXXX")
        cp -p -- "$dest" "$backup"
        printf 'Previous binary preserved at %s\n' "$backup"
    fi
fi
stage=$(mktemp "$bin_dir/.relay-install.XXXXXXXX")
install -m 0755 -- "$binary" "$stage"
mv -f -- "$stage" "$dest"
stage=
hash=$(sha256sum < "$dest" | cut -d ' ' -f 1)
printf 'Relay installation v1\n%s\n%s\n' "$hash" "$version" > "$work/manifest"
install -m 0600 "$work/manifest" "$manifest"

mkdir -p -- "$config_base/relay" "$state_base/relay" "$cache_base/relay"
if [ ! -e "$config_base/relay/config.toml" ] && [ ! -L "$config_base/relay/config.toml" ] && [ -f "$script_dir/config.example.toml" ]; then
    # noclobber handles another process creating the file during installation.
    (set -C; cat "$script_dir/config.example.toml" > "$config_base/relay/config.toml") || fail 'configuration appeared during installation; it was preserved'
fi
for shell in bash zsh fish; do
    case "$shell" in
        bash) completion=$prefix/share/bash-completion/completions/relay ;;
        zsh) completion=$prefix/share/zsh/site-functions/_relay ;;
        fish) completion=$config_base/fish/completions/relay.fish ;;
    esac
    "$dest" completion "$shell" > "$work/completion"
    mkdir -p -- "$(dirname -- "$completion")"
    owned=false
    completion_hash=$share_dir/completion-$shell.sha256
    if [ -L "$completion" ] || { [ -e "$completion" ] && [ ! -f "$completion" ]; } || [ -L "$completion_hash" ] || { [ -e "$completion_hash" ] && [ ! -f "$completion_hash" ]; }; then
        printf 'Preserved existing completion: %s\n' "$completion"
        continue
    fi
    if [ -f "$completion" ] && [ -f "$completion_hash" ]; then
        current=$(sha256sum < "$completion" | cut -d ' ' -f 1)
        if [ "$current" = "$(cat "$completion_hash")" ]; then owned=true; fi
    fi
    if [ -e "$completion" ] && ! cmp -s "$work/completion" "$completion" && ! $owned; then
        printf 'Preserved existing completion: %s\n' "$completion"
    else
        install -m 0644 "$work/completion" "$completion"
        sha256sum < "$completion" | cut -d ' ' -f 1 > "$completion_hash"
    fi
done
if [ -f "$script_dir/uninstall.sh" ]; then install -m 0755 "$script_dir/uninstall.sh" "$share_dir/uninstall.sh"; fi
if ! command -v tailscale >/dev/null 2>&1; then
    printf '\nTailscale is not installed. Install it from https://tailscale.com/download.\n'
fi
if $enable_service; then "$dest" service install; fi
printf '\nInstalled %s → %s\n' "$version" "$dest"
case ":$PATH:" in *":$bin_dir:"*) ;; *) printf '%s\n' "Add to your shell profile: export PATH=\"$bin_dir:\$PATH\"" ;; esac
printf 'Launch: %s\nCheck:  %s doctor\n' "$dest" "$dest"
printf 'Remove: %s/uninstall.sh --prefix "%s"\n' "$share_dir" "$prefix"
