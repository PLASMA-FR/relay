# Relay protocol v1

Relay uses direct TCP over the existing Tailscale network. The daemon owns
network listeners, Tailscale discovery, connection limits, trust policy and local
IPC. The transfer engine owns the authenticated transfer protocol. There is no
Relay cloud service. Tailscale itself may transport an encrypted connection
through its DERP infrastructure when a direct path is unavailable.

## Identity and authentication

Each installation persists a PKCS#8 Ed25519 private key in its private state
directory (`identity.pem`, mode `0600`). Private keys never belong in editable
configuration. The installation derives a self-signed TLS certificate from this
key at startup; changing certificates does not change identity.

Connections require TLS 1.3 and a presented Ed25519 certificate on both sides.
The identity fingerprint is the full, lowercase hexadecimal SHA-256 digest of
the certificate's DER SubjectPublicKeyInfo. Normal CA/DNS certificate validation
is intentionally replaced by explicit application pinning. TLS proves possession
of the corresponding private key; Relay pins the expected server fingerprint
**before** transmitting an offer, and the receiver checks the client fingerprint
against its current trust registry. Trust is directional and must be established
on both installations. A proposed trust addition is excluded from authorization
until its state is durably committed. Discovery alone grants no transfer permissions.

The only operation available before application trust is a bounded Hello
request/response. Peer version and capabilities are advisory, authenticated to
its observed fingerprint, and cannot authorize a transfer. Fingerprint verification
must use an independently checked full fingerprint; a hostname is not an identity.
TLS protects metadata and content, and supplies replay protection on the wire.
Transfer IDs provide application retry idempotency, not authentication.

## Framing

Every message has this eight-byte header, followed by exactly the declared
number of bytes:

| Bytes | Meaning |
| --- | --- |
| 0–1 | ASCII `RL` |
| 2 | Protocol version, currently `1` |
| 3 | Message type |
| 4–7 | Unsigned big-endian payload length |

| Type | Value | Payload |
| --- | --- | --- |
| Hello | 1 | JSON hello metadata |
| Offer | 2 | JSON transfer manifest or bounded text |
| Decision | 3 | JSON `{ "accepted": true }` |
| Resume | 4 | JSON byte offset and SHA-256 prefix; optional skip |
| Data | 5 | Raw bytes, maximum 256 KiB |
| FileEnd | 6 | JSON complete-file SHA-256 |
| FileAck | 7 | JSON verified complete-file SHA-256 |
| Complete | 8 | JSON verified receipt and destination paths |
| Error | 9 | Bounded UTF-8 diagnostic |
| Heartbeat | 10 | Empty payload during lengthy prefix hashing |

Metadata frames are limited to 2 MiB **before allocation**. Unknown versions,
message types and oversized declarations are rejected. Data is never JSON or
Base64 encoded. The engine accepts only the message type expected by its state.
A heartbeat cannot carry content or replace an acknowledgement.

## Transfer sequence

1. Establish mutual TLS; sender verifies its pinned peer fingerprint.
2. Sender transmits an Offer with a random 128-bit transfer ID.
3. Receiver checks current trust, validates the complete manifest and requests
   its local acceptance policy. An explicit rejection ends the connection.
4. Receiver sends Decision. A previously completed, identical transfer may
   immediately return its persisted Complete receipt.
5. For each regular file in manifest order, receiver sends Resume with its
   existing partial length and SHA-256 of that exact prefix. Sender independently
   hashes the same source prefix and refuses a mismatch.
6. Sender streams bounded Data frames for the remaining bytes, then FileEnd.
   Receiver rejects empty chunks, excess bytes and premature end markers.
7. Receiver compares the full SHA-256, flushes the file, publishes it atomically,
   journals completion and returns FileAck with the same digest.
8. Receiver sends Complete only after every non-skipped file is verified.

Directories have manifest entries but no payload frames. Empty files still
exchange the hash of the empty input. Text and URL offers contain up to 1 MiB
of text in bounded metadata and return a verified receipt; they do not launch
applications, execute commands or write the system clipboard automatically.

The idle limit during transfer is two minutes. Prefix hashing uses bounded disk
reads, checks cancellation and emits an empty heartbeat at least once a second
when reads are making progress. Human offer acceptance can take longer and
remains cancellable. Normal progress notifications are coalesced to roughly
10 Hz, plus state transitions; the TUI never owns these connections.

## Filesystem confinement and recovery

Offers contain at most 10,000 entries. Declared total sizes must equal the sum
of regular-file sizes and fit the configured receive limit. Paths must be
relative, normalized UTF-8, with no traversal, absolute paths, empty components,
backslashes, controls, oversized components, duplicates or reserved `.relay-`
components. A child entry must follow an explicitly declared directory parent.
Only regular files and directories are supported. Symlinks, sockets, FIFOs,
devices, setuid/setgid and other special file modes are rejected.

Destination operations use Go `os.Root` confinement and explicitly reject
symlink components. Staging files have daemon-chosen numeric names under a
private `.relay-part-<identity-and-transfer-hash>` directory on the destination
filesystem. Peer paths are never used as staging filenames.

The engine hashes while streaming and keeps buffers bounded. On interruption,
it closes and flushes the partial file. A new connection can resume even after
an engine or daemon restart by comparing the disk prefix against the current
source. A corrupted prefix is not trusted; the user must start a fresh transfer.
A final checksum failure resets the partial to zero and never publishes it.

Each file's verified checksum is durably journaled before publication. Default
publication uses a hard link, an atomic no-overwrite operation on the destination
filesystem. The file and destination directory are synced before completion is
journaled. A crash between publication and completion journaling is repaired by
matching the published file against the saved verified checksum. Lost completion
acknowledgements return the same receipt rather than duplicating files. Reusing a
transfer ID with any changed offer metadata is rejected.

`rename` chooses an available destination name, `skip` preserves an existing
item, and `cancel` refuses collisions. Explicit `replace` permits atomic rename
of regular files only; directories and symlink targets are refused. Unexpected
concurrent destination changes cause a safe error. A directory's files are
individually atomic; the entire directory is not one atomic transaction.

Normal permission bits and modification times are preserved where the filesystem
supports them. ACLs, xattrs, sparse layout, ownership and symlinks are not copied.
The sender checks file size and mtime before and after streaming; users should
send a stable source tree rather than treating Relay as a filesystem snapshot.

Persistent journals retain metadata and checksums, not file contents or clipboard
text. Text-offer digests allow idempotent replay without keeping extra clipboard
copies in transfer history. Incomplete payloads intentionally remain recoverable;
metadata and abandoned partials currently require explicit local cleanup after
the corresponding job is no longer needed.

## Limits and extension points

V1 uses one TLS connection per transfer, sequential files, 256 KiB payload frames,
and full-prefix validation on resume. Separate transfers can run concurrently,
with daemon-configured admission limits. Compression, per-file parallel streams,
chunk hash trees, resumable source snapshots and symlink transfer are deferred.
These are capability/version changes, not reasons to weaken integrity or path
confinement. Receive limits cap an individual offer; available disk space remains
an OS-enforced resource, and write failures leave recoverable partials.

This is a new implementation with defensive tests, not an independently audited
security product. Local processes running as the same Unix user share access to
its files and keys; Relay does not create a new security boundary within that user.
