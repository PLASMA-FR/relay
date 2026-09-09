package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (u *ui) key(e *tcell.EventKey) *tcell.EventKey {
	if e.Key() == tcell.KeyF24 && e.Modifiers() == tcell.ModCtrl|tcell.ModAlt {
		u.drain()
		return nil
	}
	u.mouseSelecting = false
	if e.Key() == tcell.KeyCtrlC {
		u.quit()
		return nil
	}
	if u.modal {
		if e.Key() == tcell.KeyEscape {
			u.closeModal()
			return nil
		}
		return e
	}
	switch e.Key() {
	case tcell.KeyTab:
		u.cycleFocus(false)
		return nil
	case tcell.KeyBacktab:
		u.cycleFocus(true)
		return nil
	case tcell.KeyLeft:
		u.focus(0)
		return nil
	case tcell.KeyRight:
		u.focus(1)
		return nil
	case tcell.KeyEscape:
		if u.query[u.pane] != "" {
			u.query[u.pane] = ""
			u.rebuildPeers()
			u.rebuildTransfers()
		}
		return nil
	}
	switch e.Rune() {
	case 'q', 'Q':
		u.quit()
		return nil
	case '?':
		u.help()
		return nil
	case 's', 'S', 'f', 'F':
		u.sendPicker()
		return nil
	case 'c', 'C':
		u.sendClipboard()
		return nil
	case 'H':
		u.toggleHistory()
		return nil
	case 'd', 'D':
		if u.pane == 0 {
			u.deviceDetails()
		} else {
			u.transferDetails()
		}
		return nil
	case 'r', 'R':
		if u.pane == 1 {
			u.transferAction("resume")
		} else {
			u.action(model.Action{Action: "refresh"})
		}
		return nil
	case 'p', 'P':
		u.transferAction("pause")
		return nil
	case 'x', 'X':
		u.transferAction("cancel")
		return nil
	case 'a', 'A':
		u.transferAction("accept")
		return nil
	case '/':
		u.search()
		return nil
	case ' ':
		if u.pane == 0 {
			u.quickMenu()
		} else {
			u.transferDetails()
		}
		return nil
	}
	if u.cfg.UI.VimKeys {
		switch e.Rune() {
		case 'h':
			u.focus(0)
			return nil
		case 'l':
			u.focus(1)
			return nil
		case 'j':
			return tcell.NewEventKey(tcell.KeyDown, 0, e.Modifiers())
		case 'k':
			return tcell.NewEventKey(tcell.KeyUp, 0, e.Modifiers())
		case 'g':
			if u.pane == 0 {
				u.devices.SetCurrentItem(0)
			} else {
				u.transfers.SetCurrentItem(0)
			}
			return nil
		case 'G':
			if u.pane == 0 {
				u.devices.SetCurrentItem(max(0, u.devices.GetItemCount()-1))
			} else {
				u.transfers.SetCurrentItem(max(0, u.transfers.GetItemCount()-1))
			}
			return nil
		}
	}
	return e
}
func (u *ui) cycleFocus(back bool) {
	// Every clickable tab and footer action is also reachable through Tab.
	items := []tview.Primitive{u.devices, u.transfers}
	for _, button := range u.tabButtons {
		items = append(items, button)
	}
	for _, button := range u.actionButtons {
		items = append(items, button)
	}
	current := u.app.GetFocus()
	next := 0
	for i, p := range items {
		if p == current {
			next = i + 1
			if back {
				next = i - 1
			}
			break
		}
	}
	next = (next + len(items)) % len(items)
	u.app.SetFocus(items[next])
}

type popup struct {
	*tview.Box
	child tview.Primitive
	w, h  int
}

