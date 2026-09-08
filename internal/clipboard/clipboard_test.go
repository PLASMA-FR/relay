package clipboard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeadlessClipboard(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	m := New(t.TempDir())
	if m.Backend() != "Relay Clipboard" {
		t.Fatal(m.Backend())
	}
	v := "hello\n世界\n"
	if e := m.Write(context.Background(), v); e != nil {
		t.Fatal(e)
	}
	got, e := m.Read(context.Background())
	if e != nil || got != v {
		t.Fatalf("%q %v", got, e)
	}
	info, _ := os.Stat(filepath.Join(m.dir, "clipboard.txt"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if e := m.WriteInternal(strings.Repeat("x", MaxBytes+1)); e == nil {
		t.Fatal("accepted oversized clipboard")
	}
}
func TestSSHClipboardIsInternal(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "example")
	t.Setenv("DISPLAY", ":0")
	if New(t.TempDir()).Backend() != "Relay Clipboard" {
		t.Fatal("SSH must use internal")
	}
}
func TestInternalRejectsSymlink(t *testing.T) {
	m := New(t.TempDir())
	if e := os.Symlink("/etc/hostname", filepath.Join(m.dir, "clipboard.txt")); e != nil {
		t.Fatal(e)
	}
	if _, e := m.ReadInternal(); e == nil {
		t.Fatal("followed symlink")
	}
}

// This test intentionally uses a real display server and clipboard executables.
// Run only in an isolated desktop session with synthetic clipboard data:
// RELAY_CLIPBOARD_INTEGRATION=1 go test ./internal/clipboard -run TestDisplayClipboardIntegration
func TestDisplayClipboardIntegration(t *testing.T) {
	if os.Getenv("RELAY_CLIPBOARD_INTEGRATION") != "1" {
		t.Skip("requires an isolated X11/Wayland display and clipboard tools")
	}
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	m := New(t.TempDir())
	m.FallbackInternal = false
	backend := m.Backend()
	if backend == "Relay Clipboard" {
		t.Fatal("integration test requires an actual system clipboard backend")
	}
	t.Log("backend:", backend)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	want := "Relay system clipboard\nUnicode: λ 世界 🛰\n"
	if err := m.Write(ctx, want); err != nil {
		t.Fatalf("system write: %v", err)
	}
	got, err := m.Read(ctx)
	if err != nil || got != want {
		t.Fatalf("system roundtrip: %q, %v", got, err)
	}
	internal, err := m.ReadInternal()
	if err != nil || internal != want {
		t.Fatalf("internal mirror: %q, %v", internal, err)
	}
	// Change the display's clipboard independently. Reading must observe that
	// change, rather than silently falling back to the previous internal mirror.
	var name string
	var args []string
	switch backend {
	case "System Clipboard (Wayland)":
		name = "wl-copy"
		args = []string{"--type", "text/plain;charset=utf-8"}
	case "System Clipboard (X11)":
		if available("xclip") {
			name = "xclip"
			args = []string{"-selection", "clipboard"}
		} else {
			name = "xsel"
			args = []string{"--clipboard", "--input"}
		}
	default:
		t.Fatalf("unexpected backend %s", backend)
	}
	other := "Changed outside Relay\n第二行\n"
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(other)
	if err = cmd.Run(); err != nil {
		t.Fatalf("external clipboard write: %v", err)
	}
	got, err = m.Read(ctx)
	if err != nil || got != other {
		t.Fatalf("external change not observed: %q, %v", got, err)
	}
	if err = m.Write(ctx, ""); err != nil {
		t.Fatalf("empty write: %v", err)
	}
	got, err = m.Read(ctx)
	if err != nil || got != "" {
		t.Fatalf("empty roundtrip: %q, %v", got, err)
	}
}
