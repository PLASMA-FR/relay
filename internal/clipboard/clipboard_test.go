package clipboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
