package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestTailnetDetailsHaveBlockInsteadOfPairing(t *testing.T) {
	u, backend, screen := setup(t)
	s := fixture()
	s.TrustMode = "tailnet"
	u.apply(s)
	u.deviceDetails()
	draw(u, screen, 100, 30)
	view := rendered(screen)
	if !strings.Contains(view, "Pairing is not required") || !strings.Contains(view, "Block device") || strings.Contains(view, "Compare & trust") {
		t.Fatal(view)
	}
	press(u, tcell.KeyEnter, 0)
	press(u, tcell.KeyEnter, 0)
	drainUntil(t, u, func() bool { backend.mu.Lock(); defer backend.mu.Unlock(); return len(backend.actions) > 0 })
	backend.mu.Lock()
	if backend.actions[0].Action != "untrust" {
		t.Fatal(backend.actions)
	}
	backend.mu.Unlock()
	s.Peers[0].Blocked = true
	s.Peers[0].Trusted = false
	u.apply(s)
	u.deviceDetails()
	draw(u, screen, 100, 30)
	if !strings.Contains(rendered(screen), "Unblock device") {
		t.Fatal(rendered(screen))
	}
}
