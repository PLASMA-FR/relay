// Package tui is Relay's event-driven terminal client. Networking and transfer
// ownership stay in the daemon; the terminal can disconnect at any time.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/ipc"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type backend interface {
	Watch(context.Context, func(model.Snapshot)) error
	Send(context.Context, model.SendRequest) (model.Result, error)
	Action(context.Context, model.Action) (model.Result, error)
}

type ui struct {
	mouseSelecting                        bool
	clipboardBackend                      string
	transferDetail                        *tview.TextView
	detailID                              string
	seenOffers                            map[string]bool
	app                                   *tview.Application
	client                                backend
	cfg                                   config.Config
	ctx                                   context.Context
	cancel                                context.CancelFunc
	updates                               chan func()
	workSlots                             chan struct{}
	tabButtons, actionButtons             []*tview.Button
	scheduled                             atomic.Bool
	snapshotMu                            sync.Mutex
	latestSnapshot                        *model.Snapshot
	snapshotQueued                        bool
	pages                                 *tview.Pages
	main, body, actions, tabs             *tview.Flex
	header, status, detail                *tview.TextView
	devices, transfers                    *tview.List
	snapshot                              model.Snapshot
	visiblePeers                          []model.Peer
	visibleTransfers                      []model.Transfer
	pane                                  int
	history                               bool
	query                                 [2]string
	width, height                         int
	modal                                 bool
	modalFocus                            tview.Primitive
	browser                               *browser
	lastDir                               string
	connected                             bool
	accent, muted, foreground, background tcell.Color
	ascii                                 bool
}

// Run opens Relay's interactive client. The daemon outlives this function.
func Run(ctx context.Context, client *ipc.Client, cfg config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u := newUI(ctx, client, cfg)
	defer u.cancel()
	before := u.app.GetBeforeDrawFunc()
	var started sync.Once
	u.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		started.Do(func() { go u.watch(); go func() { <-u.ctx.Done(); u.app.Stop() }() })
		return before(screen)
	})
	return u.app.Run()
}

