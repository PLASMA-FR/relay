# Mobile architecture and bridge API

Android app: Kotlin/Compose, namespace/application ID `io.github.plasmafr.relay`,
minSdk 26, compile/targetSdk 35; AGP 8.9.2, Gradle 8.11.1, Kotlin 2.1.20,
Compose BOM 2025.04.01. Existing Go desktop remains Go 1.25 compatible.
GoMobile build tooling is isolated under mobile/bind (Go 1.26+).

## Go bridge: mobile/relaycore, package relaycore

All exported methods use GoMobile-bindable strings/bools and errors.
`NewClient(stateDirectory, inboxDirectory, deviceName string, events Observer)
(*Client,error)`, where Observer has `OnChange(snapshotJSON string)`.

Client methods:

- `Snapshot() string` → JSON snapshot below.
- `Start(bindAddress string) error` → literal Tailscale IP; empty means no VPN,
  closes listener and pauses connectivity. Native platform chooses address.
- `Stop() error` → stop availability/network activity; resumable state retained.
- `Close() error` → release core; process shutdown/tests.
- `AddDevice(address string) (string,error)` → probe and remember candidate Peer.
- `ImportInvite(uri string) (string,error)` → validate invitation, probe matching
  fingerprint, remember untrusted candidate. Never grants trust automatically.
- `Trust(peerID, fingerprint string) error`, `Untrust(peerID string) error`.
- `Invite() (string,error)` → own public invitation URI when network available.
- `Refresh() error` → probe saved peers with bounded concurrency/timeouts.
- `Send(peerID, kind, text, pathsJSON string) (string,error)` → model.Result JSON;
  kind file/text/url; paths JSON array of absolute app-private staged paths.
- `Action(action, id string) (string,error)` → accept/reject/pause/resume/cancel.
- `SetAutoAccept(enabled bool) error` → trusted-only policy (default false).

Snapshot JSON: same model.Snapshot fields (name,fingerprint,address,peers,
transfers,history,notices,receive_directory), plus `running` bool,
`auto_accept` bool, `clipboard` string, `clipboard_transfer_id` string.
Snapshot observers must be coalesced; callbacks outside locks. model.Peer and
model.Transfer fields remain as in internal/model. Peer IDs are opaque strings.
Snapshot includes only app-owned destinations; never put content into history.
Durable queue/history/trust; same v1 transfer engine, no new crypto or remote IPC.
No Tailscale CLI invocation or Go net.Interfaces on Android.

## Public invitation: internal/invite

`type Invitation struct {Version int; Name,Address,Fingerprint string}`.
`New(name,address,fingerprint string) (Invitation,error)`;
`Parse(uri string) (Invitation,error)`; `(Invitation).URL() string`.
Canonical URI: `relay://pair?v=1&name=desktop&address=100.64.0.5%3A7331&fingerprint=<64hex>`.
Numeric Tailscale host only, explicit valid port, bounded 128-byte printable name,
full lowercase SHA256 pin, no extra auth tokens or secrets. Reject duplicate
query keys/unknown version, userinfo, ambiguous paths and URL fragments.
Mobile ImportInvite validates observed TLS fingerprint against supplied pin,
then UI requires explicit confirmation of provenance before Trust.

## Kotlin shared contract

`data/RelayRepository.kt` bridges the native core to Android lifecycle, file,
sharing and VPN helpers. `MainActivity.kt` and `ui/` provide the Compose interface.

`RelayApplication.repository: RelayRepository` is process-scoped.
Repository exposes `state: StateFlow<RelayState>`; models mirror snake_case Go
JSON using org.json (no serialization compiler plugin).
`data class RelayState(name,fingerprint,address:String, running:Boolean,
autoAccept:Boolean, peers:List<Peer>, transfers:List<Transfer>, history:List<Transfer>,
clipboard:String, clipboardTransferId:String, notices:List<String>)`.
Peer: id,name,hostname,address,os,arch,version,fingerprint,error:String,
online,relay,trusted:Boolean, protocol:Int, latencyMs:Long.
Transfer: id,name,peer,peerId,direction,kind,status,error,destination:String,
bytes,total:Long,speed,etaSeconds:Double,verified:Boolean,paths:List<String>.

Suspend methods returning Result<Unit> unless specified: refresh(),
addDevice(input:String):Result<Peer> (invitation or address),
trust(peer:Peer), untrust(peerId:String),
sendText(peerId,text,kind="text"), sendFiles(peerId,uris:List<Uri>),
action(action,id), setAutoAccept(Boolean), invite():Result<String>.
`setAvailable(enabled:Boolean)` starts/stops foreground service from visible UI.
`exportFile(sourcePath:String,destination:Uri):Result<Unit>` confined app inbox.
`shareFile(sourcePath:String)` via safe FileProvider; UI can use helper platform.
Incoming text never writes Android system clipboard automatically.

Core Java bindings generated package `relaycore`, class `Relaycore.newClient(...)`,
`relaycore.Client`, `relaycore.Observer`; AAR mobile/android/app/libs/relaycore.aar.

UI: restrained dark ink/off-white, mint accent, clear Devices/Transfers/Inbox
bottom navigation, prominent availability, peer tiles, contextual send sheet,
incoming approvals, full-fingerprint comparison, copy/share own pairing info,
native file picker, share-intent preselection, received-file export/share.
No arbitrary remote commands or remote daemon administration.
