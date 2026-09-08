package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type fakeBackend struct {
	mu      sync.Mutex
	sent    []model.SendRequest
	actions []model.Action
	watch   chan model.Snapshot
}

func (f *fakeBackend) Watch(ctx context.Context, fn func(model.Snapshot)) error {
	for {
		select {
		case s := <-f.watch:
			fn(s)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (f *fakeBackend) Send(_ context.Context, r model.SendRequest) (model.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r)
	return model.Result{OK: true, Message: "Queued; transfer continues when the device is reachable"}, nil
}
func (f *fakeBackend) Action(_ context.Context, a model.Action) (model.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, a)
	return model.Result{OK: true}, nil
}
func setup(t *testing.T) (*ui, *fakeBackend, tcell.SimulationScreen) {
	t.Helper()
	c := config.Default()
	c.Paths.StateDir = t.TempDir()
	c.Name = "desktop"
	c.UI.ASCII = false
	c.UI.VimKeys = true
	f := &fakeBackend{watch: make(chan model.Snapshot, 4)}
	u := newUI(context.Background(), f, c)
	t.Cleanup(u.cancel)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	u.connected = true
	u.apply(fixture())
	draw(u, screen, 80, 24)
	return u, f, screen
}
func fixture() model.Snapshot {
	return model.Snapshot{Name: "desktop", Tailscale: "Connected", Address: "100.64.0.5", ReceiveDirectory: "/home/me/relay-inbox", Peers: []model.Peer{{ID: "laptop", Name: "laptop", Address: "100.64.0.14", Relay: true, Online: true, Trusted: true, Fingerprint: strings.Repeat("ab", 32)}, {ID: "vps", Name: "dev-vps", Address: "100.64.0.27", Relay: true, Online: true, Trusted: true}}, Transfers: []model.Transfer{{ID: "one", Name: "challenge.zip", Peer: "laptop", Status: "transferring", Bytes: 438 << 20, Total: 503 << 20, Speed: 91 << 20, Direction: "send"}, {ID: "two", Name: "notes.txt", Peer: "dev-vps", Status: "offered", Total: 128}}, History: []model.Transfer{{ID: "done", Name: "screenshot.png", Status: "completed", Verified: true, Total: 2700000}}}
}
func draw(u *ui, s tcell.SimulationScreen, w, h int) {
	s.SetSize(w, h)
	s.Clear()
	u.layout(w, h)
	u.pages.SetRect(0, 0, w, h)
	r := terminalRoot{Primitive: u.pages, ascii: u.ascii, noColor: u.accent == tcell.ColorDefault}
	r.Draw(s)
	s.Show()
}
func rendered(s tcell.SimulationScreen) string {
	cells, w, h := s.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rs := cells[y*w+x].Runes
			if len(rs) == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune(rs[0])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
func press(u *ui, k tcell.Key, r rune) {
	e := u.key(tcell.NewEventKey(k, r, 0))
	if e != nil {
		if p := tview.Primitive(u.pages); p != nil {
			if handler := p.InputHandler(); handler != nil {
				handler(e, func(p tview.Primitive) { u.app.SetFocus(p) })
			}
		}
	}
}
func click(u *ui, x, y int) {
	handler := u.pages.MouseHandler()
	event := tcell.NewEventMouse(x, y, tcell.Button1, 0)
	handler(tview.MouseLeftDown, event, func(p tview.Primitive) { u.app.SetFocus(p) })
	handler(tview.MouseLeftClick, event, func(p tview.Primitive) { u.app.SetFocus(p) })
}
func drainUntil(t *testing.T, u *ui, done func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !done() {
		select {
		case fn := <-u.updates:
			fn()
		case <-deadline:
			t.Fatal("async UI operation did not finish")
		}
	}
}

func TestResponsiveKeyboardAndHistory(t *testing.T) {
	u, _, screen := setup(t)
	for _, size := range [][2]int{{80, 24}, {150, 40}, {48, 18}, {28, 10}, {80, 24}} {
		draw(u, screen, size[0], size[1])
		out := rendered(screen)
		if !strings.Contains(out, "RELAY") || !strings.Contains(out, "laptop") {
			t.Fatalf("missing primary content at %v:\n%s", size, out)
		}
	}
	press(u, tcell.KeyRune, 'j')
	if p, _ := u.selectedPeer(); p.ID != "vps" {
		t.Fatal("j did not select second device")
	}
	press(u, tcell.KeyRune, 'g')
	if p, _ := u.selectedPeer(); p.ID != "laptop" {
		t.Fatal("g did not select first device")
	}
	press(u, tcell.KeyRune, 'H')
	if !u.history || u.pane != 1 {
		t.Fatal("H did not show history")
	}
	draw(u, screen, 48, 18)
	if !strings.Contains(rendered(screen), "screenshot.png") {
		t.Fatal(rendered(screen))
	}
	press(u, tcell.KeyRune, 'h')
	if u.pane != 0 {
		t.Fatal("h must navigate, not collide with history")
	}
	press(u, tcell.KeyRune, '?')
	draw(u, screen, 80, 24)
	if !u.modal || !strings.Contains(rendered(screen), "keyboard and mouse") {
		t.Fatal(rendered(screen))
	}
	press(u, tcell.KeyEscape, 0)
	if u.modal {
		t.Fatal("Escape did not close modal")
	}
}
func TestMouseSelectsCorrectDeviceAndTransfer(t *testing.T) {
	u, _, s := setup(t)
	x, y, _, _ := u.devices.GetInnerRect()
	click(u, x+2, y+2)
	if p, _ := u.selectedPeer(); p.ID != "vps" {
		t.Fatalf("mouse selected stale device: %+v", p)
	}
	if u.modal {
		t.Fatal("device click should select directly, without an extra modal")
	}
	draw(u, s, 80, 24)
	x, y, _, _ = u.transfers.GetInnerRect()
	click(u, x+2, y+2)
	if !u.modal || u.detailID != "two" {
		t.Fatalf("click must show clicked transfer, not previous: modal=%v id=%s", u.modal, u.detailID)
	}
	draw(u, s, 80, 24)
	if !strings.Contains(rendered(s), "Accept") {
		t.Fatal(rendered(s))
	}
}

func TestRecentCompletionsStayVisibleAndHistoryRemainsComplete(t *testing.T) {
	u, _, screen := setup(t)
	u.focus(1)
	u.transfers.SetCurrentItem(0)
	snapshot := fixture()
	completed := snapshot.Transfers[0]
	completed.Status = "completed"
	completed.Verified = true
	completed.Bytes = completed.Total
	snapshot.Transfers = snapshot.Transfers[1:]
	snapshot.History = []model.Transfer{completed, {ID: "failed", Name: "failed.txt", Status: "failed"}}
	for i := range 6 {
		name := "recent-" + string(rune('a'+i))
		snapshot.History = append(snapshot.History, model.Transfer{ID: name, Name: name, Status: "completed", Verified: true})
	}
	u.apply(snapshot)
	if len(u.visibleTransfers) != 6 || u.visibleTransfers[0].ID != "two" || u.visibleTransfers[1].ID != "one" {
		t.Fatalf("expected active then five recent completions: %+v", u.visibleTransfers)
	}
	if selected, ok := u.selectedTransfer(); !ok || selected.ID != "one" {
		t.Fatalf("completion lost the selected transfer: %+v", selected)
	}
	line, sub := u.transfers.GetItemText(1)
	if !strings.Contains(line, "verified") || !strings.Contains(sub, "Recent") {
		t.Fatalf("missing recent verified result: %q / %q", line, sub)
	}
	snapshot.Transfers = nil
	u.apply(snapshot)
	draw(u, screen, 100, 32)
	if len(u.visibleTransfers) != 5 || !strings.Contains(rendered(screen), "challenge.zip") {
		t.Fatalf("quick completed transfer disappeared: %s", rendered(screen))
	}
	press(u, tcell.KeyEnter, 0)
	if u.detailID != "one" {
		t.Fatal("recent completion did not open its transfer details")
	}
	u.closeModal()
	u.query[1] = "recent-f"
	u.rebuildTransfers()
	if len(u.visibleTransfers) != 0 {
		t.Fatal("main pane included a completion older than its five recent results")
	}
	press(u, tcell.KeyRune, 'H')
	if len(u.visibleTransfers) != 1 || u.visibleTransfers[0].ID != "recent-f" {
		t.Fatal("full history did not retain older searchable completions")
	}
	u.query[1] = ""
	u.rebuildTransfers()
	if len(u.visibleTransfers) != len(snapshot.History) {
		t.Fatal("full history lost failed or older transfers")
	}
}
func TestBrowserMouseSelectionNavigationAndSend(t *testing.T) {
	u, f, s := setup(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello [red] 世界.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0600)
	u.lastDir = dir
	u.sendPicker()
	b := u.browser
	drainUntil(t, u, func() bool { return b.cwd == dir })
	draw(u, s, 100, 32)
	if len(b.visible) != 2 {
		t.Fatalf("expected two visible entries: %v", b.visible)
	}
	// Use the actual drawn Table cell coordinates, not estimated row hitboxes.
	x, y, w, h := b.table.GetInnerRect()
	found := false
	for yy := y; yy < y+h && !found; yy++ {
		for xx := x; xx < x+w; xx++ {
			row, col := b.table.CellAt(xx, yy)
			if row == 2 && col == 0 {
				click(u, xx, yy)
				found = true
				break
			}
		}
	}
	if !found || len(b.selected) != 1 {
		t.Fatalf("directory checkbox click did not select: %v", b.selected)
	}
	if row, _ := b.table.GetSelection(); row != 2 {
		t.Fatalf("checkbox click left keyboard selection on row %d", row)
	}
	press(u, tcell.KeyDown, 0)
	press(u, tcell.KeyRune, ' ')
	if len(b.selected) != 2 {
		t.Fatal("Down then Space after a checkbox click did not select the next file")
	}
	b.table.Select(2, 0)
	press(u, tcell.KeyEnter, 0)
	drainUntil(t, u, func() bool { return b.cwd == filepath.Join(dir, "project") })
	if len(b.selected) != 2 {
		t.Fatal("navigation lost selection")
	}
	press(u, tcell.KeyBackspace2, 0)
	drainUntil(t, u, func() bool { return b.cwd == dir })
	press(u, tcell.KeyRune, 's')
	drainUntil(t, u, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent[0].Paths) != 2 || f.sent[0].Peer != "laptop" {
		t.Fatalf("unexpected send: %+v", f.sent[0])
	}
}
func TestSearchPreservesTypedShortcuts(t *testing.T) {
	u, _, _ := setup(t)
	press(u, tcell.KeyRune, '/')
	for _, r := range "vps" {
		press(u, tcell.KeyRune, r)
	}
	if u.query[0] != "vps" || len(u.visiblePeers) != 1 {
		t.Fatalf("search shortcut intercepted typed text: %q, %v", u.query[0], u.visiblePeers)
	}
	press(u, tcell.KeyEnter, 0)
	if u.modal {
		t.Fatal("search did not close")
	}
	if p, _ := u.selectedPeer(); p.ID != "vps" {
		t.Fatal("wrong filtered peer")
	}
	press(u, tcell.KeyEscape, 0)
	if len(u.visiblePeers) != 2 {
		t.Fatal("Escape did not clear filter")
	}
}
func TestTrustRequiresExplicitComparison(t *testing.T) {
	u, f, s := setup(t)
	p := fixture().Peers[0]
	p.Trusted = false
	u.confirmTrust(p)
	draw(u, s, 80, 24)
	prompt := u.modalFocus.(*trustPrompt)
	prompt.trust.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { u.app.SetFocus(p) })
	if !u.modal {
		t.Fatal("trust accepted without fingerprint comparison")
	}
	f.mu.Lock()
	if len(f.actions) != 0 {
		t.Fatal("sent trust before confirmation")
	}
	f.mu.Unlock()
	prompt.check.SetChecked(true)
	prompt.trust.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { u.app.SetFocus(p) })
	drainUntil(t, u, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.actions) == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.actions[0]
	if a.Action != "trust" || a.Fingerprint != p.Fingerprint {
		t.Fatalf("trust did not pin full fingerprint: %+v", a)
	}
}
func TestTerminalSafeLabelsAndNoColorASCII(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	u, _, s := setup(t)
	u.ascii = true
	sn := fixture()
	sn.Peers[0].Name = "[red]bad\x1b]52;clipboard\a\n\u202eevil"
	u.apply(sn)
	draw(u, s, 80, 24)
	out := rendered(s)
	if !strings.Contains(out, "[red]") {
		t.Fatalf("markup interpreted: %s", out)
	}
	if strings.ContainsAny(out, "\x1b\a\u202e│─┌●") {
		t.Fatalf("unsafe controls / non-ASCII decoration: %q", out)
	}
	cells, _, _ := s.GetContents()
	for _, cell := range cells {
		fg, bg, _ := cell.Style.Decompose()
		if fg != tcell.ColorDefault || bg != tcell.ColorDefault {
			t.Fatalf("NO_COLOR violated: %v %v", fg, bg)
		}
	}
}
func TestReadDirectoryDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "normal"), nil, 0600)
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := readDirectory(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.name == "link" && !e.symlink {
			t.Fatal("symlink wasn't marked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readDirectory(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
func TestFormattingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{{0, "0 B"}, {1024, "1.0 KiB"}, {5 << 30, "5.0 GiB"}} {
		if got := bytes(tc.in); got != tc.want {
			t.Errorf("bytes(%d)=%s", tc.in, got)
		}
	}
	if got := progress(200, 100, 4, true); got != "==== 100%" {
		t.Fatal(got)
	}
	if !matches("development-vps", "dvp") || matches("laptop", "vps") {
		t.Fatal("fuzzy matching incorrect")
	}
}

func TestEventLoopWatchUpdatesAndShutdown(t *testing.T) {
	u, f, _ := setup(t)
	screen := tcell.NewSimulationScreen("UTF-8")
	u.app.SetScreen(screen)
	screen.SetSize(80, 24)
	done := make(chan error, 1)
	go func() { done <- u.app.Run() }()
	go u.watch()
	f.watch <- fixture()
	u.post(func() { u.quit() })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		u.app.Stop()
		t.Fatal("event loop failed to shut down")
	}
}

func TestIncomingOfferFocusAndNoRepeatedPrompt(t *testing.T) {
	u, _, s := setup(t)
	u.presentOffer()
	if !u.modal || u.detailID != "two" {
		t.Fatal("incoming offer was not presented")
	}
	draw(u, s, 80, 24)
	if b, ok := u.app.GetFocus().(*tview.Button); !ok || b.GetLabel() != "A Accept" {
		t.Fatal("Accept should be immediately keyboard reachable")
	}
	u.closeModal()
	u.presentOffer()
	if u.modal {
		t.Fatal("dismissed offer repeatedly interrupted the user")
	}
}
func TestTransferShortcutsCannotActFromDevicePane(t *testing.T) {
	u, f, _ := setup(t)
	press(u, tcell.KeyRune, 'x')
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.actions) > 0 {
		t.Fatal("X cancelled a hidden transfer while a device was selected")
	}
}
func TestNativeEventLoopMouseKeyboardAndResize(t *testing.T) {
	u, _, _ := setup(t)
	screen := tcell.NewSimulationScreen("UTF-8")
	u.app.SetScreen(screen)
	screen.SetSize(80, 24)
	checks := make(chan func(), 1)
	capture := u.app.GetInputCapture()
	u.app.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyF23 {
			(<-checks)()
			return nil
		}
		return capture(e)
	})
	done := make(chan error, 1)
	go func() { done <- u.app.Run() }()
	defer func() {
		u.cancel()
		u.app.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("UI event loop did not stop")
		}
	}()
	query := func(fn func()) {
		finished := make(chan struct{})
		checks <- func() { fn(); close(finished) }
		u.app.QueueEvent(tcell.NewEventKey(tcell.KeyF23, 0, 0))
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("UI event loop unresponsive")
		}
	}
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'j', 0))
	query(func() {
		if p, _ := u.selectedPeer(); p.ID != "vps" {
			t.Error("live keyboard navigation failed")
		}
	})
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'H', 0))
	query(func() {
		if !u.history || u.pane != 1 {
			t.Error("live history shortcut failed")
		}
	})
	screen.SetSize(48, 18)
	u.app.QueueEvent(tcell.NewEventResize(48, 18))
	u.app.QueueUpdateDraw(func() {})
	query(func() {
		if u.width != 48 {
			t.Errorf("resize ignored: %d", u.width)
		}
	})
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'h', 0))
	u.app.QueueUpdateDraw(func() {})
	var x, y int
	query(func() { x, y, _, _ = u.devices.GetInnerRect() })
	u.app.QueueEvent(tcell.NewEventMouse(x+2, y, tcell.Button1, 0))
	u.app.QueueEvent(tcell.NewEventMouse(x+2, y, tcell.ButtonNone, 0))
	query(func() {
		if p, _ := u.selectedPeer(); p.ID != "laptop" {
			t.Errorf("live mouse selected wrong peer: %s", p.ID)
		}
		if u.modal {
			t.Error("live device click unexpectedly opened a modal")
		}
	})
}