func newUI(ctx context.Context, client backend, cfg config.Config) *ui {
	ctx, cancel := context.WithCancel(ctx)
	u := &ui{app: tview.NewApplication(), client: client, cfg: cfg, ctx: ctx, cancel: cancel,
		updates: make(chan func(), 32), workSlots: make(chan struct{}, 8), seenOffers: map[string]bool{}, ascii: cfg.UI.ASCII || os.Getenv("TERM") == "linux" || strings.EqualFold(os.Getenv("LANG"), "C"),
		foreground: tcell.ColorWhite, background: tcell.ColorDefault, accent: tcell.NewHexColor(0x71d5bd), muted: tcell.NewHexColor(0x8f9ba8)}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		u.foreground = tcell.ColorDefault
		u.accent = tcell.ColorDefault
		u.muted = tcell.ColorDefault
	}
	u.clipboardBackend = clipboard.New(cfg.Paths.StateDir).Backend()
	u.header = u.text()
	u.status = u.text()
	u.detail = u.text().SetWrap(true).SetScrollable(true)
	u.detail.SetBorder(true).SetTitle(" Details ")
	u.devices = u.list(" Devices ")
	u.transfers = u.list(" Transfers ")
	u.devices.SetChangedFunc(func(index int, _ string, _ string, _ rune) {
		if index >= 0 && index < len(u.visiblePeers) && u.pane == 0 {
			u.detail.SetText(peerText(u.visiblePeers[index]))
		}
	})
	u.devices.SetMouseCapture(func(a tview.MouseAction, e *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		u.mouseSelecting = a == tview.MouseLeftClick
		return a, e
	})
	u.devices.SetSelectedFunc(func(index int, _ string, _ string, _ rune) {
		u.devices.SetCurrentItem(index)
		if !u.mouseSelecting {
			u.deviceDetails()
		}
		u.mouseSelecting = false
	})
	u.transfers.SetChangedFunc(func(int, string, string, rune) { u.renderDetails() })
	u.transfers.SetSelectedFunc(func(index int, _ string, _ string, _ rune) { u.transfers.SetCurrentItem(index); u.transferDetails() })
	u.devices.SetFocusFunc(func() { u.pane = 0; u.focusStyle(); u.layout(u.width, u.height) })
	u.transfers.SetFocusFunc(func() { u.pane = 1; u.focusStyle(); u.layout(u.width, u.height) })
	u.body = tview.NewFlex()
	u.actions = tview.NewFlex()
	u.tabs = tview.NewFlex()
	u.tabButtons = []*tview.Button{
		u.button("Devices", func() { u.focus(0) }),
		u.button("Transfers", func() { u.history = false; u.rebuildTransfers(); u.focus(1) }),
		u.button("History", func() { u.history = true; u.rebuildTransfers(); u.focus(1) }),
		u.button("Details", func() {
			if u.pane == 0 {
				u.deviceDetails()
			} else {
				u.transferDetails()
			}
		}),
		u.button("Find /", u.search),
	}
	u.actionButtons = []*tview.Button{u.button("S Send", u.sendPicker), u.button("C Clip", u.sendClipboard), u.button("F Files", u.sendPicker), u.button("More", u.quickMenu), u.button("H History", u.toggleHistory), u.button("? Help", u.help), u.button("Q Quit", u.quit)}

	u.main = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.header, 2, 0, false).AddItem(u.tabs, 1, 0, false).AddItem(u.body, 0, 1, true).
		AddItem(u.status, 2, 0, false).AddItem(u.actions, 1, 0, false)
	u.pages = tview.NewPages().AddPage("main", u.main, true, true)
	_, noColor := os.LookupEnv("NO_COLOR")
	u.app.SetRoot(&terminalRoot{Primitive: u.pages, ascii: u.ascii, noColor: noColor}, true).EnableMouse(cfg.UI.Mouse).EnablePaste(true).SetFocus(u.devices)
	u.app.SetInputCapture(u.key)
	u.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, h := screen.Size()
		if w != u.width || h != u.height {
			u.layout(w, h)
		}
		return false
	})
	u.layout(80, 24)
	u.status.SetText(" Connecting to Relay daemon…\n Closing this terminal never stops your transfers.")
	u.apply(model.Snapshot{Name: cfg.Name, ReceiveDirectory: cfg.Receive.Directory, Tailscale: "waiting for daemon"})
	return u
}

