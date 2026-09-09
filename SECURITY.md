# Security model

Relay is a new Linux application with defensive tests, not an independently
audited security product. This document describes the implemented boundaries
and the limits users should consider when trusting another device.

## Network and identity

Relay 0.3 defaults to `network.trust_tailnet = true`: every Tailscale-visible
device permitted to reach Relay by the network's access rules can connect
without pairing. This includes shared devices and devices owned by other people;
the policy does not require a matching human owner. Relay must be running on
the remote device, and local blocks still apply.

Production desktop listeners bind to a local Tailscale address discovered through
the authenticated Tailscale CLI. Incoming authorization checks the actual TCP
remote endpoint with the local tailscaled WhoIs API, verifies its exact assigned
address and stable node identity against the current device map, and rejects
expired or explicitly unauthorized identities. A Tailscale-shaped IP or a
claimed name is insufficient. Outgoing discovery probes also verify the expected
Tailscale node before publishing its observed Relay key. Lost or stale membership,
removed devices, and discovery failure disable automatic authorization.

Relay adds TLS 1.3 to the Tailscale transport. Both endpoints present Ed25519
certificates and prove possession of their keys. CA/DNS verification is replaced
by pinning the complete SHA-256 fingerprint of the certificate's public-key
information. The sender verifies its expected application fingerprint before
transmitting an offer. New automatic keys become effective only after their
peer metadata is saved; automatic pins remain separate from durable manual
trust and must be re-established after restart.

A current authorized device can rotate its Relay key without a pairing prompt.
Existing queued jobs retain their original expected key and are never silently
redirected to the replacement. New sends use the newly verified key.

`tailscale_only = false` permits explicit numeric loopback listeners for local
testing; it does not enable LAN/public listening or automatic loopback trust.
Relay uses the local Tailscale daemon rather than a control-plane API token and
has no central transfer server. Tailscale may use encrypted DERP transport when
a direct route is unavailable.

### Android network identity

Android's public VPN APIs do not cryptographically identify the VPN provider.
Relay assumes the VPN selected by the user is Tailscale. It checks that the
active network used by Relay is a VPN with a matching Tailnet address, rather
than accepting an unrelated VPN found elsewhere on the device. Losing that
network or stopping availability disables automatic authorization and discovery.

For incoming automatic authorization, the mobile core reverse-probes the Relay
listener at the actual source IP and requires the observed key to equal the
incoming TLS key. It saves the peer before authorizing it and rechecks its VPN
availability generation. Outgoing candidates must be numeric Tailnet endpoints
and successfully prove their TLS keys over that active VPN. This is an Android
network assumption plus a Relay key proof, not desktop WhoIs verification.

### Manual trust and device discovery

Set `network.trust_tailnet = false` on desktop, or choose manual trust in Android's
advanced settings, to require explicit application pins. Compare the full
fingerprint on the actual remote device through an independently trusted session
before granting manual trust. Each endpoint authorizes its peer separately.
Automatic observations never become durable manual grants.

Before authorization, peers may request only bounded Hello metadata: display
name, platform, version, capabilities and the observed TLS identity. The optional
`peer-directory-v1` exchange requires authorization and carries at most 256
numeric device endpoint hints. Desktop responses are confined to current
unblocked Tailnet members and its own endpoint; incoming hints cannot add desktop
membership. Mobile treats hints as candidates to verify, never trust assertions.
This introduces a fresh phone when a desktop discovers it. If no updated desktop
can announce devices, a phone needs one known address to begin discovery.

The directory exposes neither filesystem listings nor remote commands. It is
an optional extension: protocol-v1 file and text framing remains compatible,
but older endpoints need upgrading for the complete automatic trust flow.

## What trust allows

V1 trust permits supported file, directory, text, and URL transfers. There are
no per-content-type permissions yet. With the default
`auto_accept_trusted = true`, a trusted sender can place content in the configured
inbox without individual prompts. Disable that setting if each transfer needs
approval. Manual approval is preserved as an explicit decision through recovery;
an interrupted, unaccepted offer must not become accepted merely because the
daemon restarted.

In automatic mode, `relay untrust DEVICE` blocks the device and stops its active
transfers. Blocks survive discovery refresh, restart, and a switch to manual
mode. `relay trust DEVICE` unblocks in automatic mode; manual mode requires the
verified fingerprint. A new trust grant or unblock is withheld until its state
is durably saved. A failed block save still revokes access in memory and reports
the durability failure so it can be retried.