func TestSendOnDisconnectedOrUnpairedPeerIsActionable(t *testing.T) {
	u, f, s := setup(t)
	u.connected = false
	press(u, tcell.KeyRune, 's')
	draw(u, s, 80, 24)
	if !u.modal || !strings.Contains(rendered(s), "Daemon disconnected") {
		t.Fatal(rendered(s))
	}
	u.closeModal()
	u.connected = true
	sn := fixture()
	sn.Peers[0].Trusted = false
	u.apply(sn)
	press(u, tcell.KeyRune, 's')
	draw(u, s, 80, 24)
	if !u.modal || !strings.Contains(rendered(s), "Compare & trust") {
		t.Fatal(rendered(s))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) > 0 || len(f.actions) > 0 {
		t.Fatal("unpaired send must never implicitly grant trust")
	}
}

func TestOfflineTrustedSendKeyboardAndMouse(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "headless-test")
	for _, kind := range []string{"files", "clipboard", "text", "url"} {
		for _, mouse := range []bool{false, true} {
			mode := "keyboard"
			if mouse {
				mode = "mouse"
			}
			t.Run(kind+"/"+mode, func(t *testing.T) {
				u, f, screen := setup(t)
				snapshot := fixture()
				snapshot.Peers[0].Online = false
				snapshot.Peers[0].Relay = false
				u.apply(snapshot)
				u.lastDir = t.TempDir()
				path := filepath.Join(u.lastDir, "queued.txt")
				if err := os.WriteFile(path, []byte("queued file"), 0600); err != nil {
					t.Fatal(err)
				}
				payload := "offline context"
				if kind == "url" {
					payload = "https://example.com/queued"
				}
				if err := clipboard.New(u.cfg.Paths.StateDir).WriteInternal(payload); err != nil {
					t.Fatal(err)
				}
				activate := func(p tview.Primitive) {
					t.Helper()
					draw(u, screen, 100, 32)
					if mouse {
						x, y, w, _ := p.GetRect()
						click(u, x+w/2, y)
					} else {
						u.app.SetFocus(p)
						press(u, tcell.KeyEnter, 0)
					}
				}
				switch kind {
				case "files":
					if mouse {
						activate(u.actionButtons[0])
					} else {
						press(u, tcell.KeyRune, 'S')
					}
					b := u.browser
					if b == nil {
						t.Fatal("offline trusted device did not open the picker")
					}
					drainUntil(t, u, func() bool { return b.cwd == u.lastDir })
					b.table.Select(2, 0)
					if mouse {
						activate(b.buttons.GetItem(0))
					} else {
						press(u, tcell.KeyRune, 'S')
					}
				case "clipboard":
					if mouse {
						activate(u.actionButtons[1])
					} else {
						press(u, tcell.KeyRune, 'C')
					}
				case "text", "url":
					if mouse {
						activate(u.actionButtons[3])
					} else {
						press(u, tcell.KeyRune, ' ')
					}
					list, ok := u.modalFocus.(*tview.List)
					if !ok {
						t.Fatal("offline trusted device did not open send actions")
					}
					index := 2
					if kind == "url" {
						index = 3
					}
					if mouse {
						draw(u, screen, 100, 32)
						x, y, _, _ := list.GetInnerRect()
						click(u, x+2, y+2*index)
					} else {
						press(u, tcell.KeyRune, rune(kind[0]))
					}
					form, ok := u.modalFocus.(*tview.Form)
					if !ok {
						t.Fatal("send action did not open composer")
					}
					if kind == "url" {
						form.GetFormItem(0).(*tview.InputField).SetText(payload)
					} else {
						form.GetFormItem(0).(*tview.TextArea).SetText(payload, true)
					}
					activate(form.GetButton(0))
				}
				drainUntil(t, u, func() bool { return strings.Contains(u.status.GetText(true), "when the device is reachable") })
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.sent) != 1 || f.sent[0].Peer != "laptop" || f.sent[0].Kind != kind {
					t.Fatalf("unexpected queued send: %+v", f.sent)
				}
				if kind == "files" {
					if len(f.sent[0].Paths) != 1 || f.sent[0].Paths[0] != path {
						t.Fatalf("unexpected queued paths: %+v", f.sent[0].Paths)
					}
				} else if f.sent[0].Text != payload {
					t.Fatalf("queued content changed: %q", f.sent[0].Text)
				}
				if u.history || u.pane != 1 || u.modal || len(f.actions) != 0 {
					t.Fatal("send must show transfers without implicitly pairing")
				}
			})
		}
	}
}

