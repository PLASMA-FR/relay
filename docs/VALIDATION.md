# V1 validation

Validation took place on 2026-09-08 on Ubuntu 24.04 Linux arm64, using Go 1.27.1,
an authenticated local Tailscale client, tmux 3.4 and user systemd. Tests use
temporary identities and synthetic data. No other Tailnet device was modified
or enrolled in Relay trust.

## Automated gates

```sh
go test ./...
go test -race ./...
go vet ./...
GOTOOLCHAIN=go1.25.0 go test ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
shellcheck install.sh uninstall.sh scripts/*.sh
```

The vulnerability scan found no known vulnerabilities after dependency updates.
This is a dependency check, not an independent security audit.

| Area | Coverage |
| --- | --- |
| Identity/protocol | Persistent Ed25519 identity, TLS pinning, bounded frames, malformed input, invalid manifests |
| Filesystem | Traversal/symlink refusal, collisions, Unicode/spaces, empty files, directories, executable bits |
| Recovery | Verified prefix resume, engine/daemon restart, source changes, damaged checksums, lost receipt, publication journal |
| Authorization | Accept/reject, live revocation, exact-offer approval, failed persistence and pending trust cannot authorize |
| Daemon/CLI | Private socket, bounded events/queue, durable jobs, graceful shutdown, JSON, pipelines, binary stdin, pairing mismatch |
| TUI | Keyboard/mouse, checkbox selection, search, modal isolation, offline queue, recent completions, reconnect, ASCII/NO_COLOR |
| Layout | Simulations at 24×8 and 28/48/80/150 columns; resize and recovery from 1×1 |
| Installer | Isolated prefixes, spaces, repeat install, user config/completion preservation, conflicts/backups, uninstall, release archive installation |

Normal test runs skip opt-in large-file, virtual-display and interactive tests.

## Live checks

- Installed from source into `~/.local/bin`; started a standalone daemon, then
  handed it off to user systemd. `relay doctor` and actual Tailnet discovery
  worked. The listener bound the Tailscale address, not a public wildcard.
- Ran the real TUI in tmux with xterm mouse reporting. Exercised keyboard
  navigation, help, history, picker checkboxes and terminal resizing.
- Two real TLS daemons on isolated loopback listeners used fictional desktop
  and laptop identities. The TUI sent a Unicode file, an empty file and a
  directory together; received bytes and executable permissions matched.
- Paused a 512 MiB transfer after more than 100 MiB progressed, resumed it and
  confirmed verified completion. Piped Unicode text reached the other daemon's
  Relay Clipboard byte-for-byte. Binary stdin reached its destination intact.
- Actual X11 and Wayland clipboard protocols passed on virtual display servers;
  see [backend details](CLIPBOARD-TESTING.md).
- Actual 1 GiB and 3 GiB TLS/disk transfers and a simulated slower connection
  passed. See [measurements and caveats](PERFORMANCE.md).

## Reproduce the live fixture

```sh
fixture_dir=$(mktemp -d /tmp/relay-interactive.XXXXXXXX)
RELAY_INTERACTIVE_DIR="$fixture_dir" go test ./internal/daemon \
  -run '^TestInteractiveFixture$' -count=1 -timeout=12m -v
```

From another terminal, read `fixture.json` in that directory. It supplies each
device's environment map, socket, source paths and inbox. Launch `bin/relay
--no-start` with one device's environment. The fixture seeds trust only between
its own generated identities and never contacts the Tailnet. Create its
specified `stop_file` to end it, or wait for its ten-minute limit. Test data is
cleaned up automatically.

## Environment gaps and V1 limits

A second physical Tailscale peer running Relay was unavailable. Cross-device
WAN throughput, DERP failover and physical network-loss recovery were not
manually verified. Controlled interruption tests do not substitute for those.

Individual GNOME Terminal, Kitty, Alacritty, WezTerm, foot, Linux-console and
screen installations were not tested. Neither was a separate headless Debian
VPS or independent SSH session. Headless Linux, tmux, SSH capability fallbacks
and virtual display backends were exercised as described above.

Compression, symlink/ACL/xattr preservation, whole-directory atomicity,
per-device capability policies and automatic abandoned-partial cleanup remain
documented V1 limitations. Passing tests do not constitute a production audit.