func (u *ui) text() *tview.TextView {
	v := tview.NewTextView().SetDynamicColors(true).SetTextColor(u.foreground)
	v.SetBackgroundColor(u.background)
	return v
}
func (u *ui) list(title string) *tview.List {
	l := tview.NewList().ShowSecondaryText(true).SetMainTextColor(u.foreground).SetSecondaryTextColor(u.muted).
		SetSelectedTextColor(tcell.NewHexColor(0x10161e)).SetSelectedBackgroundColor(u.accent).SetHighlightFullLine(true)
	l.SetBackgroundColor(u.background)
	l.SetBorder(true).SetTitle(title).SetBorderColor(u.muted).SetTitleColor(u.foreground)
	if u.accent == tcell.ColorDefault {
		l.SetSelectedStyle(tcell.StyleDefault.Reverse(true))
	}
	return l
}
func (u *ui) button(label string, fn func()) *tview.Button {
	b := tview.NewButton(label).SetSelectedFunc(fn).SetStyle(tcell.StyleDefault.Foreground(u.foreground).Background(u.background)).
		SetActivatedStyle(tcell.StyleDefault.Foreground(u.accent).Reverse(true))
	b.SetBorder(false)
	b.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		// tview classifies rapid clicks globally, even across different controls.
		// Buttons have one action, so a second click should still activate it.
		if action == tview.MouseLeftDoubleClick {
			action = tview.MouseLeftClick
		}
		return action, event
	})
	return b
}
func (u *ui) focusStyle() {
	u.devices.SetBorderColor(u.muted)
	u.transfers.SetBorderColor(u.muted)
	if u.pane == 0 {
		u.devices.SetBorderColor(u.accent)
	} else {
		u.transfers.SetBorderColor(u.accent)
	}
}
func (u *ui) layout(w, h int) {
	u.width, u.height = w, h
	u.body.Clear()
	u.tabs.Clear()
	u.actions.Clear()
	if w < 72 {
		if u.pane == 0 {
			u.body.AddItem(u.devices, 0, 1, true)
		} else {
			u.body.AddItem(u.transfers, 0, 1, true)
		}
	} else {
		u.body.AddItem(u.devices, 26, 0, u.pane == 0).AddItem(u.transfers, 0, 1, u.pane == 1)
		if w >= 120 {
			u.body.AddItem(u.detail, 36, 0, false)
		}
	}
	// Preserve primitive identities across resizes, including a focused button.
	tabLabels := []string{"Devices", "Transfers", "History", "Details", "Find /"}
	actionLabels := []string{"S Send", "C Clip", "F Files", "More", "H History", "? Help", "Q Quit"}
	if w < 60 {
		tabLabels = []string{"Peers", "Moves", "Past", "Info", "Find"}
		actionLabels = []string{"Send", "Clip", "Files", "More", "Hist", "Help", "Quit"}
	}
	for i, button := range u.tabButtons {
		button.SetLabel(tabLabels[i])
		u.tabs.AddItem(button, 0, 1, false)
	}
	for i, button := range u.actionButtons {
		button.SetLabel(actionLabels[i])
	}
	u.actions.SetDirection(tview.FlexColumn)
	actionRows := 1
	if w < 42 {
		actionRows = 2
		u.actions.SetDirection(tview.FlexRow)
		first, second := tview.NewFlex(), tview.NewFlex()
		for i, button := range u.actionButtons {
			if i < 4 {
				first.AddItem(button, 0, 1, false)
			} else {
				second.AddItem(button, 0, 1, false)
			}
		}
		u.actions.AddItem(first, 1, 0, false).AddItem(second, 1, 0, false)
	} else {
		for _, button := range u.actionButtons {
			u.actions.AddItem(button, 0, 1, false)
		}
	}
	u.main.ResizeItem(u.actions, actionRows, 0)
	headerRows, statusRows := 2, 2
	if h < 14 {
		headerRows, statusRows = 1, 1
	}
	u.main.ResizeItem(u.header, headerRows, 0).ResizeItem(u.status, statusRows, 0)
	u.devices.ShowSecondaryText(h >= 12)
	u.transfers.ShowSecondaryText(h >= 12)
	u.devices.SetBorder(h >= 10)
	u.transfers.SetBorder(h >= 10)
	if u.browser != nil {
		u.browser.layout(min(100, max(1, w-2)), min(32, max(1, h-2)))
	}

	u.focusStyle()
}
func (u *ui) focus(pane int) {
	u.pane = pane
	if pane == 0 {
		u.app.SetFocus(u.devices)
	} else {
		u.app.SetFocus(u.transfers)
	}
	u.layout(u.width, u.height)
	u.renderDetails()
}
func (u *ui) quit() { u.cancel(); u.app.Stop() }

// post coalesces wakeups into a single reserved key event. Worker goroutines
// never wait on QueueUpdateDraw after shutdown, and only the UI loop mutates UI.
func (u *ui) post(fn func()) {
	select {
	case <-u.ctx.Done():
		return
	case u.updates <- fn:
	}
	if u.scheduled.CompareAndSwap(false, true) {
		u.app.QueueEvent(tcell.NewEventKey(tcell.KeyF24, 0, tcell.ModCtrl|tcell.ModAlt))
	}
}
func (u *ui) drain() {
	for {
		select {
		case fn := <-u.updates:
			fn()
		default:
			u.scheduled.Store(false)
			if len(u.updates) > 0 && u.scheduled.CompareAndSwap(false, true) {
				continue
			}
			return
		}
	}
}