func TestOfflineSendRequiresCurrentTrustAndFingerprint(t *testing.T) {
	for _, state := range []string{"unpaired", "identity changed", "missing fingerprint"} {
		t.Run(state, func(t *testing.T) {
			u, f, screen := setup(t)
			snapshot := fixture()
			snapshot.Peers[0].Relay = false
			snapshot.Peers[0].Online = false
			if state == "missing fingerprint" {
				snapshot.Peers[0].Fingerprint = ""
			} else {
				snapshot.Peers[0].Trusted = false
				if state == "identity changed" {
					snapshot.Peers[0].Fingerprint = strings.Repeat("cd", 32)
				}
			}
			u.apply(snapshot)
			for _, key := range []rune{'S', 'C', ' '} {
				press(u, tcell.KeyRune, key)
				draw(u, screen, 100, 32)
				if !u.modal || !strings.Contains(rendered(screen), "Relay is not reachable") || !strings.Contains(rendered(screen), "Start relay daemon") {
					t.Fatalf("missing unreachable guidance: %s", rendered(screen))
				}
				u.closeModal()
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.sent) != 0 || len(f.actions) != 0 {
				t.Fatal("unverified offline device must never submit or implicitly pair")
			}
		})
	}
}

func TestComposerRechecksTrustBeforeSubmission(t *testing.T) {
	u, f, screen := setup(t)
	p, _ := u.selectedPeer()
	u.compose(p, "text")
	form := u.modalFocus.(*tview.Form)
	form.GetFormItem(0).(*tview.TextArea).SetText("pending context", true)
	snapshot := fixture()
	snapshot.Peers[0].Trusted = false
	snapshot.Peers[0].Fingerprint = strings.Repeat("cd", 32)
	u.apply(snapshot)
	u.app.SetFocus(form.GetButton(0))
	press(u, tcell.KeyEnter, 0)
	draw(u, screen, 100, 32)
	if !strings.Contains(rendered(screen), "trust changed") {
		t.Fatalf("missing changed identity guidance: %s", rendered(screen))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 0 || len(f.actions) != 0 {
		t.Fatal("stale composer submitted to a changed identity")
	}
}

func TestResizePreservesFocusedButtons(t *testing.T) {
	u, _, s := setup(t)
	u.app.SetFocus(u.tabButtons[4])
	draw(u, s, 28, 10)
	press(u, tcell.KeyEnter, 0)
	if !u.modal {
		t.Fatal("resizing lost focus on Find tab")
	}
	u.closeModal()
	u.app.SetFocus(u.actionButtons[3])
	draw(u, s, 150, 40)
	draw(u, s, 28, 10)
	press(u, tcell.KeyEnter, 0)
	if !u.modal {
		t.Fatal("resizing lost focus on More action")
	}
	u.closeModal()
	u.app.SetFocus(u.actionButtons[6])
	press(u, tcell.KeyBacktab, 0)
	if u.app.GetFocus() != u.actionButtons[5] {
		t.Fatal("nested compact footer lost keyboard traversal")
	}
	for _, size := range [][2]int{{24, 8}, {10, 4}, {1, 1}, {80, 24}} {
		draw(u, s, size[0], size[1])
	}
	if !strings.Contains(rendered(s), "RELAY") {
		t.Fatal("screen did not recover after a very small resize")
	}
}
func TestModalPreventsMouseClickThrough(t *testing.T) {
	u, _, s := setup(t)
	u.help()
	draw(u, s, 100, 30)
	x, y, w, _ := u.actionButtons[6].GetRect()
	click(u, x+w/2, y)
	if u.ctx.Err() != nil {
		t.Fatal("background Quit activated through modal")
	}
	if !u.modal {
		t.Fatal("background mouse click dismissed modal")
	}
	if !u.modalFocus.HasFocus() {
		t.Fatal("background mouse click stole modal focus")
	}
}
func TestBrowserEscapeAndMouseKeyboardParity(t *testing.T) {
	u, _, s := setup(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "visible"), nil, 0600)
	os.WriteFile(filepath.Join(dir, ".secret"), nil, 0600)
	u.lastDir = dir
	u.sendPicker()
	b := u.browser
	drainUntil(t, u, func() bool { return b.cwd == dir })
	draw(u, s, 80, 24)
	buttonClick := func(index int) {
		draw(u, s, 80, 24)
		x, y, w, _ := b.buttons.GetItem(index).GetRect()
		click(u, x+w/2, y)
	}
	buttonClick(1)
	if !b.hidden || len(b.visible) != 2 {
		t.Fatal("mouse Hidden button failed")
	}
	u.app.SetFocus(b.table)
	press(u, tcell.KeyRune, '.')
	if b.hidden {
		t.Fatal("keyboard hidden toggle differed from mouse")
	}
	buttonClick(2)
	if b.order != 1 {
		t.Fatal("mouse Sort button failed")
	}
	u.app.SetFocus(b.table)
	press(u, tcell.KeyRune, 'O')
	if b.order != 2 {
		t.Fatal("keyboard sort differed from mouse")
	}
	u.app.SetFocus(b.filter)
	for _, r := range "sq?/h" {
		press(u, tcell.KeyRune, r)
	}
	if b.filter.GetText() != "sq?/h" || u.browser != b {
		t.Fatal("file filter intercepted command letters")
	}
	draw(u, s, 24, 8)
	press(u, tcell.KeyEscape, 0)
	if u.modal || u.browser != nil {
		t.Fatal("Escape failed in tiny-terminal file filter")
	}
	if u.app.GetFocus() != u.devices {
		t.Fatal("Escape did not return focus to device pane")
	}
}
func TestOfferMemoryPrunedAfterCompletion(t *testing.T) {
	u, _, _ := setup(t)
	u.presentOffer()
	if len(u.seenOffers) != 1 {
		t.Fatal("offer tracking missing")
	}
	u.closeModal()
	snapshot := fixture()
	snapshot.Transfers[1].Status = "completed"
	u.apply(snapshot)
	if len(u.seenOffers) != 0 {
		t.Fatal("completed offers accumulated in UI memory")
	}
}
func TestAsyncJobsAreBounded(t *testing.T) {
	u, _, _ := setup(t)
	release := make(chan struct{})
	for i := 0; i < 100; i++ {
		u.async("test", func(ctx context.Context) (model.Result, error) {
			select {
			case <-ctx.Done():
				return model.Result{}, ctx.Err()
			case <-release:
				return model.Result{OK: true}, nil
			}
		}, nil)
	}
	if got := len(u.workSlots); got != 8 {
		t.Fatalf("in-flight jobs unbounded: %d", got)
	}
	close(release)
	deadline := time.After(3 * time.Second)
	for len(u.workSlots) > 0 {
		select {
		case fn := <-u.updates:
			fn()
		case <-time.After(time.Millisecond):
		case <-deadline:
			t.Fatal("job slots leaked after completion")
		}
	}
}