func (p *popup) Draw(s tcell.Screen) {
	x, y, w, h := p.GetRect()
	cw, ch := min(p.w, max(1, w-2)), min(p.h, max(1, h-2))
	p.child.SetRect(x+(w-cw)/2, y+(h-ch)/2, cw, ch)
	p.child.Draw(s)
}
func (p *popup) Focus(f func(tview.Primitive)) { f(p.child) }
func (p *popup) HasFocus() bool                { return p.child.HasFocus() }
func (p *popup) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return p.child.InputHandler()
}
func (p *popup) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, event *tcell.EventMouse, focus func(tview.Primitive)) (bool, tview.Primitive) {
		if handler := p.child.MouseHandler(); handler != nil {
			_, capture := handler(action, event, focus)
			return true, capture
		}
		return true, nil
	}
}
func (p *popup) PasteHandler() func(string, func(tview.Primitive)) { return p.child.PasteHandler() }
func (u *ui) showModal(p tview.Primitive, w, h int) {
	u.modal = true
	u.modalFocus = p
	u.pages.AddPage("dialog", &popup{Box: tview.NewBox(), child: p, w: w, h: h}, true, true)
	u.app.SetFocus(p)
}
func (u *ui) closeModal() {
	u.pages.RemovePage("dialog")
	u.modal = false
	u.modalFocus = nil
	u.transferDetail = nil
	u.detailID = ""
	if u.browser != nil && u.browser.cancel != nil {
		u.browser.cancel()
	}
	u.browser = nil
	u.focus(u.pane)
}
func (u *ui) message(title, text string) {
	content := u.text().SetText(text).SetWrap(true).SetScrollable(true)
	content.SetBorder(true).SetTitle(" " + safe(title) + " ")
	done := u.button("Close · Esc", u.closeModal)
	flex := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(content, 0, 1, true).AddItem(done, 1, 0, false)
	flex.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
			if content.HasFocus() {
				u.app.SetFocus(done)
			} else {
				u.app.SetFocus(content)
			}
			return nil
		}
		return e
	})
	u.showModal(flex, 76, 24)
}
func (u *ui) help() {
	u.message("Relay · keyboard and mouse", `
 YOUR TAILNET. YOUR FILES.

 S / F     Select files or directories for the selected device
 C         Send system clipboard (Relay Clipboard on headless hosts)
 Space     All sending actions: files, text, URL, clipboard
 D / Enter Device identity or transfer details and actions
 H         History / active transfers
 /         Incremental fuzzy search in the current pane
 R         Refresh devices / resume selected transfer
 P / X     Pause / cancel selected transfer
 A         Accept selected incoming offer

 Arrows    Navigate rows; left/right switch panes
 j/k h/l   Vim navigation (when enabled); g/G first/last
 Tab       Cycle panes, tabs, and action buttons
 Esc       Close a dialog or clear search
 Q/Ctrl-C  Close the TUI; your daemon keeps working

 FILE PICKER
 Space / click checkbox   Select files or whole directories
 Enter / click name       Open directory or toggle file
 Backspace                Parent directory
 / filter  . hidden  O sort  ~ home  Ctrl-L path  S send
 Selection survives directory navigation. Escape closes picker.

 Pairing: compare the FULL fingerprint with relay status on the
 other device, then trust. Repeat on both devices.

 Mouse: click rows, tabs, checkboxes, dialogs, and buttons;
 wheel scrolls lists. Native terminal reporting works over SSH
 and tmux. Hold your terminal's selection modifier to select text.
 Incoming text stays in Relay Clipboard; URLs are never launched.`)
}
func (u *ui) search() {
	pane := u.pane
	field := tview.NewInputField().SetLabel(" Find: ").SetText(u.query[pane]).SetFieldBackgroundColor(u.background).SetFieldTextColor(u.foreground)
	field.SetBorder(true).SetTitle(" Fuzzy search ")
	field.SetChangedFunc(func(s string) {
		u.query[pane] = s
		if pane == 0 {
			u.rebuildPeers()
		} else {
			u.rebuildTransfers()
		}
	})
	field.SetDoneFunc(func(tcell.Key) { u.closeModal() })
	done := u.button("Done", u.closeModal)
	flex := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(field, 3, 0, true).AddItem(done, 1, 0, false)
	flex.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
			if field.HasFocus() {
				u.app.SetFocus(done)
			} else {
				u.app.SetFocus(field)
			}
			return nil
		}
		return e
	})
	u.showModal(flex, 66, 4)
}
func (u *ui) requirePeer() (model.Peer, bool) {
	if !u.connected {
		u.message("Daemon disconnected", "\n Relay is reconnecting automatically.\n\n Start the background daemon with relay daemon or relay service start.\n\n Run relay doctor if the connection does not return.")
		return model.Peer{}, false
	}
	p, ok := u.selectedPeer()
	if !ok {
		u.message("Discover your devices", "\n Start relay daemon on another Tailscale device, then press R.\n\n Relay discovers running peers automatically.\n\n Run relay doctor for connectivity diagnostics.")
		return p, false
	}
	// The daemon can queue for a previously verified device while it is
	// unreachable. An advertised fingerprint alone never establishes trust.
	if !p.Relay && (!p.Trusted || p.Fingerprint == "") {
		u.message("Relay is not reachable", "\n "+safe(p.Name)+" is not running a reachable Relay daemon.\n\n Start relay daemon there and allow Tailnet TCP port 7331.\n\n Press R to refresh.")
		return p, false
	}
	if !p.Trusted {
		u.deviceDetails()
		return p, false
	}
	return p, true
}
func (u *ui) sendPicker() {
	p, ok := u.requirePeer()
	if !ok {
		return
	}
	u.openBrowser(p)
}
func (u *ui) send(req model.SendRequest) {
	// A picker or composer may stay open across discovery and trust updates.
	// Recheck its target before submitting; the daemon also enforces the pin.
	allowed := false
	for _, p := range u.snapshot.Peers {
		if p.ID == req.Peer {
			allowed = u.connected && p.Trusted && (p.Relay || p.Fingerprint != "")
			break
		}
	}
	if !allowed {
		u.closeModal()
		message := "\n The daemon connection or this device's trust changed while composing.\n\n Close this dialog, select the device, and try again."
		if u.snapshot.TrustMode == "tailnet" {
			message = "\n The daemon connection or this device's access changed while composing.\n\n Close this dialog, select the device, and try again."
		}
		if u.snapshot.TrustMode != "tailnet" {
			message += "\n\n Compare its full fingerprint before granting trust to a changed identity."
		}
		u.message("Check the sending device", message)
		return
	}
	u.closeModal()
	u.async("Preparing send", func(ctx context.Context) (model.Result, error) { return u.client.Send(ctx, req) }, func(model.Result) { u.history = false; u.focus(1) })
}
func (u *ui) sendClipboard() {
	p, ok := u.requirePeer()
	if !ok {
		return
	}
	u.async("Reading clipboard", func(ctx context.Context) (model.Result, error) {
		m := clipboard.New(u.cfg.Paths.StateDir)
		m.FallbackInternal = u.cfg.Clipboard.FallbackInternal
		text, err := m.Read(ctx)
		if err != nil {
			return model.Result{}, err
		}
		if text == "" {
			return model.Result{}, fmt.Errorf("clipboard is empty; use Space → Text to compose a message")
		}
		return u.client.Send(ctx, model.SendRequest{Peer: p.ID, Kind: "clipboard", Text: text})
	}, func(model.Result) { u.history = false; u.focus(1) })
}
func (u *ui) quickMenu() {
	p, ok := u.requirePeer()
	if !ok {
		return
	}
	list := u.list(" Send to " + safe(p.Name) + " ").ShowSecondaryText(true)
	list.AddItem("Files and directories", "Choose multiple paths; S is the direct shortcut", 'f', func() { u.openBrowser(p) })
	list.AddItem("Clipboard", "System clipboard, with a headless fallback", 'c', func() { u.closeModal(); u.sendClipboard() })
	list.AddItem("Text", "Compose or paste development context", 't', func() { u.compose(p, "text") })
	list.AddItem("URL", "Deliver a link; never launch it remotely", 'u', func() { u.compose(p, "url") })
	list.AddItem("Cancel", "Esc", 0, u.closeModal)
	u.showModal(list, 64, 14)
}
func (u *ui) compose(p model.Peer, kind string) {
	text := ""
	form := tview.NewForm().SetFieldBackgroundColor(u.background).SetFieldTextColor(u.foreground).SetLabelColor(u.accent).SetButtonsAlign(tview.AlignRight)
	form.SetBackgroundColor(u.background)
	form.SetBorder(true).SetTitle(" " + safe(strings.ToUpper(kind)) + " to " + safe(p.Name) + " ")
	if kind == "url" {
		form.AddInputField("URL", "", 0, nil, func(s string) { text = s })
	} else {
		form.AddTextArea("Text", "", 0, 7, 1<<20, func(s string) { text = s })
	}
	form.AddButton("Send", func() {
		if strings.TrimSpace(text) == "" {
			return
		}
		u.send(model.SendRequest{Peer: p.ID, Kind: kind, Text: text})
	}).AddButton("Cancel", u.closeModal)
	u.showModal(form, 74, 15)
}
func (u *ui) deviceDetails() {
	p, ok := u.selectedPeer()
	if !ok {
		u.message("This device", "\n "+safe(u.snapshot.Name)+"\n\n Identity fingerprint\n "+safe(u.snapshot.Fingerprint)+"\n\n Run Relay on another Tailnet device to begin.")
		return
	}
	tailnet := u.snapshot.TrustMode == "tailnet"
	explanation := "\n\n Compare the full fingerprint with relay status on that device. Trust must be granted on BOTH devices."
	if tailnet {
		explanation = "\n\n Access follows your Tailscale network. Pairing is not required. Block a device here to stop its access."
	}
	text := u.text().SetText(peerText(p) + explanation).SetWrap(true).SetScrollable(true)
	text.SetBorder(true).SetTitle(" Device identity ")
	label := "Compare & trust"
	if p.Trusted {
		label = "Revoke trust"
	}
	if tailnet {
		label = "Block device"
		if p.Blocked {
			label = "Unblock device"
		}
	}
	button := u.button(label, func() {
		if tailnet {
			u.closeModal()
			action := "untrust"
			if p.Blocked {
				action = "trust"
			}
			u.action(model.Action{Action: action, Peer: p.ID})
			return
		}
		if p.Trusted {
			u.closeModal()
			u.action(model.Action{Action: "untrust", Peer: p.ID})
			return
		}
		u.confirmTrust(p)
	})
	closeButton := u.button("Close", u.closeModal)
	controls := []tview.Primitive{text, button, closeButton}
	buttons := tview.NewFlex().AddItem(button, 0, 1, true).AddItem(closeButton, 0, 1, false)
	flex := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(text, 0, 1, true).AddItem(buttons, 1, 0, false)
	flex.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyEnter && text.HasFocus() {
			u.app.SetFocus(button)
			return nil
		}
		if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
			idx := 0
			for i, c := range controls {
				if c.HasFocus() {
					idx = i
					break
				}
			}
			delta := 1
			if e.Key() == tcell.KeyBacktab {
				delta = -1
			}
			u.app.SetFocus(controls[(idx+delta+len(controls))%len(controls)])
			return nil
		}
		return e
	})
	u.showModal(flex, 76, 25)
}
func (u *ui) confirmTrust(p model.Peer) {
	if p.Fingerprint == "" {
		u.async("Reading peer identity", func(ctx context.Context) (model.Result, error) {
			return u.client.Action(ctx, model.Action{Action: "pair", Peer: p.ID})
		}, func(r model.Result) {
			if r.Peer != nil {
				u.confirmTrust(*r.Peer)
			}
		})
		return
	}

	text := u.text().SetText(" Compare this full fingerprint with relay status on " + safe(p.Name) + ".\n Check the box only if both fingerprints match.\n\n " + safe(p.Fingerprint) + "\n\n Trust is directional: repeat on the other device.").SetWrap(true).SetScrollable(true)
	text.SetBorder(true).SetTitle(" Verify identity ")
	prompt := &trustPrompt{Flex: tview.NewFlex().SetDirection(tview.FlexRow)}
	prompt.check = tview.NewCheckbox().SetLabel(" Match confirmed ").SetFieldBackgroundColor(u.background).SetFieldTextColor(u.accent).SetLabelColor(u.foreground)
	prompt.trust = u.button("Trust", func() {
		if !prompt.check.IsChecked() {
			return
		}
		u.closeModal()
		u.action(model.Action{Action: "trust", Peer: p.ID, Fingerprint: p.Fingerprint})
	})
	prompt.trust.SetDisabled(true)
	prompt.check.SetChangedFunc(func(v bool) { prompt.trust.SetDisabled(!v) })
	close := u.button("Cancel", u.closeModal)
	controls := []tview.Primitive{text, prompt.check, prompt.trust, close}
	prompt.AddItem(text, 0, 1, true).AddItem(prompt.check, 1, 0, false).AddItem(tview.NewFlex().AddItem(prompt.trust, 0, 1, false).AddItem(close, 0, 1, false), 1, 0, false)
	prompt.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
			idx := 0
			for i, c := range controls {
				if c.HasFocus() {
					idx = i
					break
				}
			}
			delta := 1
			if e.Key() == tcell.KeyBacktab {
				delta = -1
			}
			u.app.SetFocus(controls[(idx+delta+len(controls))%len(controls)])
			return nil
		}
		return e
	})
	u.showModal(prompt, 78, 17)
	if u.width >= 72 && u.height >= 20 {
		u.app.SetFocus(prompt.check)
	}
}