// Superseded snapshots are dropped before rendering. Transfer progress cannot
// accumulate stale full-state copies if a terminal is slow.
func (u *ui) postSnapshot(snapshot model.Snapshot) {
	u.snapshotMu.Lock()
	u.latestSnapshot = &snapshot
	if u.snapshotQueued {
		u.snapshotMu.Unlock()
		return
	}
	u.snapshotQueued = true
	u.snapshotMu.Unlock()
	u.post(func() {
		u.snapshotMu.Lock()
		latest := u.latestSnapshot
		u.latestSnapshot = nil
		u.snapshotQueued = false
		u.snapshotMu.Unlock()
		if latest != nil {
			u.connected = true
			u.apply(*latest)
			u.presentOffer()
		}
	})
}
func (u *ui) watch() {
	retry := time.Second
	for u.ctx.Err() == nil {
		err := u.client.Watch(u.ctx, func(s model.Snapshot) { retry = time.Second; u.postSnapshot(s) })
		if u.ctx.Err() != nil {
			return
		}
		u.post(func() {
			u.connected = false
			u.header.SetText(" RELAY  ·  daemon disconnected")
			u.status.SetText(" " + safe(fmt.Sprint(err)) + "\n Run relay daemon or relay service start. Reconnecting automatically…")
		})
		timer := time.NewTimer(retry)
		select {
		case <-u.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		retry = min(retry*2, 10*time.Second)
	}
}
func (u *ui) async(label string, fn func(context.Context) (model.Result, error), done func(model.Result)) {
	select {
	case u.workSlots <- struct{}{}:
	default:
		u.status.SetText(" Relay is finishing earlier requests. Try again in a moment.")
		return
	}
	u.status.SetText(" " + safe(label) + "…")
	go func() {
		defer func() { <-u.workSlots }()
		ctx, cancel := context.WithTimeout(u.ctx, 30*time.Second)
		defer cancel()
		r, err := fn(ctx)
		u.post(func() {
			if err != nil {
				u.status.SetText(" " + safe(err.Error()) + "\n ? Help · R refresh · transfer details show recovery actions")
				return
			}
			u.status.SetText(" " + safe(r.Message))
			if done != nil {
				done(r)
			}
		})
	}()
}
func (u *ui) action(a model.Action) {
	u.async(a.Action, func(ctx context.Context) (model.Result, error) { return u.client.Action(ctx, a) }, nil)
}
func (u *ui) apply(s model.Snapshot) {
	u.snapshot = s
	offeredIDs := make(map[string]bool)
	for _, t := range s.Transfers {
		if t.Status == "offered" {
			offeredIDs[t.ID] = true
		}
	}
	for id := range u.seenOffers {
		if !offeredIDs[id] {
			delete(u.seenOffers, id)
		}
	}
	if u.transferDetail != nil {
		for _, t := range s.Transfers {
			if t.ID == u.detailID {
				u.transferDetail.SetText(transferText(t, u.ascii))
				break
			}
		}
	}
	address := ""
	if s.Address != "" {
		address = " · " + s.Address
	}
	u.header.SetText(" RELAY  /  " + safe(s.Name) + "\n " + safe(s.Tailscale+address))
	u.rebuildPeers()
	u.rebuildTransfers()
	u.renderDetails()
	offered := 0
	active := 0
	for _, t := range s.Transfers {
		if t.Status == "offered" {
			offered++
		}
		if !t.Terminal() {
			active++
		}
	}
	if u.connected {
		note := fmt.Sprintf(" %d active · %d waiting for approval · %s", active, offered, u.clipboardBackend)
		if offered > 0 {
			note += "\n Transfers → Enter to accept or reject"
		} else if len(s.Notices) > 0 {
			note += "\n " + safe(s.Notices[0])
		} else {
			note += "\n Inbox: " + safe(s.ReceiveDirectory)
		}
		u.status.SetText(note)
	}
}
func (u *ui) selectedPeer() (model.Peer, bool) {
	i := u.devices.GetCurrentItem()
	if i >= 0 && i < len(u.visiblePeers) {
		return u.visiblePeers[i], true
	}
	return model.Peer{}, false
}
func (u *ui) selectedTransfer() (model.Transfer, bool) {
	i := u.transfers.GetCurrentItem()
	if i >= 0 && i < len(u.visibleTransfers) {
		return u.visibleTransfers[i], true
	}
	return model.Transfer{}, false
}
func (u *ui) rebuildPeers() {
	old, _ := u.selectedPeer()
	index := u.devices.GetCurrentItem()
	u.visiblePeers = nil
	u.devices.Clear()
	for _, p := range u.snapshot.Peers {
		if !matches(p.Name+" "+p.Hostname+" "+p.Address, u.query[0]) {
			continue
		}
		u.visiblePeers = append(u.visiblePeers, p)
		symbol := "○"
		state := "offline"
		if p.Online {
			state = "no Relay"
			symbol = "●"
		}
		if p.Relay {
			state = "ready"
		}
		badge := ""
		if p.Trusted {
			badge = " +"
			if !u.ascii {
				badge = " ✓"
			}
		}
		if u.ascii {
			symbol = "o"
			if p.Online {
				symbol = "*"
			}
		}
		u.devices.AddItem(symbol+" "+safe(p.Name)+badge, safe(p.Address+" · "+state), 0, nil)
		if p.ID == old.ID {
			index = len(u.visiblePeers) - 1
		}
	}
	if len(u.visiblePeers) == 0 {
		u.devices.AddItem("No devices yet", "Run Relay on another Tailnet device", 0, nil)
	} else {
		u.devices.SetCurrentItem(min(max(index, 0), len(u.visiblePeers)-1))
	}
	title := " Devices "
	if u.query[0] != "" {
		title = " Devices / " + safe(u.query[0]) + " "
	}
	u.devices.SetTitle(title)
}
func (u *ui) rebuildTransfers() {
	old, _ := u.selectedTransfer()
	index := u.transfers.GetCurrentItem()
	u.visibleTransfers = nil
	u.transfers.Clear()
	source := u.snapshot.Transfers
	title := " Transfers "
	recent := make(map[string]bool)
	if u.history {
		source = u.snapshot.History
		title = " History "
	} else {
		// Completed jobs move to History immediately. Keep recent successes
		// visible so a fast transfer does not disappear before the next draw.
		source = append([]model.Transfer(nil), source...)
		seen := make(map[string]bool, len(source))
		for _, t := range source {
			seen[t.ID] = true
		}
		for _, t := range u.snapshot.History {
			if t.Status != "completed" || seen[t.ID] {
				continue
			}
			source = append(source, t)
			recent[t.ID] = true
			seen[t.ID] = true
			if len(recent) == 5 {
				break
			}
		}
	}
	for _, t := range source {
		if !matches(t.Name+" "+t.Peer+" "+t.Status, u.query[1]) {
			continue
		}
		u.visibleTransfers = append(u.visibleTransfers, t)
		indicator := t.Status
		if t.Status == "completed" && t.Verified {
			indicator = "verified"
		}
		line := safe(t.Name) + "  ·  " + safe(indicator)
		sub := safe(t.Peer) + " · " + bytes(t.Total)
		if recent[t.ID] {
			sub = "Recent · " + sub
		}
		if t.Status == "transferring" {
			sub = progress(t.Bytes, t.Total, 12, u.ascii) + "  " + bytes(int64(t.Speed)) + "/s"
		}
		if t.Status == "offered" {
			sub = "Approval needed · Enter / click for actions"
		}
		u.transfers.AddItem(line, sub, 0, nil)
		if t.ID == old.ID {
			index = len(u.visibleTransfers) - 1
		}
	}
	if len(u.visibleTransfers) == 0 {
		text := "Your next transfer starts here"
		if u.history {
			text = "Completed transfers appear here"
		}
		u.transfers.AddItem(text, "Select a device, then S to send", 0, nil)
	} else {
		u.transfers.SetCurrentItem(min(max(index, 0), len(u.visibleTransfers)-1))
	}
	if u.query[1] != "" {
		title = strings.TrimSpace(title) + " / " + safe(u.query[1])
	}
	u.transfers.SetTitle(title)
}
func (u *ui) toggleHistory() { u.history = !u.history; u.rebuildTransfers(); u.focus(1) }
func (u *ui) renderDetails() {
	if u.pane == 0 {
		if p, ok := u.selectedPeer(); ok {
			u.detail.SetText(peerText(p))
			return
		}
	}
	if t, ok := u.selectedTransfer(); ok {
		u.detail.SetText(transferText(t, u.ascii))
		return
	}
	instruction := "Select a device and press D to compare identity fingerprints, then trust it on both devices."
	if u.snapshot.TrustMode == "tailnet" {
		instruction = "Devices connect through Tailscale automatically. Pairing is not required. Select a device to send; D opens access controls."
	}
	u.detail.SetText("\n  Your Tailnet. Your files.\n\n  Start relay daemon on your other devices. Relay discovers them automatically.\n\n  " + instruction + "\n\n  S  send files\n  C  send clipboard\n  ?  all controls")
}
func peerText(p model.Peer) string {
	trust := "Not trusted"
	if p.Trusted {
		trust = "Trusted"
	}
	if p.Blocked {
		trust = "Blocked"
	}
	lastSeen := "not observed"
	if !p.LastSeen.IsZero() {
		lastSeen = p.LastSeen.Local().Format("Jan 02 15:04:05")
	}
	latency := "not measured"
	if p.LatencyMS > 0 {
		latency = fmt.Sprintf("%d ms", p.LatencyMS)
	}
	return fmt.Sprintf("\n %s\n %s\n\n %s · %s\n %s\n Relay %s · protocol %d\n Latency %s\n Last seen %s\n\n %s\n\n Identity fingerprint\n %s\n\n Capabilities\n %s\n\n %s", safe(p.Name), safe(p.Address), safe(p.OS), safe(p.Arch), safe(p.Hostname), safe(p.Version), p.Protocol, latency, lastSeen, trust, safe(p.Fingerprint), safe(strings.Join(p.Capabilities, ", ")), safe(p.Error))
}
func transferText(t model.Transfer, ascii bool) string {
	verified := "Awaiting integrity verification"
	if t.Verified {
		verified = "Integrity verified (SHA-256)"
	}
	arrow := "←"
	if t.Direction == "send" || t.Direction == "outgoing" {
		arrow = "→"
	}
	if ascii {
		arrow = "<->"
	}
	elapsed := "—"
	if !t.Started.IsZero() {
		end := time.Now()
		if t.Terminal() && !t.Updated.IsZero() {
			end = t.Updated
		}
		elapsed = duration(end.Sub(t.Started).Seconds())
	}
	return fmt.Sprintf("\n %s\n %s %s\n\n %s\n %s\n\n %s / %s\n Current %s/s · average %s/s\n ETA %s · retry %d\n Elapsed %s\n\n %s\n\n %s\n\n %s", safe(t.Name), arrow, safe(t.Peer), safe(t.Status), progress(t.Bytes, t.Total, 20, ascii), bytes(t.Bytes), bytes(t.Total), bytes(int64(t.Speed)), bytes(int64(t.AverageSpeed)), duration(t.ETASeconds), t.Retry, elapsed, verified, safe(t.Destination), safe(t.Error))
}
