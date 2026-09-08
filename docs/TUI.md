# Living in Relay

Run `relay` to attach to the daemon. Closing Relay, your SSH connection, or the
terminal leaves the daemon and its transfers running. If the daemon restarts,
the interface reconnects automatically.

Select a device, press **S**, select paths with **Space**, then **S** to send.
For a single highlighted file, pressing **S** in the picker sends it immediately.
Directory selections include their contents. Selection survives navigation, so
files from several directories can travel together. Selecting a parent and its
child sends that content only once.

Previously trusted devices can receive queued files, clipboard content, text,
and URLs while offline. Relay confirms the queue after submission and sends
when the device's daemon becomes reachable again. Keep selected source files
in place until the transfer finishes. A stored fingerprint and current trust
are required; new or changed identities must be verified before sending.

Transfers keeps the five most recent completions below active transfers, with
verified results marked, so quick sends remain visible. Press **H** for the
complete history, including failed, cancelled, and rejected transfers.

## Navigation

| Key | Action |
| --- | --- |
| Arrows; `j` / `k` | Move through rows |
| Left / Right; `h` / `l` | Change device / transfer pane |
| `g` / `G` | First / last row |
| Tab / Shift-Tab | Cycle panes, tabs, and buttons |
| Enter / `D` | Device identity or transfer details |
| `S` / `F` | Send files and directories |
| `C` | Send the current clipboard |
| Space | All send actions: files, clipboard, text, URL |
| `H` | History / active transfers |
| `/` | Incremental fuzzy search in the current pane |
| `R` | Refresh devices; resume/retry the selected transfer |
| `P` / `X` | Pause / cancel the selected transfer |
| `A` | Accept the selected incoming transfer |
| Esc | Close a dialog or clear the current search |
| `?` | Full keyboard reference |
| `Q` / Ctrl-C | Close the client |

Letter shortcuts accept lowercase and uppercase except **H**: lowercase **h**
belongs to Vim pane navigation. Text entry fields keep their ordinary typing
behavior. Vim bindings can be disabled with `[ui] vim_keys = false`.

Click devices to select them; click transfers for details and actions. Tabs,
buttons, checkboxes, and dialogs use native widget hit detection. The mouse
wheel scrolls lists. Your terminal's usual selection modifier (often Shift)
lets you select terminal text while mouse reporting is enabled.

## File picker

| Key | Action |
| --- | --- |
| Space / checkbox click | Toggle a file or directory |
| Enter / filename click | Open a directory or toggle a file |
| Backspace; `h` | Parent directory |
| `/` | Focus fuzzy filename filtering |
| Ctrl-L | Enter a path (`~` is supported) |
| `~` | Home directory |
| `.` | Toggle hidden files |
| `O` | Cycle name, size, and newest-first sorting |
| `S` | Send selection, or the highlighted path |
| Esc | Close picker |

The picker reopens the last visited directory for this session. It reads
metadata asynchronously and does not recursively walk directories to display
sizes. The summary separates known file bytes from directory selections.
Symlinks are visibly excluded in V1. Large listings are limited to 50,000
entries; enter a more specific path if needed.

## Receiving and identity

An incoming offer opens an Accept / Reject dialog when no other dialog is in
use. Accept has initial focus. Esc dismisses the prompt without rejecting the
offer; it stays in Transfers. Relay prompts only once per offer per TUI session.
The status bar also counts offers awaiting approval. Trusted auto-accept is
controlled by the daemon's receive configuration.

Device details show the full fingerprint. Compare it with `relay status` on the
other device before checking the comparison box and granting trust. Repeat on
both devices. Revocation is available in the same device dialog.

## SSH and compact terminals

At 80 columns, devices and transfers share the screen. Wider terminals add a
live details pane. Below 72 columns, the selected pane fills the screen; tabs and
Left / Right keep the other pane reachable. Dialogs fit the terminal and long
details scroll.

The clipboard indicator reflects the environment of the **TUI**, which can
differ from the systemd daemon's environment. Desktop sessions use available
Wayland/X11 tools; SSH and headless sessions use Relay Clipboard. Receiving text
never injects it into your desktop clipboard or terminal. URL sharing delivers
text and never launches remote applications.

`NO_COLOR` removes color throughout the interface. `[ui] ascii = true`, the Linux
console, and a `LANG=C` environment use ASCII decorations. Unicode filenames
remain intact. Names and peer metadata are escaped before rendering: terminal
control sequences, bidirectional overrides, and widget markup are not executed.

## Verification

`go test -race ./internal/tui` exercises tcell SimulationScreen rendering at
28, 48, 80, and 150 columns; real event-loop keyboard, mouse, and resize events;
file checkbox hit detection; multi-directory selection; offline trusted queues;
unpaired and unreachable send guidance; trust confirmation;
search; incoming approval; clipboard labels; terminal escaping; color/ASCII
fallbacks; and shutdown. These simulations supplement real terminal testing;
they do not claim physical validation of every terminal emulator or GUI backend.
