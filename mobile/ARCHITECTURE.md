# Mobile architecture and bridge API

Android app: version **0.3.0**, version code **2**, Kotlin/Compose,
namespace/application ID `io.github.plasmafr.relay`, minSdk 26, compile/targetSdk 35;
AGP 8.9.2, Gradle 8.11.1, Kotlin 2.1.20, Compose BOM 2025.04.01.
Existing Go desktop remains Go 1.25 compatible. GoMobile build tooling is isolated
under `mobile/bind` (Go 1.26+).

## Go bridge: mobile/relaycore, package relaycore

All exported methods use GoMobile-bindable strings/bools and errors.
`NewClient(stateDirectory, inboxDirectory, deviceName string, events Observer)
(*Client,error)`, where Observer has `OnChange(snapshotJSON string)`.

Client methods:

- `Snapshot() string` → JSON snapshot below.
- `Start(bindAddress string) error` → literal Tailscale IP selected by the native
  active-VPN helper; empty closes the listener and pauses connectivity.
- `Stop() error` → stop network activity, cancel discovery/authorization, clear
  automatic pins, retain resumable state.
- `Close() error` → release core; process shutdown/tests.
- `AddDevice(address string) (string,error)` → probe and remember a Peer; live
  discovery grants automatic trust in Tailnet mode unless the peer is blocked.
- `ImportInvite(uri string) (string,error)` → validate the invitation, require the
  observed TLS fingerprint to match, then apply the selected trust policy.
- `SetTrustTailnet(enabled bool) error` → select Tailnet trust (default true) or
  advanced manual fingerprint trust.
- `Trust(peerID, fingerprint string) error` → clear matching block keys; in manual
  mode, also persist the full observed fingerprint as an explicit trust pin.
  Pin and unblock commit together or roll back together on storage failure.
- `Untrust(peerID string) error` → revoke trust and cancel the peer's jobs; in
  Tailnet mode, persist a block against its saved ID, endpoint, and fingerprint.
- `Invite() (string,error)` → own public invitation URI when available.
- `Refresh() error` → probe saved endpoints and bounded directory candidates,
  with at most four workers and per-probe timeouts.
- `Send(peerID, kind, text, pathsJSON string) (string,error)` → model.Result JSON;
  kind file/text/url; paths JSON array of absolute app-private staged paths.
- `Action(action, id string) (string,error)` → accept/reject/pause/resume/cancel.
- `SetAutoAccept(enabled bool) error` → receive approval policy for trusted
  senders, independent of trust mode; default false.

Snapshot JSON: same model.Snapshot fields
(`name,fingerprint,address,trust_mode,peers,transfers,history,notices,receive_directory`),
plus `running` bool, `auto_accept` bool, `clipboard` string, and
`clipboard_transfer_id` string. `trust_mode` is `tailnet` or `manual`;
Peer includes `blocked` independently of `trusted`. Peer IDs are opaque strings.
Observers receive coalesced callbacks outside locks. Snapshot includes only
app-owned destinations and never puts content into history.

## Active VPN and automatic trust

The native helper uses ConnectivityManager's active network for this app,
requires `TRANSPORT_VPN`, and selects a link address in `100.64.0.0/10` or
`fd7a:115c:a1e0::/48`. It does not select an unrelated VPN from `allNetworks`.
Default-network, capability, link-property, and loss callbacks update an
address/generation session. A changed generation forces core Stop followed by
Start, even when a replacement VPN uses the same address.

**Trust assumption:** the user has selected Tailscale as this app's active VPN.
Public Android APIs cannot attest the VPN provider. The active-VPN and address
checks do not implement Tailscale WhoIs or prove membership by themselves.
Relay must be included in Tailscale's VPN rather than excluded by split tunneling.
The Android core uses neither the Tailscale CLI nor Go `net.Interfaces`.

