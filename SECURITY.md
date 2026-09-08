# Security model

Relay is a new Linux application with defensive tests, not an independently
audited security product. This document describes the implemented boundaries
and the limits users should consider when trusting another device.

## Network and identity

Production listeners bind to a local Tailscale address discovered through the
authenticated Tailscale CLI. Relay does not expose a public listener, run a
central transfer server, or require a Tailscale API token. The
`tailscale_only = false` setting accepts explicit numeric loopback listeners for
local testing; it does not enable general LAN/public listening.

Tailscale transport encryption is supplemented by Relay's TLS 1.3 connections.
Both endpoints present Ed25519 certificates and prove possession of their keys.
CA/DNS verification is replaced by pinning the complete SHA-256 fingerprint of
the certificate's public-key information. Self-signed certificates can change
without changing the installation's persistent identity.

The sender checks the expected fingerprint before transmitting an offer. The
receiver independently checks the sender against its current trust registry.
Pairing is directional: both installations must explicitly trust each other.
Before trusting, compare the full fingerprint with `relay status` on the actual
remote device through an independently trusted session. Names and addresses
are discovery hints, not proof of identity. Changed identities are not silently
accepted.

Untrusted peers can request bounded discovery metadata, including display name,
platform, version, and capabilities. They cannot use that operation to send
files or clipboard content. Tailscale routing may use DERP when direct paths
are unavailable; this does not introduce a Relay-operated cloud service.

## What trust allows

V1 trust permits supported file, directory, text, and URL transfers. There are
no per-content-type permissions yet. With the default
`auto_accept_trusted = true`, a trusted sender can place content in the configured
inbox without individual prompts. Disable that setting if each transfer needs
approval. Manual approval is preserved as an explicit decision through recovery;
an interrupted, unaccepted offer must not become accepted merely because the
daemon restarted.

Revoking trust stops the device's active transfers and prevents subsequent
authorized streaming. Received URLs are text: Relay does not open browsers,
execute commands, launch received files, or expose a shell. Executable permission
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

State includes the private identity, trust registry, queued work, transfer
history, the Relay Clipboard, and recovery journals. Pending outbound text and
stdin spools are kept so work can survive restart. Successful transfers clear pending outbound content; failed work that can be
retried may retain its source request. Protocol journals retain metadata and
integrity information rather than redundant clipboard text or completed payload
copies.

Interrupted and abandoned incoming partials intentionally remain on disk, and
protocol journals currently require explicit local retention cleanup. Stop the
related work before removing them. Installer and service uninstallation preserve
user data. Deleting the identity key changes device identity and requires pairing
again on peers; do not delete it merely to troubleshoot a transfer.

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
