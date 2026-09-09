# Architecture and local API

Relay is one Go binary with terminal, CLI and independent daemon entry points.
Clients communicate through a private Unix socket and never open peer connections.

```text
TUI / CLI — private Unix socket — daemon — TLS 1.3 over Tailscale — peer
```

| Package | Responsibility |
| --- | --- |
| cmd/relay | CLI, pipeline input, daemon bootstrap and completion waiting |
| config | Validated TOML and XDG paths |
| ipc, model | Local client and JSON data model |
| daemon | Authorization, discovery, persistent jobs/history, bounded events |
| tailscale | Bounded local CLI status reads, local tailscaled WhoIs and exact node/address validation |
| identity | Persistent Ed25519 identity and TLS certificate generation |
| protocol, transfer | Binary frames, streaming, verified resume and publication |
| tui | tview/tcell widgets, responsive layouts and asynchronous file browser |
| clipboard | Optional Wayland/X11 tools and private internal clipboard |
| service, doctor | Linux service lifecycle and diagnostics |

## Private IPC v1

HTTP travels only through a mode-0600 Unix socket in a private user-owned directory.
This API has no TCP listener.

| Request | Input | Response |
| --- | --- | --- |
| GET /v1/status | — | model.Snapshot |
| GET /v1/events | — | Initial snapshot, then changed snapshots as NDJSON |
| POST /v1/send | model.SendRequest | model.Result with queued transfer ID |
| POST /v1/action | model.Action | model.Result |

Actions: refresh, accept, reject, pause, resume, cancel, pair, trust, untrust,
clipboard-copy, clipboard-show and graceful local shutdown. Errors return a
non-success HTTP status and an error string. Event streams use blank keepalives.
With default automatic Tailnet trust, `pair` refreshes device observations,
`untrust` persistently blocks a device, and `trust` unblocks it without a
fingerprint. In manual mode, `trust` requires the complete verified fingerprint
and clears any block on that device only when the pin and unblock are durable.

Snapshots separate unfinished jobs from terminal history. The TUI adds a small
recent-completion list to active work while retaining separate full history.
Snapshots include `trust_mode` (`tailnet` or `manual`) and each peer's `blocked`
state as well as effective `trusted` state. Payload data never travels through
UI event streams.

## Automatic discovery and authorization

Desktop defaults to `network.trust_tailnet = true`. Discovery maintains an
expiring map of currently visible Tailscale nodes from the local CLI. Probing a
Relay listener obtains its TLS key and Hello metadata; local tailscaled WhoIs
must confirm the discovered stable node and exact address before an automatic
pin is published. Incoming non-Hello requests use the actual remote TCP endpoint
for the same WhoIs/current-membership checks. Blocks, node removal, unavailable
or stale discovery, and expired network identity deny automatic authorization.

The policy applies to all Tailscale-visible devices allowed by network rules,
including shared devices, rather than only nodes belonging to the same person.
Application keys are still pinned per connection. Auto-observed metadata is
saved, but effective automatic pins are kept separate from durable manual trust
and must be established again after restart. A key change affects new sends;
existing jobs retain the fingerprint captured when queued.

After a capable peer is probed, the daemon exchanges `peer-directory-v1` hints.
The bounded directory includes its own endpoint and current unblocked Tailnet
members. Incoming hints cannot add desktop membership. A phone verifies received
candidates independently; the exchange lets it discover a desktop automatically.
Without an updated desktop to announce devices, a phone needs one known address
to seed discovery. The peer protocol offers no remote filesystem listing or
command execution.

Mobile runs the same transfer engine through `mobile/relaycore`. Its automatic
policy requires the active VPN used by Relay and a Tailnet address. Incoming
connections reverse-probe the source's Relay listener for the same TLS key,
persist the resulting peer, and recheck the availability generation. Android
cannot cryptographically identify a VPN provider through its public API; the
user-selected VPN is assumed to be Tailscale. Stop or loss of that VPN disables
authorization and discovery. The [security model](../SECURITY.md) describes this
platform distinction.

Set `network.trust_tailnet = false` for desktop manual pins; Android exposes an
advanced manual-trust setting. Blocks apply in either mode. Receive approval is
independent of device trust: `receive.auto_accept_trusted = false` requires a
local Accept / Reject decision for each incoming offer.

## Lifecycle and durability

The daemon holds a state-directory lock and owns peer cancellation. Job metadata,
automatic peer observations, trust activation, unblocks and incoming approval
are persisted before dependent actions proceed. Pending manual pins and block
removals are withheld from unrelated state saves as well as authorization gates.
Failed trust activation cannot authorize a transfer. Revocation remains
effective in memory if storage fails, with an explicit durability notice.

A background daemon outlives its client. User-service installation requests a
graceful local shutdown, waits for its state lock, starts systemd and waits for
IPC readiness. Clients reconnect after daemon restart.

Progress is coalesced, and snapshots and asynchronous UI work are bounded.
Blocking filesystem/network work stays outside the UI event loop. Remote framing
and filesystem transaction boundaries are specified in [PROTOCOL.md](PROTOCOL.md).
