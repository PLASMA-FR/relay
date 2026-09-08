# Android 0.2.0 validation

Validated on 2026-09-08. The app is native Kotlin/Compose with a GoMobile bridge
to the existing Relay v1 transfer engine. It requires Android 8.0/API 26 or newer;
the distributed APK includes ARM64 and x86_64 native libraries.

## Passed checks

| Check | Evidence |
| --- | --- |
| Shared transfer core | 15 Go integration/lifecycle/security tests with race detection |
| Desktop compatibility | Real TLS engine round trips, file/text transfer, approval/rejection and verified partial-file resume after mobile-core restart |
| Android JVM tests | 19 tests covering parsing, file confinement, VPN addresses, notification planning and normal/inverted QR decoding |
| Android device tests | All 12 instrumentation tests passed on an Android 15/API 35 x86_64 Pixel 6 emulator |
| Actual native startup | Go identity creation/persistence, native errors, real MainActivity initialization; no test replacement for the Go library |
| Release variant startup | Installed and launched on the emulator using a disposable CI signing key; actual screen hierarchy and screenshot collected |
| Release APK | Built separately with the private release key; APK v2 signature verified, application ID and SDK metadata inspected |
| Modern native packaging | ARM64 native libraries use 16 KiB ELF alignment; the APK passes 16 KiB zip alignment verification |
| Static checks | Android lint passes, Go vet passes, Go dependency scan clean, shell scripts pass Shellcheck |
| Desktop regression | Full Go suite passes, including the Go 1.25 compatibility floor |

The final device-test and release-startup run is
[Android CI 34281471471](https://github.com/PLASMA-FR/relay/actions/runs/34281471471).
Its real screenshots were inspected and are included under
[screenshots/android](screenshots/android/).

The JVM QR tests use actual ZXing decoding of normal and inverted QR pixels.
They cover terminal color inversion rather than relying on a mocked scanner.
The device tests cover explicit fingerprint confirmation, incoming approvals,
shared-content target selection, clipboard review, verified-only file export,
transfer controls, real native startup, and light/dark rendering.

## Signing identity

The release APK is application `io.github.plasmafr.relay`, version `0.2.0`,
version code `1`. Its APK-signing certificate SHA-256 is:

```text
a58aedbb7b370b605a629739213bb2b2c38b2a8d9c0c8008eada384771fe57b1
```

This identifies Android application updates. It is separate from each phone's
random Relay peer identity used for Tailnet pairing. Private signing material
is stored outside the repository and is not included in CI artifacts or releases.

## Remaining environment limits

No physical phone or second Android device was connected to this workspace.
Actual phone-to-Tailnet routing, camera scanning of a physical terminal, OEM
battery policies, real background delivery and Android 8 hardware were not
manually verified. The emulator verifies Android code and native initialization;
the Go integration suite verifies transport interoperability separately.

Tailscale's Android source maps its node's local addresses into Android VPN
link properties. Relay reads those through Android's network APIs, avoiding
restricted native interface enumeration. This design is source-verified, not
a substitute for physical-device routing tests. Users must include Relay in
Tailscale's VPN configuration.

Background availability is an explicit foreground-service session, not an
always-on guarantee. Mobile sends are capped at 32 paths and 2 GiB. Desktop
protocol v1 remains compatible; full Tailnet enumeration and iOS are not
implemented in this Android release.