type trustPrompt struct {
	*tview.Flex
	check *tview.Checkbox
	trust *tview.Button
}

func (u *ui) transferAction(action string) {
	if u.pane != 1 {
		u.status.SetText(" Select a transfer first with Right or the Transfers tab.")
		return
	}
	t, ok := u.selectedTransfer()
	if !ok {
		return
	}
	u.action(model.Action{Action: action, ID: t.ID})
}
func (u *ui) transferDetails() {
	t, ok := u.selectedTransfer()
	if !ok {
		return
	}
	text := u.text().SetText(transferText(t, u.ascii)).SetWrap(true).SetScrollable(true)
	text.SetBorder(true).SetTitle(" Transfer · " + safe(t.ID) + " ")
	buttons := tview.NewFlex()
	controls := []tview.Primitive{text}
	add := func(label, action string) {
		b := u.button(label, func() { u.closeModal(); u.action(model.Action{Action: action, ID: t.ID}) })
		buttons.AddItem(b, 0, 1, false)
		controls = append(controls, b)
	}
	if t.Status == "offered" {
		add("A Accept", "accept")
		add("Reject", "reject")
	} else if !t.Terminal() {
		if t.Status == "paused" || t.Status == "interrupted" {
			add("R Resume", "resume")
		} else {
			add("P Pause", "pause")
		}
		add("X Cancel", "cancel")
	} else if t.Status == "failed" {
		add("R Retry", "resume")
	}
	close := u.button("Close", u.closeModal)
	buttons.AddItem(close, 0, 1, false)
	controls = append(controls, close)
	flex := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(text, 0, 1, true).AddItem(buttons, 1, 0, false)
	flex.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
			idx := 0
			for i, c := range controls {
				if c.HasFocus() {
					idx = i
					break
				}
			}
			delta := 1
			if e.Key() == tcell.KeyBacktab {
				delta = -1
			}
			u.app.SetFocus(controls[(idx+delta+len(controls))%len(controls)])
			return nil
		}
		if t.Status == "offered" {
			switch e.Rune() {
			case 'a', 'A':
				u.closeModal()
				u.action(model.Action{Action: "accept", ID: t.ID})
				return nil
			case 'r', 'R':
				u.closeModal()
				u.action(model.Action{Action: "reject", ID: t.ID})
				return nil
			}
		}
		action := ""
		if t.Status != "offered" {
			switch e.Rune() {
			case 'p', 'P':
				if !t.Terminal() && t.Status != "paused" && t.Status != "interrupted" {
					action = "pause"
				}
			case 'r', 'R':
				if t.Status == "paused" || t.Status == "interrupted" || t.Status == "failed" {
					action = "resume"
				}
			case 'x', 'X':
				if !t.Terminal() {
					action = "cancel"
				}
			}
		}
		if action != "" {
			u.closeModal()
			u.action(model.Action{Action: action, ID: t.ID})
			return nil
		}

		return e
	})
	u.showModal(flex, 78, 26)
	u.transferDetail = text
	u.detailID = t.ID
	if t.Status == "offered" && len(controls) > 1 {
		u.app.SetFocus(controls[1])
	}
}

// Present each incoming offer once, without interrupting an existing dialog.
func (u *ui) presentOffer() {
	if u.modal {
		return
	}
	for _, t := range u.snapshot.Transfers {
		if t.Status == "offered" && !u.seenOffers[t.ID] {
			u.seenOffers[t.ID] = true
			u.history = false
			u.query[1] = ""
			u.rebuildTransfers()
			u.focus(1)
			for i, v := range u.visibleTransfers {
				if v.ID == t.ID {
					u.transfers.SetCurrentItem(i)
					break
				}
			}
			u.transferDetails()
			return
		}
	}
}
