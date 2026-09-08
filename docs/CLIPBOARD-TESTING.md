# Clipboard backend validation

On 2026-09-08 the real clipboard integration test passed on these isolated
virtual display environments on Linux arm64:

| Backend | Environment | Result |
| --- | --- | --- |
| X11 / xclip 0.13 | Xvfb, no TCP listener | Passed |
| X11 / xsel 1.2.1 | Xvfb, xclip excluded from PATH | Passed |
| Wayland / wl-clipboard 2.2.1 | Weston 13 X11 backend and pixman on Xvfb | Passed |

Tests used actual clipboard executables and display protocols. They checked
Unicode, emoji, trailing newlines, empty text, the internal mirror, and changes
made by a separate clipboard owner. Internal fallback was disabled so a failed
system operation could not pass by reading Relay's stored text. This found and
fixed an inherited-output-pipe issue with xclip's background clipboard owner.

To reproduce with the appropriate tools in a disposable display session:

```sh
RELAY_CLIPBOARD_INTEGRATION=1 go test ./internal/clipboard \
  -run '^TestDisplayClipboardIntegration$' -count=1 -v
```

The process needs that session's `DISPLAY` or `WAYLAND_DISPLAY` and, for Wayland,
its private `XDG_RUNTIME_DIR`. The test clears SSH markers and replaces the
session clipboard with synthetic data: use an isolated session. To test xsel,
exclude xclip from the test's PATH.

Pure headless Weston lacked a usable input seat here; nested Weston supplied a
virtual seat. This verifies actual Wayland clients, without claiming physical
GNOME, KDE or Sway validation. Default unit tests cover headless/SSH fallback.
