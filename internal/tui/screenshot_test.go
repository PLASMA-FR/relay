package tui

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/gdamore/tcell/v2"
)

// TestCaptureScreen is an explicit documentation tool, not a golden screenshot
// assertion. Its data is fictional and never comes from the local daemon.
func TestCaptureScreen(t *testing.T) {
	output := os.Getenv("RELAY_TUI_CAPTURE")
	if output == "" {
		t.Skip("set RELAY_TUI_CAPTURE to regenerate the documented SimulationScreen capture")
	}
	u, _, screen := setup(t)
	u.ascii = false
	snapshot := fixture()
	snapshot.ReceiveDirectory = "/home/you/Downloads/Relay"
	for i := range snapshot.Peers {
		snapshot.Peers[i].OS = "Linux"
		snapshot.Peers[i].Version = "0.1.0"
		snapshot.Peers[i].Protocol = 1
	}
	snapshot.Peers = append(snapshot.Peers, model.Peer{ID: "workstation", Name: "workstation", Address: "100.64.0.42", OS: "Linux", Trusted: true})
	snapshot.History[0].Peer = "dev-vps"
	u.apply(snapshot)
	draw(u, screen, 100, 30)
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "relay.txt"), []byte(rendered(screen)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "relay.svg"), []byte(screenSVG(screen)), 0644); err != nil {
		t.Fatal(err)
	}
}

func screenSVG(screen tcell.SimulationScreen) string {
	cells, w, h := screen.GetContents()
	const cw, ch, pad = 9, 20, 18
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title description"><title id="title">Relay terminal interface</title><desc id="description">Actual 100 by 30 cell tcell SimulationScreen capture using fictional device and transfer data.</desc><rect width="100%%" height="100%%" rx="12" fill="#10161e"/><g font-family="DejaVu Sans Mono, Liberation Mono, monospace" font-size="15" xml:space="preserve">`, w*cw+pad*2, h*ch+pad*2, w*cw+pad*2, h*ch+pad*2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell := cells[y*w+x]
			fg, bg, attrs := cell.Style.Decompose()
			foreground, background := "#e8edf2", "#10161e"
			if fg != tcell.ColorDefault && fg.Valid() {
				foreground = fmt.Sprintf("#%06x", fg.Hex())
			}
			if bg != tcell.ColorDefault && bg.Valid() {
				background = fmt.Sprintf("#%06x", bg.Hex())
			}
			if attrs&tcell.AttrReverse != 0 {
				foreground, background = background, foreground
			}
			if background != "#10161e" {
				fmt.Fprintf(&out, `<rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`, pad+x*cw, pad+y*ch, cw, ch, background)
			}
			if len(cell.Runes) == 0 || string(cell.Runes) == " " {
				continue
			}
			weight := "normal"
			if attrs&tcell.AttrBold != 0 {
				weight = "bold"
			}
			fmt.Fprintf(&out, `<text x="%d" y="%d" fill="%s" font-weight="%s">%s</text>`, pad+x*cw, pad+y*ch+15, foreground, weight, html.EscapeString(string(cell.Runes)))
		}
	}
	out.WriteString("</g></svg>\n")
	return out.String()
}