func TestMouseSearchDoneAndPathGo(t *testing.T) {
	u, _, s := setup(t)
	x, y, w, _ := u.tabButtons[4].GetRect()
	click(u, x+w/2, y)
	if !u.modal {
		t.Fatal("Find mouse button failed")
	}
	draw(u, s, 80, 24)
	done := u.modalFocus.(*tview.Flex).GetItem(1)
	x, y, w, _ = done.GetRect()
	click(u, x+w/2, y)
	if u.modal {
		t.Fatal("search Done mouse button failed")
	}
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	u.lastDir = dir
	u.sendPicker()
	b := u.browser
	drainUntil(t, u, func() bool { return b.cwd == dir })
	b.path.SetText(child)
	draw(u, s, 80, 24)
	x, y, w, _ = b.goPath.GetRect()
	click(u, x+w/2, y)
	drainUntil(t, u, func() bool { return b.cwd == child })
	if !b.table.HasFocus() {
		t.Fatal("Go did not return focus to file rows")
	}
}
func TestTrustFingerprintWrapsAtNarrowWidth(t *testing.T) {
	u, _, s := setup(t)
	peer := fixture().Peers[0]
	peer.Trusted = false
	peer.Fingerprint = strings.Repeat("0123456789abcdef", 4)
	u.confirmTrust(peer)
	draw(u, s, 48, 18)
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r >= 0x2500 && r <= 0x257f {
			return -1
		}
		return r
	}, rendered(s))
	if !strings.Contains(compact, peer.Fingerprint) {
		t.Fatalf("narrow trust dialog hides fingerprint:\n%s", rendered(s))
	}
	draw(u, s, 24, 8)
	out := rendered(s)
	if !strings.Contains(out, "Match confirmed") || !strings.Contains(out, "Trust") || !strings.Contains(out, "Cancel") {
		t.Fatalf("tiny trust controls inaccessible:\n%s", out)
	}
}
func TestConcurrentUIDecorationsDoNotMutateGlobals(t *testing.T) {
	before := tview.Borders
	first, _, a := setup(t)
	second, _, b := setup(t)
	second.ascii = true
	second.accent = tcell.ColorDefault
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			draw(first, a, 80+i, 24)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			draw(second, b, 48+i, 18)
		}
	}()
	wg.Wait()
	if before != tview.Borders {
		t.Fatal("ASCII adapter mutated global tview borders")
	}
}