Automatic outgoing trust requires a live TLS Hello probe to a permitted numeric
endpoint during the current running VPN generation. Incoming non-Hello requests
also require the actual remote TCP source address to be permitted. The core
reverse-probes that source's Relay listener, using its known matching endpoint
or port 7331, and requires the listener's TLS key to equal the client's observed
TLS key. Only then does it save peer metadata and enable authorization; failed
persistence denies the request. Hello is discovery-only and never invokes
authorization, so reverse probes cannot recurse.

Automatic pins are transient and separate from durable manual pins. Stop or VPN
loss clears automatic pins and cancels probes and connections. New discovery
must establish them again in the new generation. Blocking survives refresh,
restart, and policy changes; switching to manual mode does not unblock a peer.
Manual Trust still requires the observed fingerprint before clearing a block.
A failed durable block revokes authorization for the current session and exposes
a persistent UI notice explaining that storage must be repaired before restart.

Every queued transfer retains its original TLS fingerprint. Learning a changed
device key pauses old jobs; automatic discovery never changes their destination
key. The user can create a new transfer to the replacement identity.

The existing v1 transfer engine retains TLS pinning, content verification,
resumption, and private storage confinement. There are no arbitrary remote
commands, remote filesystem browsing, or remote daemon administration endpoints.

## Discovery and first contact

The default onboarding is: connect Tailscale, enable availability, and wait for
a current desktop Relay to discover the phone. A desktop with its own Tailnet
device map can probe the phone and send an authenticated peer directory, so a
fresh phone needs no address, invitation, or pairing confirmation in that flow.

The optional v1 capability `peer-directory-v1` uses `PeersFrame` and bounded
`PeerHint{Name,Address,OS}` values. Directories contain names and numeric IP:port
endpoints, never trust assertions or file contents. The shared protocol accepts
at most 256 hints. The mobile core filters candidate addresses to permitted
numeric endpoints, queues at most 128 candidates, and retains at most 128 saved
peers. Hints cannot grant trust or replace pins; each candidate needs a live TLS
probe. Peers without the capability are probed and remain usable for v1 transfers.

A bounded discovery worker probes saved peers and new candidates, and exchanges
directories only with capable, authorized peers. It refreshes periodically and
coalesces notifications for newly learned candidates. Blocked endpoints are
excluded. Stop and VPN loss cancel the worker. Relay does not scan IP ranges or
enumerate Android's Tailnet itself.

If only phones are running and none has a saved peer or a desktop introduction,
one numeric address is needed to seed discovery. That is an optional discovery
fallback, not a fingerprint-pairing requirement in Tailnet mode. Automatic trust
cannot override an older 0.2.0 peer's manual policy: update both endpoints to
0.3.0 for the default no-pairing experience, or meet the older peer's explicit
trust requirements. The wire protocol remains v1.

## Persistence and upgrade

The existing private installation identity, Inbox, queue, and manual pins survive
normal app updates. Mobile state missing `trust_tailnet` migrates to true; an
explicitly persisted false value remains manual. Snapshot `trust_mode` reports
the selected policy. Persisted blocks remain effective in either mode.

Automatic observations save peer metadata but do not populate the durable manual
trust map. Restarting or changing networks therefore requires live discovery
before automatic transfers resume. Disabling automatic trust does not convert
its remembered keys into manual grants. Receive approval remains a separate
persisted preference and defaults to off for automatic acceptance.

Uninstalling or clearing app data resets the private identity and Inbox; export
wanted files first. A new installation is rediscovered automatically in Tailnet
mode or needs new verification in manual mode. Old queued transfers on other
devices remain tied to the previous installation key.

## Public invitation: internal/invite

`type Invitation struct {Version int; Name,Address,Fingerprint string}`.
`New(name,address,fingerprint string) (Invitation,error)`;
`Parse(uri string) (Invitation,error)`; `(Invitation).URL() string`.
Canonical URI:
`relay://pair?v=1&name=desktop&address=100.64.0.5%3A7331&fingerprint=<64hex>`.