Received URLs are text: Relay does not open browsers, execute commands, launch
received files, or expose a shell. Executable permission
bits may be preserved on transferred files. Incoming text updates only the
private Relay Clipboard, not the desktop clipboard or terminal clipboard.

Trust does not make content harmless. A trusted device can send unwanted files,
large data within configured limits, or misleading text. Approval and inbox
policies remain under the receiving user's control.

## Filesystem and transfer integrity

The binary protocol bounds frames before allocating their payloads and validates
the complete manifest before receiving files. It rejects traversal, absolute
paths, malformed names, duplicate paths, unsupported modes, oversized offers,
and missing parent directories. V1 rejects all symlinks and special files.

Receive operations use Go `os.Root` confinement and explicit symlink checks.
Partial payloads use generated staging names inside a private directory on the
receive filesystem. They are not published under their final names until
SHA-256 verification succeeds. Default publication does not overwrite an
existing destination. Explicit `replace` supports regular files only; it does
not replace directories or symlink targets.

Files and journals are flushed around publication. Recovery checks saved hashes
before treating an interrupted publication as complete. Each file is atomic;
the entire directory is not a transaction. A crash may leave already verified
files visible alongside recoverable partials.

Resume compares the SHA-256 of the existing partial prefix against the source
before sending the remaining bytes. A mismatch is refused. A final checksum
failure never publishes the corrupted file. Persistent receipts and offer
identity checks make identical retries idempotent; reusing an ID for changed
content is rejected. TLS protects transport replay, while transfer IDs handle
application retries rather than granting authorization.

See [PROTOCOL.md](docs/PROTOCOL.md) for framing, limits, publication ordering,
and the exact recovery procedure.

## Resource and local boundaries

Concurrency, transfer manifests, IPC bodies, event subscribers, and queue
metadata are bounded. The default receive limit is 1 TiB **per offer**, not a
quota. Disk exhaustion remains possible: multiple trusted transfers can consume
available space, and OS write failures leave recoverable partials. Stdin spooling
also consumes local disk. Compression is not implemented, so Relay does not
extract externally supplied archives or decompress payloads automatically.

Private keys live in `identity.pem` with mode `0600` inside a private state
directory, separate from editable configuration. The Unix IPC socket is owned
by the user, mode `0600`, and placed in a private owned directory. Relay refuses
to replace unrelated filesystem objects at its socket path.

Processes running as the same Unix user, and root, can access the daemon's files,
keys, and IPC. Relay creates no additional security boundary within that user.
A compromised endpoint can read its plaintext inbox and clipboard. Payloads,
partial files, and private state are not encrypted at rest; use filesystem or
disk encryption when needed.

File paths, peer labels, clipboard text, and errors are escaped for terminal
display. Relay does not emit clipboard-control sequences automatically. The
clipboard CLI intentionally returns raw text to non-terminal stdout so pipelines
preserve content; the receiving program is responsible for its own handling.

## Stored data and cleanup

State includes the private identity, device blocks, manual trust registry,
observed peer metadata, queued work, transfer
history, the Relay Clipboard, and recovery journals. Pending outbound text and
stdin spools are kept so work can survive restart. Successful transfers clear
pending outbound content; failed work that can be retried may retain its source
request. Protocol journals retain metadata and
integrity information rather than redundant clipboard text or completed payload
copies.

Interrupted and abandoned incoming partials intentionally remain on disk, and
protocol journals currently require explicit local retention cleanup. Stop the
related work before removing them. Installer and service uninstallation preserve
user data. Deleting the identity key changes device identity. Automatic mode
relearns a current authorized device; manual peers require a new verified pin.
Existing queued jobs keep their original key.

Do not publish state directories, received files, logs, clipboard contents,
private keys, or Tailscale credentials in issue reports. The repository contains
example configuration and synthetic screenshot fixtures, not operational state.

## Reporting a security issue

Use the repository's [GitHub security reporting interface](https://github.com/PLASMA-FR/relay/security/advisories/new)
when private vulnerability reporting is available. Otherwise, open a minimal
issue requesting a private contact channel without publishing sensitive details.
Do not include live credentials, private files, or another person's device data.

Useful reports include the affected version, platform, relevant configuration
with secrets removed, the affected trust boundary, and the observed impact.
The [validation report](docs/VALIDATION.md) records tested environments and
remaining gaps; passing automated tests is not a claim of independent audit.
