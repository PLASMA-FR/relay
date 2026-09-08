// Package service manages Relay's unprivileged Linux systemd service.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/ipc"
	"github.com/PLASMA-FR/relay/internal/model"
)

const marker = "# Managed by Relay."

func UnitPath() string {
	home, _ := os.UserHomeDir()
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", "relay.service")
}
func quote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return "\"" + s + "\""
}
func Unit(binary string, c config.Config) string {
	execQuote := func(s string) string { return quote(strings.ReplaceAll(s, "$", "$$")) }
	return marker + "\n[Unit]\nDescription=Relay — private file and clipboard sharing\nAfter=network.target\n\n[Service]\nType=simple\nExecStart=" + execQuote(binary) + " --config " + execQuote(c.Paths.ConfigFile) + " daemon\n" +
		"Environment=" + quote("RELAY_STATE_DIR="+c.Paths.StateDir) + "\nEnvironment=" + quote("RELAY_SOCKET="+c.Paths.Socket) + "\n" +
		"Restart=on-failure\nRestartSec=3\nTimeoutStopSec=20\nUMask=0077\nNoNewPrivileges=true\n\n[Install]\nWantedBy=default.target\n"
}
func systemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("systemd user service: %s (%w); a user systemd session is required. On a VPS, enable lingering with 'loginctl enable-linger USER'", strings.TrimSpace(string(b)), e)
	}
	return string(b), nil
}

// handoff asks only the authenticated local daemon to flush its jobs and exit.
// Wait for its state lock, so a service never races an unsupervised instance.
func handoff(ctx context.Context, c config.Config) error {
	client := ipc.New(c.Paths.Socket)
	defer client.Close()
	probe, stop := context.WithTimeout(ctx, time.Second)
	_, err := client.Status(probe)
	stop()
	if err != nil {
		return nil
	}
	if _, err = client.Action(ctx, model.Action{Action: "shutdown"}); err != nil {
		return fmt.Errorf("stop existing daemon for service handoff: %w", err)
	}
	wait, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-wait.Done():
			return errors.New("existing daemon is still flushing transfers; retry service start shortly")
		case <-tick.C:
			f, err := os.OpenFile(filepath.Join(c.Paths.StateDir, "daemon.lock"), os.O_RDWR, 0600)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
				return nil
			}
			f.Close()
		}
	}
}

func awaitReady(ctx context.Context, c config.Config) error {
	client := ipc.New(c.Paths.Socket)
	defer client.Close()
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		probe, stop := context.WithTimeout(wait, 500*time.Millisecond)
		_, err := client.Status(probe)
		stop()
		if err == nil {
			return nil
		}
		select {
		case <-wait.Done():
			return errors.New("service started but IPC is not ready; inspect journalctl --user -u relay.service -n 30")
		case <-tick.C:
		}
	}
}
func Run(ctx context.Context, action string, c config.Config) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("service management requires Linux systemd; run 'relay daemon' with your process supervisor")
	}
	if _, e := exec.LookPath("systemctl"); e != nil {
		return "", errors.New("systemctl is unavailable; run 'relay daemon' with your process supervisor")
	}
	path := UnitPath()
	switch action {
	case "install":
		binary, e := os.Executable()
		if e != nil {
			return "", e
		}
		binary, e = filepath.EvalSymlinks(binary)
		if e != nil {
			return "", e
		}
		if b, e := os.ReadFile(path); e == nil && !strings.HasPrefix(string(b), marker) {
			return "", fmt.Errorf("%s is not managed by Relay; preserve it and choose a separate user service name", path)
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return "", e
		}
		f, e := os.CreateTemp(filepath.Dir(path), ".relay-unit-*")
		if e != nil {
			return "", e
		}
		defer os.Remove(f.Name())
		if _, e = f.WriteString(Unit(binary, c)); e != nil {
			f.Close()
			return "", e
		}
		if e = f.Close(); e != nil {
			return "", e
		}
		if e = os.Rename(f.Name(), path); e != nil {
			return "", e
		}
		if _, e = systemctl(ctx, "daemon-reload"); e != nil {
			return "", e
		}
		if e = handoff(ctx, c); e != nil {
			return "", e
		}
		if _, e = systemctl(ctx, "enable", "--now", "relay.service"); e != nil {
			return "", e
		}
		if e = awaitReady(ctx, c); e != nil {
			return "", e
		}
		return "Relay service installed and running. It will start with your user session.", nil
	case "uninstall":
		b, e := os.ReadFile(path)
		if errors.Is(e, os.ErrNotExist) {
			return "Relay service is not installed.", nil
		}
		if e != nil {
			return "", e
		}
		if !strings.HasPrefix(string(b), marker) {
			return "", fmt.Errorf("refusing to remove unmanaged unit %s", path)
		}
		if _, e = systemctl(ctx, "disable", "--now", "relay.service"); e != nil {
			return "", e
		}
		if e = os.Remove(path); e != nil {
			return "", e
		}
		if _, e = systemctl(ctx, "daemon-reload"); e != nil {
			return "", e
		}
		return "Relay service removed. Files, configuration and device identity were preserved.", nil
	case "start", "stop", "restart":
		if action == "start" || action == "restart" {
			if e := handoff(ctx, c); e != nil {
				return "", e
			}
		}
		_, e := systemctl(ctx, action, "relay.service")
		if e == nil && (action == "start" || action == "restart") {
			e = awaitReady(ctx, c)
		}
		return "Relay service: " + action, e
	case "status":
		return systemctl(ctx, "status", "relay.service", "--no-pager", "--lines=8")
	default:
		return "", fmt.Errorf("unknown service action %q", action)
	}
}
