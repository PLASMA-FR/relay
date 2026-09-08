package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type fileEntry struct {
	name, path   string
	dir, symlink bool
	size         int64
	modified     time.Time
}
type browser struct {
	cancel           context.CancelFunc
	u                *ui
	peer             model.Peer
	root             *tview.Flex
	table            *tview.Table
	path, filter     *tview.InputField
	goPath           *tview.Button
	summary          *tview.TextView
	buttons          *tview.Flex
	entries, visible []fileEntry
	selected         map[string]fileEntry
	cwd, query       string
	hidden           bool
	order            int
	generation       uint64
}

func readDirectory(ctx context.Context, path string) ([]fileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var result []fileEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := f.ReadDir(256)
		for _, d := range entries {
			if len(result) >= 50000 {
				return nil, fmt.Errorf("directory has over 50,000 entries; enter a more specific path")
			}
			info, e := d.Info()
			if e != nil {
				continue
			}
			result = append(result, fileEntry{name: d.Name(), path: filepath.Join(path, d.Name()), dir: d.IsDir(), symlink: d.Type()&os.ModeSymlink != 0, size: info.Size(), modified: info.ModTime()})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (u *ui) openBrowser(peer model.Peer) {
	b := &browser{u: u, peer: peer, selected: map[string]fileEntry{}}
	b.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetBorders(false).SetSeparator(' ').SetSelectedStyle(tcell.StyleDefault.Foreground(u.accent).Reverse(true))
	b.table.SetBorder(true).SetTitle(" Space select · Enter open · S send ").SetBorderColor(u.accent).SetBackgroundColor(u.background)
	b.path = tview.NewInputField().SetLabel(" Path  ").SetFieldBackgroundColor(u.background).SetFieldTextColor(u.foreground).SetLabelColor(u.accent)
	b.filter = tview.NewInputField().SetLabel(" Find  ").SetFieldBackgroundColor(u.background).SetFieldTextColor(u.foreground).SetLabelColor(u.accent).SetPlaceholder("/ fuzzy filter")
	b.goPath = u.button("Go", func() { b.load(config.ExpandPath(b.path.GetText())); u.app.SetFocus(b.table) })
	pathBar := tview.NewFlex().AddItem(b.path, 0, 1, false).AddItem(b.goPath, 4, 0, false)
	b.summary = u.text()
	b.buttons = tview.NewFlex()
	b.buttons.AddItem(u.button("Send", b.send), 0, 1, false).
		AddItem(u.button("Hidden", func() { b.hidden = !b.hidden; b.render() }), 0, 1, false).
		AddItem(u.button("Sort", func() { b.order = (b.order + 1) % 3; b.render() }), 0, 1, false).
		AddItem(u.button("Home", func() { home, _ := os.UserHomeDir(); b.load(home) }), 0, 1, false).
		AddItem(u.button("Cancel", u.closeModal), 0, 1, false)
	b.root = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(pathBar, 1, 0, false).AddItem(b.filter, 1, 0, false).
		AddItem(b.table, 0, 1, true).AddItem(b.summary, 2, 0, false).AddItem(b.buttons, 1, 0, false)
	b.root.SetBorder(true).SetTitle(" Send to " + safe(peer.Name) + " ").SetBackgroundColor(u.background)
	b.root.SetInputCapture(b.key)
	b.path.SetDoneFunc(func(k tcell.Key) {
		if k == tcell.KeyEnter {
			b.load(config.ExpandPath(b.path.GetText()))
			u.app.SetFocus(b.table)
		}
	})
	b.filter.SetChangedFunc(func(q string) { b.query = q; b.render() }).SetDoneFunc(func(tcell.Key) { u.app.SetFocus(b.table) })
	b.table.SetSelectedFunc(func(row, col int) { b.open(row) })
	u.browser = b
	u.showModal(b.root, 100, 32)
	b.layout(min(100, max(1, u.width-2)), min(32, max(1, u.height-2)))
	dir := u.lastDir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	b.load(dir)
}
func (b *browser) layout(w, h int) {
	summaryRows := 2
	if h < 16 {
		summaryRows = 1
	}
	b.root.ResizeItem(b.summary, summaryRows, 0)
	b.root.SetBorder(h >= 10)
	b.table.SetBorder(h >= 14)
	labels := []string{"Send", "Hidden", "Sort", "Home", "Cancel"}
	if w < 30 {
		labels = []string{"Send", "Dot", "Sort", "Home", "Back"}
	}
	for i, label := range labels {
		b.buttons.GetItem(i).(*tview.Button).SetLabel(label)
	}
}
func (b *browser) load(path string) {
	select {
	case b.u.workSlots <- struct{}{}:
	default:
		b.summary.SetText(" Finishing earlier reads. Try the path again shortly.")
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.cwd, path)
	}
	path = filepath.Clean(path)
	if b.cancel != nil {
		b.cancel()
	}
	ctx, cancel := context.WithCancel(b.u.ctx)
	b.cancel = cancel
	b.generation++
	generation := b.generation
	b.summary.SetText(" Reading directory…")
	go func() {
		defer func() { <-b.u.workSlots }()
		entries, err := readDirectory(ctx, path)
		b.u.post(func() {
			if b.u.browser != b || b.generation != generation {
				return
			}
			if err != nil {
				b.summary.SetText(" " + safe(err.Error()))
				return
			}
			b.cwd = path
			b.u.lastDir = path
			b.entries = entries
			b.query = ""
			b.filter.SetText("")
			b.path.SetText(plain(path))
			b.render()
			b.table.Select(1, 0)
		})
	}()
}
func (b *browser) render() {
	b.visible = nil
	for _, e := range b.entries {
		if !b.hidden && strings.HasPrefix(e.name, ".") {
			continue
		}
		if matches(e.name, b.query) {
			b.visible = append(b.visible, e)
		}
	}
	sort.SliceStable(b.visible, func(i, j int) bool {
		a, c := b.visible[i], b.visible[j]
		if a.dir != c.dir {
			return a.dir
		}
		switch b.order {
		case 1:
			if a.size != c.size {
				return a.size > c.size
			}
		case 2:
			if !a.modified.Equal(c.modified) {
				return a.modified.After(c.modified)
			}
		}
		return strings.ToLower(a.name) < strings.ToLower(c.name)
	})
	row, _ := b.table.GetSelection()
	b.table.Clear()
	for col, label := range []string{"", "Name", "Size"} {
		b.table.SetCell(0, col, tview.NewTableCell(label).SetTextColor(b.u.muted).SetSelectable(false))
	}
	parent := tview.NewTableCell("../").SetTextColor(b.u.accent).SetExpansion(1)
	parent.SetClickedFunc(func() bool { b.load(filepath.Dir(b.cwd)); return true })
	b.table.SetCell(1, 0, tview.NewTableCell(" "))
	b.table.SetCell(1, 1, parent)
	b.table.SetCell(1, 2, tview.NewTableCell("parent"))
	for i, e := range b.visible {
		entry := e
		checked := "[ ]"
		if _, ok := b.selected[e.path]; ok {
			checked = "[x]"
		}
		check := tview.NewTableCell(tview.Escape(checked)).SetTextColor(b.u.accent)
		check.SetClickedFunc(func() bool {
			b.table.Select(i+2, 0)
			b.toggle(entry)
			return true
		})
		name := safe(e.name)
		size := bytes(e.size)
		if e.dir {
			name += "/"
			size = "directory"
		}
		if e.symlink {
			name += " @"
			size = "symlink: excluded"
		}
		nameCell := tview.NewTableCell(name).SetExpansion(1).SetMaxWidth(80).SetTextColor(b.u.foreground)
		nameCell.SetClickedFunc(func() bool {
			b.table.Select(i+2, 1)
			if entry.dir {
				b.load(entry.path)
			} else {
				b.toggle(entry)
			}
			return true
		})
		b.table.SetCell(i+2, 0, check).SetCell(i+2, 1, nameCell).SetCell(i+2, 2, tview.NewTableCell(size).SetAlign(tview.AlignRight).SetTextColor(b.u.muted))
	}
	b.table.Select(min(max(1, row), len(b.visible)+1), 0)
	b.updateSummary()
}
func (b *browser) updateSummary() {
	var total int64
	dirs := 0
	for _, e := range b.selected {
		if e.dir {
			dirs++
		} else {
			total += e.size
		}
	}
	sortName := []string{"name", "size", "newest"}[b.order]
	hidden := "hidden off"
	if b.hidden {
		hidden = "hidden on"
	}
	b.summary.SetText(fmt.Sprintf(" %d selected · %s + %d directories · %d shown\n Sort: %s · %s · ~ home · Ctrl-L path", len(b.selected), bytes(total), dirs, len(b.visible), sortName, hidden))
}
func (b *browser) toggle(e fileEntry) {
	if e.symlink {
		b.summary.SetText(" Symlinks are excluded in V1. Select the actual file or directory.")
		return
	}
	if _, ok := b.selected[e.path]; ok {
		delete(b.selected, e.path)
	} else {
		b.selected[e.path] = e
	}
	b.render()
}
func (b *browser) open(row int) {
	if row == 1 {
		b.load(filepath.Dir(b.cwd))
		return
	}
	i := row - 2
	if i < 0 || i >= len(b.visible) {
		return
	}
	e := b.visible[i]
	if e.dir {
		b.load(e.path)
	} else {
		b.toggle(e)
	}
}
func (b *browser) send() {
	if len(b.selected) == 0 {
		row, _ := b.table.GetSelection()
		i := row - 2
		if i >= 0 && i < len(b.visible) && !b.visible[i].symlink {
			b.selected[b.visible[i].path] = b.visible[i]
		}
	}
	if len(b.selected) == 0 {
		b.summary.SetText(" Select files or directories with Space, then press S.")
		return
	}
	paths := make([]string, 0, len(b.selected))
	for p := range b.selected {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// Avoid sending the same child twice when its parent directory is selected.
	roots := paths[:0]
	for _, p := range paths {
		covered := false
		for _, root := range roots {
			if b.selected[root].dir && strings.HasPrefix(p, root+string(os.PathSeparator)) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, p)
		}
	}
	b.u.send(model.SendRequest{Peer: b.peer.ID, Paths: roots, Kind: "files"})
}
func (b *browser) key(e *tcell.EventKey) *tcell.EventKey {
	if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
		items := []tview.Primitive{b.table, b.path, b.goPath, b.filter}
		for i := 0; i < b.buttons.GetItemCount(); i++ {
			items = append(items, b.buttons.GetItem(i))
		}
		idx := 0
		for i, p := range items {
			if p.HasFocus() {
				idx = i
				break
			}
		}
		delta := 1
		if e.Key() == tcell.KeyBacktab {
			delta = -1
		}
		b.u.app.SetFocus(items[(idx+delta+len(items))%len(items)])
		return nil
	}
	if e.Key() == tcell.KeyCtrlL {
		b.u.app.SetFocus(b.path)
		return nil
	}
	if b.path.HasFocus() || b.filter.HasFocus() {
		return e
	}
	if !b.u.cfg.UI.VimKeys && e.Key() == tcell.KeyRune && strings.ContainsRune("hjklgG", e.Rune()) {
		return nil
	}
	if e.Key() == tcell.KeyBackspace || e.Key() == tcell.KeyBackspace2 {
		b.load(filepath.Dir(b.cwd))
		return nil
	}
	switch e.Rune() {
	case 's', 'S':
		b.send()
		return nil
	case '/':
		b.u.app.SetFocus(b.filter)
		return nil
	case '.':
		b.hidden = !b.hidden
		b.render()
		return nil
	case 'o', 'O':
		b.order = (b.order + 1) % 3
		b.render()
		return nil
	case '~':
		home, _ := os.UserHomeDir()
		b.load(home)
		return nil
	case ' ':
		row, _ := b.table.GetSelection()
		if i := row - 2; i >= 0 && i < len(b.visible) {
			b.toggle(b.visible[i])
		}
		return nil
	}
	if b.u.cfg.UI.VimKeys {
		switch e.Rune() {
		case 'j':
			return tcell.NewEventKey(tcell.KeyDown, 0, 0)
		case 'k':
			return tcell.NewEventKey(tcell.KeyUp, 0, 0)
		case 'h':
			b.load(filepath.Dir(b.cwd))
			return nil
		case 'l':
			row, _ := b.table.GetSelection()
			b.open(row)
			return nil
		case 'g':
			b.table.Select(1, 0)
			return nil
		case 'G':
			b.table.Select(len(b.visible)+1, 0)
			return nil
		}
	}
	return e
}