Invitations use numeric Tailscale hosts, valid ports, a bounded 128-byte printable
name, and a full lowercase SHA-256 pin. They contain no auth tokens or secrets.
Parsing rejects duplicate query keys, unknown versions, userinfo, ambiguous
paths, and URL fragments. ImportInvite always verifies the live TLS key against
the supplied pin. Tailnet mode applies automatic trust after that check; manual
mode leaves a candidate for explicit fingerprint/provenance confirmation.
Invitations and QR codes are optional, and their legacy URI name does not make
pairing a requirement.

## Kotlin shared contract

`data/RelayRepository.kt` bridges the native core to Android lifecycle, file,
sharing and VPN helpers. `MainActivity.kt` and `ui/` provide the Compose interface.

`RelayApplication.repository: RelayRepository` is process-scoped.
Repository exposes `state: StateFlow<RelayState>`; models mirror snake_case Go
JSON using org.json (no serialization compiler plugin).
`RelayState` contains `name,fingerprint,address,trustMode:String`,
`running,autoAccept:Boolean`, peer/transfer/history lists, clipboard fields,
and notices. Missing `trust_mode` defaults to `tailnet`.
Peer includes `id,name,hostname,address,os,arch,version,fingerprint,error:String`,
`online,relay,trusted,blocked:Boolean`, `protocol:Int`, and `latencyMs:Long`;
missing `blocked` defaults to false.
Transfer includes `id,name,peer,peerId,direction,kind,status,error,destination:String`,
`bytes,total:Long`, `speed,etaSeconds:Double`, `verified:Boolean`, and
`paths:List<String>`.

Suspend methods returning Result<Unit> unless specified:

- `refresh()`, `addDevice(input:String):Result<Peer>` (invitation or address).
- `trust(peer:Peer)`, `untrust(peerId:String)`, `setTrustTailnet(Boolean)`.
- `sendText(peerId,text,kind="text")`, `sendFiles(peerId,uris:List<Uri>)`.
- `action(action,id)`, `setAutoAccept(Boolean)`, `invite():Result<String>`.

`setAvailable(enabled:Boolean)` starts/stops the foreground service from visible
UI. `exportFile(sourcePath:String,destination:Uri):Result<Unit>` is confined to
the app Inbox. `shareFile(sourcePath:String)` grants access through FileProvider.
Incoming text never writes the Android system clipboard automatically.

Core Java bindings use package `relaycore`, class `Relaycore.newClient(...)`,
`relaycore.Client`, and `relaycore.Observer`; the AAR is
`mobile/android/app/libs/relaycore.aar`.

UI: Devices/Transfers/Inbox navigation, prominent availability, Tailnet
discovery status, Block/Unblock controls, contextual send sheet, independent
incoming approvals, native file picker, share-intent preselection, and received
file export/share. Adding an automatically trusted device opens its details
without a fingerprint dialog. Full fingerprint comparison remains in advanced
manual mode, including manual unblocking of a previously blocked candidate.

## Verification

The mobile Go suite exercises the real TLS v1 engine through package-private
loopback fixtures. It covers automatic trust migration, manual-pin separation,
persistent blocks, cross-mode manual unblock and storage-failure rollback,
actual-source and reverse-key verification, fresh-phone directory bootstrap
with a third peer, bounded candidate hints, VPN-loss cancellation, and preservation
of queued fingerprints across identity changes. Existing transfer, approval,
resume, storage-confinement, and lifecycle tests remain in manual mode.

Run `go test -race ./mobile/relaycore` from the repository root. Public mobile
APIs expose no loopback or insecure-network switch. Android unit and
instrumentation checks cover parsing, active-VPN selection, lifecycle, and UI
routing; see [the build guide](README.md#verification) and the current
[validation report](../docs/MOBILE-VALIDATION.md) for commands and results.
Emulator coverage does not substitute for a physical-device Tailscale transfer.
