# Relay for Android

Relay for Android sends files, text, and links between your phone and your other
Relay devices over Tailscale. It uses the same authenticated, resumable v1 transfer
engine as the desktop application, with a native Android interface and a private
inbox. There is no separate Relay account or Relay cloud service.

The first Android app is version **0.2.0**, included with the **Relay 0.2.0**
release. Existing desktop Relay 0.1.0 transfers remain protocol-compatible.
The `relay mobile invite` and `relay mobile pair` convenience commands require an
updated desktop build from this release. See the [desktop guide](../README.md)
for installation and ordinary desktop commands.

## What you need

- Android 8.0 / API 26 or newer. The default APK includes ARM64 phone and x86_64
  emulator/device libraries; it does not include 32-bit ARM libraries.
- The Tailscale Android app, connected to the same tailnet as the other device.
  If you use Tailscale's app exclusions, ensure **Relay is included in the VPN**.
  Relay does not bundle or start its own VPN.
- Relay running on the other device, with tailnet policy allowing the connection.
  The phone listens on TCP port 7331 at its Tailscale address.

Install the Android APK from the relevant
[Relay release](https://github.com/PLASMA-FR/relay/releases), when available, or
build a debug APK using the instructions below. Android may ask you to allow APK
installation from the browser or file manager used to open it.

## Connect a phone and computer

1. Connect Tailscale on both devices and start desktop Relay. In the Android app,
   turn **Device availability** on. The status becomes ready when Relay can bind
   to the Tailscale VPN address.
2. On the computer, generate its public invitation:

   ```sh
   relay mobile invite --qr
   ```

3. On the phone, choose **Add device**, then scan the terminal QR code or paste
   the invitation. Scanning requests camera permission; pasting does not need it.
   Relay probes the computer and checks that its observed TLS fingerprint matches
   the invitation. Compare the complete fingerprint with the computer, or confirm
   that you obtained the invitation directly from it, then choose **Trust device**.
4. Open the phone's device details and use **Copy link** or **Share link** to obtain
   its own invitation. On the computer, deliberately run:

   ```sh
   relay mobile pair 'LINK'
   ```

   Replace `LINK` with the complete invitation from your phone and keep the quotes.
   This command probes the phone, verifies the invitation fingerprint, and grants
   desktop trust only after the match. If the phone is missing from the desktop
   tailnet peer list, check Tailscale, enable phone availability, and refresh
   desktop Relay before trying again.

Trust is required on **both** devices. Importing or opening a `relay://pair` link
on Android never grants trust automatically. Treat a pairing link as public
identity information whose origin you need to verify; an arbitrary link from
another app or website is not approval to trust its sender.

You can also add a saved device by its numeric Tailscale address, optionally with
an explicit Relay port. DNS names and ordinary LAN addresses are not accepted by
the mobile core. Android's **Refresh** checks your saved devices; it does not
enumerate the entire tailnet. A changed fingerprint requires a fresh comparison
and explicit trust decision.

## Send, receive, and keep files

Select a trusted, reachable device to send files, text, a web link, or clipboard
text. **Send clipboard** reads the Android clipboard only when you select it and
lets you review the text before sending. You can also choose Relay from another
Android app's share sheet, then select the destination device and send.

The file picker imports private staged copies before queueing a transfer. Each
handoff accepts **1–32 file paths** with an aggregate limit of **2 GiB**. Incoming
transfers also have a 2 GiB receive limit. Text and links are limited to **1 MiB**;
links must use HTTP or HTTPS and are never opened automatically. Allow enough
free app storage for staged outgoing copies and incoming files.

Incoming offers from trusted devices require **Accept** or **Reject** by default.
Use the Transfers tab, or the incoming-transfer notification while availability
is enabled. Android 13 and newer ask for notification permission; if you decline,
open Relay to review offers. **Automatically accept** in device settings applies
only to trusted senders and is off by default.

Received text appears as the **latest Relay Clipboard** in the Inbox. It never
replaces the Android system clipboard automatically. Use the explicit copy action
when you want it there. Relay keeps the latest received text privately, rather
than retaining a text-content archive in transfer history.

Completed files appear in the private Inbox with verification status. Use **Save to device**
to export a file through Android's document picker, or **Share** to grant another
app temporary read access. Files are not published automatically to Downloads or
a shared storage folder. Filename collisions are handled by renaming incoming
files instead of overwriting existing ones.

The Transfers tab exposes pause, resume/retry, and cancel where applicable.
Incomplete jobs, their original manifests, and saved trust survive process
restarts. Reconnect Tailscale and turn availability back on to continue; manually
paused transfers need **Resume**. Failed jobs keep their sources for inspection
or retry. Cancelled or rejected jobs cannot be resumed as the same job.

After a successful completion, cancellation, or rejection, the core removes
outgoing staged copies once the worker has released them and the final state is
durable. It retains copies referenced by another active or resumable job, including
failed jobs, and never deletes an Inbox original merely because it was forwarded.

Incoming partial files and protocol journals are currently retained for resume
and need explicit local cleanup when abandoned. There is no per-transfer storage
purge screen yet. They are in private app storage, so ordinary file managers cannot
clean them directly. Developer inspection or app-data cleanup must happen with
related transfers stopped. Clearing app data or uninstalling is a complete reset,
not a selective cleanup: export wanted Inbox files first, and expect to pair again.

## Availability and identity

Availability is an explicitly started foreground session, with a persistent
notification and a **Stop** action. Turning it off stops network activity while
keeping resumable state. Losing the VPN pauses connectivity; an existing session
can reconnect when the VPN returns.

Android or the device manufacturer's power management can still stop the process
or foreground service. Relay does not restart at boot or silently restart after
process death. Open the app and enable availability again when needed; do not
assume a phone is permanently reachable just because you enabled it earlier.

Each installation has a private identity stored outside Android backup. The app
also disables backup of its data. Normal app restarts and updates preserve this
identity. Uninstalling, clearing app data, or losing the installation's private
storage removes it and requires fingerprint verification and pairing again.
Debug and release installs have separate identities, trust, queues, and inboxes.

For the shared wire protocol and trust boundaries, read the
[security model](../SECURITY.md) and [protocol specification](../docs/PROTOCOL.md).

## Build an APK

Use a host supported by the upstream GoMobile/Android NDK toolchain: **Linux
x86_64 or macOS**. The build script does not configure Windows or Linux ARM64
cross-compilation workarounds.

Install:

- Go **1.26 or newer** for the isolated `mobile/bind` tool module; CI uses Go 1.27.
  The desktop module still has its own Go 1.25 minimum.
- JDK **17**, with `JAVA_HOME` set appropriately.
- Android SDK command-line tools, platform **35**, build-tools **35.0.0**, and NDK
  **27.2.12479018**, with `ANDROID_HOME` pointing to the SDK directory.

For example, after installing the SDK command-line tools and accepting their
licenses:

```sh
sdkmanager 'platforms;android-35' 'build-tools;35.0.0' 'ndk;27.2.12479018'
./mobile/build-android.sh debug
```

Run the build command from the repository root. It builds the GoMobile tools and
shared core AAR, then runs Gradle's debug unit tests and lint before assembling the
APK. The Gradle wrapper pins Gradle 8.11.1; the project uses AGP 8.9.2 and Kotlin
2.1.20. Dependencies and the Android SDK must be available to the build host.

Outputs:

| Build | Output |
| --- | --- |
| Shared native core | `mobile/android/app/libs/relaycore.aar` |
| Debug APK | `mobile/android/app/build/outputs/apk/debug/app-debug.apk` |
| Signed release APK | `mobile/android/app/build/outputs/apk/release/app-release.apk` |

Install the debug APK on a connected device or running emulator:

```sh
adb install -r mobile/android/app/build/outputs/apk/debug/app-debug.apk
```

Debug uses application ID `io.github.plasmafr.relay.debug` and can be installed
alongside release `io.github.plasmafr.relay`. A debug build does not replace or
reuse a release installation's identity or data.

To work in Android Studio, first run `./mobile/build-core.sh` from the repository
root, then open `mobile/android`. The Android project consumes the generated AAR;
opening it directly does not compile the Go core for you. Rebuild the AAR after
changes to Go code. The default native targets are `android/arm64,android/amd64`;
`RELAY_ANDROID_TARGETS` can override them for a supported toolchain. An alternate
installed NDK can be selected with `ANDROID_NDK_HOME`.

### Signed release builds

Use a private release keystore outside the repository. Supply these environment
variables through your local secret manager or CI secret configuration:

| Variable | Purpose |
| --- | --- |
| `RELAY_ANDROID_KEYSTORE` | Absolute path to the release keystore |
| `RELAY_ANDROID_STORE_PASSWORD` | Keystore password |
| `RELAY_ANDROID_KEY_ALIAS` | Signing alias; defaults to `relay` |
| `RELAY_ANDROID_KEY_PASSWORD` | Private-key password |

Then run:

```sh
./mobile/build-android.sh release
```

The release helper requires a configured keystore. Never commit signing files or
passwords. Keep the release signing key safe: Android updates need the same signing
identity. The first Android release uses app version `0.2.0` and Android version code `1`.

## Verification

Run the ordinary Go integration and race tests from the repository root:

```sh
go test -race ./mobile/relaycore ./internal/invite
```

The core tests run against the real v1 transfer engine using package-private
loopback injection. They cover file/text interoperability, fingerprint pinning,
manual approvals, revocation, restart recovery, actual partial-file resume,
source confinement, lifecycle races, and shared-source staging cleanup. Production
mobile APIs expose no loopback or insecure-network switch.

After building the AAR, run Android checks from `mobile/android`:

```sh
./gradlew --no-daemon testDebugUnitTest lintDebug assembleDebug assembleDebugAndroidTest
./gradlew --no-daemon connectedDebugAndroidTest
```

The second command needs an attached Android device or emulator. The
[Android workflow](../.github/workflows/android.yml) configures an API 35 x86_64
emulator, runs instrumentation, and collects APKs, reports, and available emulator
screenshots. Instrumentation checks native initialization and interface behavior;
it does not replace a physical-phone Tailscale transfer test. Consult the actual
workflow reports for test results; the existence of a workflow is not a claim
that instrumentation has passed.
