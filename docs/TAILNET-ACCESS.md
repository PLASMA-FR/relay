# Automatic Tailnet access in Relay 0.3

Relay devices connect without manual pairing by default. On desktop,
`network.trust_tailnet = true` is the default even when the setting is absent
from an existing configuration. Android upgrades also select Tailnet trust when
an older saved state has no policy setting. Existing identity, inbox and history
are preserved.

## Normal workflow

1. Install Relay 0.3 on the devices and connect them to Tailscale.
2. Run the desktop daemon and enable availability on Android.
3. Select a discovered device and send. No QR code, fingerprint comparison or
   per-device trust command is required.

Desktop Relay obtains the current Tailscale peer map and introduces those
addresses to the phone. Each endpoint is probed independently before becoming
authorized. All currently visible, permitted Relay devices can participate,
including shared devices when Tailscale policy allows them. Devices without a
running Relay service cannot receive Relay transfers.

If only phones are running and none knows another endpoint, add one numeric
Tailscale address as a discovery seed. This is address discovery, not pairing.
Android cannot enumerate Tailscale's private peer map; Relay does not scan the
entire address range or add another cloud account.

## Authorization and privacy

Desktop authorizes the socket's actual remote endpoint through local tailscaled
WhoIs, requiring an exact direct node address and current membership. Missing,
expired, disabled or mismatched identities fail closed. Subnet routes and
forwarded headers do not grant access.

Android uses the active VPN for the Relay app, checks its Tailscale addresses,
and verifies that the incoming connection's TLS key matches a reverse Relay
probe to its source. VPN loss or replacement clears automatic grants. Android's
public APIs cannot attest which app owns the VPN; this mode assumes the user
selected Tailscale. Excluding Relay from the VPN leaves it unavailable.

TLS 1.3, key-possession checks, SHA-256 payload verification, bounded streaming
and safe filesystem handling remain in place. Automatic observations are not
converted into permanent manual trust. The optional `peer-directory-v1`
extension shares at most 256 bounded name/address/OS hints, never files, history
or trust grants. Every hint must pass local address and transport checks.

## Blocking, approvals and recovery

Use **Block** in device details, or `relay untrust DEVICE`, to stop a device's
access. Blocks persist through discovery refreshes and restarts. **Unblock** or
`relay trust DEVICE` allows automatic access again without a fingerprint prompt.

Pairing and receiving are separate policies. Android still asks before receiving
unless **Automatically accept** is enabled. Desktop retains its configured
`receive.auto_accept_trusted` setting.

Temporary Tailscale authorization outages interrupt transfers and allow retry
after fresh membership and the same expected key return. Explicit blocks,
membership removal and changed application keys pause affected jobs. A rotated
key can be learned automatically for new sends, but an old queued transfer is
never silently redirected to that new identity.

For explicit application-level fingerprint trust, set
`network.trust_tailnet = false` on desktop or disable automatic Tailnet trust in
Android's Advanced settings. Blocks still apply; verified manual trust can
explicitly clear a block. Older Relay versions retain their configured manual
policy, so update both endpoints for the no-pairing workflow.

## Validation

New regression tests cover first transfers with zero saved pins, a fresh phone
learning a desktop and a third peer, reverse-key mismatches, invalid WhoIs
identities, membership expiry/removal, VPN replacement, bounded hint lists,
storage-failure rollback, persistent blocks, manual-mode transitions, and
same-key recovery after a temporary authorization outage.

The local desktop's real Tailscale identity API and automatic-mode diagnostics
were checked. All 22 Android unit tests and all 20 Android 15 emulator device
tests passed, including the no-pairing UI and native bridge. The release variant
also installed and started successfully in [Android CI](https://github.com/PLASMA-FR/relay/actions/runs/34312963323).
Physical-phone Tailnet routing and OEM background behavior
remain unverified; see the [Android guide](../mobile/README.md) for setup limits.
