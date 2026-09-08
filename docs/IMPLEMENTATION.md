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
| tailscale | Bounded local CLI status reads and address validation |
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
Pairing observes identity; trust requires a verified full fingerprint.

Snapshots separate unfinished jobs from terminal history. The TUI adds a small
recent-completion list to active work while retaining separate full history.
Payload data never travels through UI event streams.

## Lifecycle and durability

The daemon holds a state-directory lock and owns peer cancellation. Job metadata,
trust activation and incoming approval are persisted before dependent actions
proceed. Failed trust activation cannot authorize a transfer. Revocation remains
effective in memory if storage fails, with an explicit durability notice.

A background daemon outlives its client. User-service installation requests a
graceful local shutdown, waits for its state lock, starts systemd and waits for
IPC readiness. Clients reconnect after daemon restart.

Progress is coalesced, and snapshots and asynchronous UI work are bounded.
Blocking filesystem/network work stays outside the UI event loop. Remote framing
and filesystem transaction boundaries are specified in [PROTOCOL.md](PROTOCOL.md).
