package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// terminalRoot adapts rendering without changing tview's process-wide style or
// border globals. Input and native mouse geometry remain unchanged.
type terminalRoot struct {
	tview.Primitive
	ascii, noColor bool
}

func (r *terminalRoot) Draw(screen tcell.Screen) {
	r.Primitive.Draw(adaptedScreen{Screen: screen, ascii: r.ascii, noColor: r.noColor})
}

type adaptedScreen struct {
	tcell.Screen
	ascii, noColor bool
}

func (s adaptedScreen) SetContent(x, y int, r rune, combining []rune, style tcell.Style) {
	if s.ascii {
		switch r {
		case '─', '━', '═':
			r = '-'
		case '│', '┃', '║':
			r = '|'
		case '●', '○':
			r = 'o'
		case '→':
			r = '>'
		case '←':
			r = '<'
		case '✓':
			r = '+'
		case '·', '…':
			r = '.'
		case '—', '–':
			r = '-'
		default:
			if r >= 0x2500 && r <= 0x257f {
				r = '+'
			}
		}
	}
	if s.noColor {
		_, _, attrs := style.Decompose()
		style = tcell.StyleDefault.Attributes(attrs)
	}
	s.Screen.SetContent(x, y, r, combining, style)
}
