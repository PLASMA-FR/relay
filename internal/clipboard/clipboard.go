// Package clipboard provides optional desktop integration and an always-available
// private clipboard. Receiving text never injects it into the desktop clipboard.
package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const MaxBytes = 1 << 20

type Manager struct {
	dir              string
	FallbackInternal bool
}

func New(stateDir string) *Manager { return &Manager{dir: stateDir, FallbackInternal: true} }
func available(s string) bool      { _, err := exec.LookPath(s); return err == nil }
func (m *Manager) Backend() string {
	// SSH's forwarding/display variables do not identify the invoking terminal's
	// clipboard. Keep it explicit and local; never emit OSC 52 automatically.
	if os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == "" {
		if os.Getenv("WAYLAND_DISPLAY") != "" && available("wl-copy") && available("wl-paste") {
			return "System Clipboard (Wayland)"
		}
		if os.Getenv("DISPLAY") != "" && (available("xclip") || available("xsel")) {
			return "System Clipboard (X11)"
		}
	}
	return "Relay Clipboard"
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxBytes-b.Len() {
		return 0, fmt.Errorf("clipboard exceeds %d bytes", MaxBytes)
	}
	return b.Buffer.Write(p)
}
func run(ctx context.Context, input string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 250 * time.Millisecond
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out limitedBuffer
	cmd.Stdout = &out
	if e := cmd.Run(); e != nil {
		return "", e
	}
	return out.String(), nil
}

func writeSystem(ctx context.Context, text, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdin = strings.NewReader(text)
	// Clipboard owners may fork and retain their parent's descriptors. Nil
	// stdout/stderr attach /dev/null directly, avoiding inherited capture pipes.
	return cmd.Run()
}
func (m *Manager) Read(ctx context.Context) (string, error) {
	var text string
	var err error
	switch m.Backend() {
	case "System Clipboard (Wayland)":
		text, err = run(ctx, "", "wl-paste", "--no-newline")
	case "System Clipboard (X11)":
		if available("xclip") {
			text, err = run(ctx, "", "xclip", "-selection", "clipboard", "-o")
		} else {
			text, err = run(ctx, "", "xsel", "--clipboard", "--output")
		}
	default:
		if !m.FallbackInternal {
			return "", errors.New("no system clipboard available and fallback_internal is disabled")
		}
		return m.ReadInternal()
	}
	if err != nil {
		if m.FallbackInternal {
			return m.ReadInternal()
		}
		return "", fmt.Errorf("read system clipboard: %w", err)
	}
	return text, nil
}
func (m *Manager) Write(ctx context.Context, text string) error {
	if m.Backend() == "Relay Clipboard" && !m.FallbackInternal {
		return errors.New("no system clipboard available and fallback_internal is disabled")
	}
	if err := m.WriteInternal(text); err != nil {
		return err
	}
	var err error
	switch m.Backend() {
	case "System Clipboard (Wayland)":
		err = writeSystem(ctx, text, "wl-copy", "--type", "text/plain;charset=utf-8")
	case "System Clipboard (X11)":
		if available("xclip") {
			err = writeSystem(ctx, text, "xclip", "-selection", "clipboard")
		} else {
			err = writeSystem(ctx, text, "xsel", "--clipboard", "--input")
		}
	}
	if err != nil && !m.FallbackInternal {
		return fmt.Errorf("text saved to Relay Clipboard; system clipboard unavailable: %w", err)
	}
	return nil
}
func (m *Manager) ReadInternal() (string, error) {
	p := filepath.Join(m.dir, "clipboard.txt")
	st, e := os.Lstat(p)
	if errors.Is(e, os.ErrNotExist) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("Relay Clipboard is not a regular file")
	}
	if st.Size() > MaxBytes {
		return "", errors.New("Relay Clipboard exceeds 1 MiB")
	}
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if len(b) > MaxBytes {
		return "", errors.New("Relay Clipboard exceeds 1 MiB")
	}
	return string(b), e
}
func (m *Manager) WriteInternal(text string) error {
	if len(text) > MaxBytes {
		return errors.New("clipboard text exceeds 1 MiB; send a file instead")
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return err
	}
	f, e := os.CreateTemp(m.dir, ".clipboard-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(text); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(m.dir, "clipboard.txt"))
}