func TestProgressSnapshotsCoalesceToLatest(t *testing.T) {
	u, _, _ := setup(t)
	for i := 0; i < 1000; i++ {
		snapshot := fixture()
		snapshot.Transfers[0].Bytes = int64(i)
		u.postSnapshot(snapshot)
	}
	if len(u.updates) != 1 {
		t.Fatalf("stale snapshots accumulated: %d", len(u.updates))
	}
	u.drain()
	if u.snapshot.Transfers[0].Bytes != 999 {
		t.Fatal("latest progress was lost")
	}
	if u.latestSnapshot != nil || u.snapshotQueued {
		t.Fatal("snapshot copy retained after rendering")
	}
}
func TestFilePickerHonorsDisabledVimKeys(t *testing.T) {
	u, _, _ := setup(t)
	u.cfg.UI.VimKeys = false
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file"), nil, 0600)
	u.lastDir = dir
	u.sendPicker()
	b := u.browser
	drainUntil(t, u, func() bool { return b.cwd == dir })
	b.table.Select(1, 0)
	press(u, tcell.KeyRune, 'j')
	row, _ := b.table.GetSelection()
	if row != 1 {
		t.Fatal("native widget bypassed vim_keys=false")
	}
	press(u, tcell.KeyDown, 0)
	row, _ = b.table.GetSelection()
	if row != 2 {
		t.Fatal("disabling Vim keys broke arrows")
	}
}
