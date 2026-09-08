#!/bin/sh
# Isolated installation lifecycle; never touches the user's actual XDG state.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
binary=${1:-"$root/bin/relay"}
case "$binary" in /*) ;; *) binary="$(pwd)/$binary" ;; esac
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/relay-install-test.XXXXXXXX")
trap 'rm -rf -- "$test_dir"' EXIT HUP INT TERM
export XDG_RUNTIME_DIR="$test_dir/run"
export XDG_CONFIG_HOME="$test_dir/config" XDG_STATE_HOME="$test_dir/state" XDG_CACHE_HOME="$test_dir/cache"
export RELAY_STATE_DIR="$test_dir/state/relay" RELAY_SOCKET="$test_dir/relay.sock"
export RELAY_CONFIG="$XDG_CONFIG_HOME/relay/config.toml"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 0700 "$test_dir" "$XDG_RUNTIME_DIR"
prefix="$test_dir/prefix with spaces\\and-backslash"
expect_failure() {
    if "$@" > "$test_dir/refusal.log" 2>&1; then
        printf 'Expected command to fail: %s\n' "$*" >&2; exit 1
    fi
}
for script in "$root/install.sh" "$root/uninstall.sh"; do
    expect_failure "$script" --unknown
    expect_failure "$script" --prefix
    expect_failure "$script" --prefix ''
    expect_failure "$script" --prefix relative/path
done
expect_failure "$root/install.sh" --prefix "$prefix" --binary
expect_failure "$root/install.sh" --prefix "$prefix" --binary ''
expect_failure "$root/install.sh" --prefix "$prefix" --binary "$test_dir/missing"
printf 'not executable\n' > "$test_dir/not-executable"
expect_failure "$root/install.sh" --prefix "$prefix" --binary "$test_dir/not-executable"
for variable in XDG_CONFIG_HOME XDG_STATE_HOME XDG_CACHE_HOME; do
    expect_failure env "$variable=relative/path" "$root/install.sh" --prefix "$prefix" --binary "$binary"
    test ! -e "$prefix"
done
"$root/install.sh" --user --prefix "$prefix" --binary "$binary"
"$prefix/bin/relay" version
bash_completion=$prefix/share/bash-completion/completions/relay
zsh_completion=$prefix/share/zsh/site-functions/_relay
fish_completion=$XDG_CONFIG_HOME/fish/completions/relay.fish
printf '\n# preserved sentinel\n' >> "$XDG_CONFIG_HOME/relay/config.toml"
printf '\n# personal completion\n' >> "$bash_completion"
cp "$bash_completion" "$test_dir/edited-completion"
printf '\n# personal fish completion\n' >> "$fish_completion"
cp "$fish_completion" "$test_dir/edited-fish-completion"
"$root/install.sh" --prefix "$prefix" --binary "$binary"
cmp "$bash_completion" "$test_dir/edited-completion"
cmp "$fish_completion" "$test_dir/edited-fish-completion"
grep -q 'preserved sentinel' "$XDG_CONFIG_HOME/relay/config.toml"
printf 'personal data\n' > "$RELAY_STATE_DIR/sentinel"
expect_failure env XDG_CONFIG_HOME=relative/path "$root/uninstall.sh" --prefix "$prefix"
test -x "$prefix/bin/relay"
"$root/uninstall.sh" --prefix "$prefix"
test ! -e "$prefix/bin/relay"
cmp "$bash_completion" "$test_dir/edited-completion"
test ! -e "$zsh_completion"
cmp "$fish_completion" "$test_dir/edited-fish-completion"
test -f "$RELAY_STATE_DIR/sentinel"
test -f "$XDG_CONFIG_HOME/relay/config.toml"
"$root/uninstall.sh" --prefix "$prefix"
mkdir -p "$prefix/bin"
printf 'valuable unrelated file\n' > "$prefix/bin/relay"
expect_failure "$root/install.sh" --prefix "$prefix" --binary "$binary"
grep -q 'valuable unrelated file' "$prefix/bin/relay"
"$root/install.sh" --prefix "$prefix" --binary "$binary" --force
test "$(find "$prefix/bin" -name 'relay.backup.*' | wc -l)" -eq 1
for backup in "$prefix/bin"/relay.backup.*; do grep -q 'valuable unrelated file' "$backup"; done

# A changed managed binary must survive both reinstall and uninstall.
printf '\nexternally changed\n' >> "$prefix/bin/relay"
cp "$prefix/bin/relay" "$test_dir/changed-binary"
expect_failure "$root/install.sh" --prefix "$prefix" --binary "$binary"
cmp "$prefix/bin/relay" "$test_dir/changed-binary"
expect_failure "$root/uninstall.sh" --prefix "$prefix"
cmp "$prefix/bin/relay" "$test_dir/changed-binary"
test -f "$prefix/share/relay/install-manifest"
"$root/install.sh" --prefix "$prefix" --binary "$binary" --force
test "$(find "$prefix/bin" -name 'relay.backup.*' | wc -l)" -eq 2
found=false
for backup in "$prefix/bin"/relay.backup.*; do
    if cmp -s "$backup" "$test_dir/changed-binary"; then found=true; fi
done
test "$found" = true

# Preserve linked completions, including dangling links, throughout the lifecycle.
cp "$zsh_completion" "$test_dir/linked-completion"
rm "$zsh_completion" "$fish_completion"
ln -s "$test_dir/linked-completion" "$zsh_completion"
ln -s "$test_dir/missing-completion" "$fish_completion"
"$root/install.sh" --prefix "$prefix" --binary "$binary"
test -L "$zsh_completion"
test -L "$fish_completion"
test ! -e "$test_dir/missing-completion"
"$root/uninstall.sh" --prefix "$prefix"
test -L "$zsh_completion"
test -L "$fish_completion"
test -f "$test_dir/linked-completion"
cmp "$bash_completion" "$test_dir/edited-completion"

# Refuse a destination directory or symlink without moving or adopting it.
mkdir "$prefix/bin/relay"
expect_failure "$root/install.sh" --prefix "$prefix" --binary "$binary" --force
test -d "$prefix/bin/relay"
test "$(find "$prefix/bin/relay" -mindepth 1 | wc -l)" -eq 0
rmdir "$prefix/bin/relay"
ln -s "$binary" "$prefix/bin/relay"
expect_failure "$root/install.sh" --prefix "$prefix" --binary "$binary" --force
test -L "$prefix/bin/relay"
printf 'Installer lifecycle passed: path validation, unusual paths, idempotence, data/completion preservation, modified binaries and conflict backups.\n'
