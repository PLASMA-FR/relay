#!/bin/sh
# Remove program files only. Never delete inboxes, configuration, or private state.
set -eu
prefix=${RELAY_INSTALL_PREFIX:-"$HOME/.local"}
remove_service=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --prefix) [ "$#" -ge 2 ] || exit 2; prefix=$2; shift 2 ;;
        --service) remove_service=true; shift ;;
        -h|--help) printf 'Usage: uninstall.sh [--prefix DIR] [--service]\nAll user data, configuration and device identity are preserved.\n'; exit 0 ;;
        *) printf 'Unknown option: %s\n' "$1" >&2; exit 2 ;;
    esac
done
case "$prefix" in /*) ;; *) printf 'Prefix must be absolute.\n' >&2; exit 2 ;; esac
config_base=${XDG_CONFIG_HOME:-"$HOME/.config"}
case "$config_base" in /*) ;; *) printf 'XDG_CONFIG_HOME must be absolute.\n' >&2; exit 2 ;; esac
manifest=$prefix/share/relay/install-manifest
if [ -L "$manifest" ] || [ ! -f "$manifest" ] || [ "$(head -n 1 "$manifest")" != 'Relay installation v1' ]; then
    printf 'No managed Relay installation at %s. Nothing removed.\n' "$prefix"; exit 0
fi
dest=$prefix/bin/relay
if [ -L "$dest" ] || { [ -e "$dest" ] && [ ! -f "$dest" ]; }; then
    printf 'Relay binary is not a regular file; preserved %s.\n' "$dest" >&2; exit 1
fi
if [ -e "$dest" ]; then
    actual=$(sha256sum < "$dest" | cut -d ' ' -f 1)
    expected=$(sed -n '2p' "$manifest")
    if [ "$actual" != "$expected" ]; then printf 'Relay binary changed since installation; preserved %s.\n' "$dest" >&2; exit 1; fi
    if $remove_service; then "$dest" service uninstall; fi
    "$dest" stop >/dev/null 2>&1 || true
    rm -f -- "$dest"
fi
for shell in bash zsh fish; do
    case "$shell" in
        bash) completion=$prefix/share/bash-completion/completions/relay ;;
        zsh) completion=$prefix/share/zsh/site-functions/_relay ;;
        fish) completion=$config_base/fish/completions/relay.fish ;;
    esac
    completion_hash=$prefix/share/relay/completion-$shell.sha256
    if [ ! -L "$completion" ] && [ -f "$completion" ] && [ ! -L "$completion_hash" ] && [ -f "$completion_hash" ]; then
        actual=$(sha256sum < "$completion" | cut -d ' ' -f 1)
        if [ "$actual" = "$(cat "$completion_hash")" ]; then rm -f -- "$completion"; else printf 'Preserved edited completion: %s\n' "$completion"; fi
    fi
    rm -f -- "$completion_hash"
done
rm -f -- "$manifest" "$prefix/share/relay/uninstall.sh"
printf 'Relay program files removed. Configuration, identity, history and received files preserved.\n'
if ! $remove_service; then printf 'If you installed the service, remove it before uninstalling with --service.\n'; fi
