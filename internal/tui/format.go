package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/rivo/tview"
)

// plain removes terminal controls, bidi overrides, and line breaks from labels.
// Never interpret ANSI received from a filename, peer, clipboard, or daemon.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
}
func safe(s string) string { return tview.Escape(plain(s)) }
func bytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, unit := range units {
		v /= 1024
		if v < 1024 || unit == "PiB" {
			return fmt.Sprintf("%.1f %s", v, unit)
		}
	}
	return "0 B"
}
func progress(done, total int64, width int, ascii bool) string {
	if width < 1 {
		return ""
	}
	f := 0.0
	if total > 0 {
		f = math.Max(0, math.Min(1, float64(done)/float64(total)))
	}
	filled := int(f * float64(width))
	a, b := "━", "─"
	if ascii {
		a, b = "=", "-"
	}
	return strings.Repeat(a, filled) + strings.Repeat(b, width-filled) + fmt.Sprintf(" %3.0f%%", f*100)
}
func duration(seconds float64) string {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return "—"
	}
	d := time.Duration(math.Min(seconds, 365*24*3600)) * time.Second
	if d < time.Second {
		return "<1s"
	}
	return d.String()
}
func matches(value, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	hay := []rune(strings.ToLower(value))
	needle := []rune(query)
	i := 0
	for _, r := range hay {
		if r == needle[i] {
			i++
			if i == len(needle) {
				return true
			}
		}
	}
	return false
}
