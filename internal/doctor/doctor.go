// Package doctor performs local, read-only diagnostics except for a temporary
// inbox write test that is removed immediately.
package doctor

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/ipc"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/service"
	"github.com/PLASMA-FR/relay/internal/tailscale"
)

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}
type Report struct {
	Ready  bool    `json:"ready"`
	Checks []Check `json:"checks"`
}

func Run(ctx context.Context, c config.Config) Report {
	r := Report{Ready: true}
	add := func(name, status, detail, fix string) {
		r.Checks = append(r.Checks, Check{name, status, detail, fix})
		if status == "error" {
			r.Ready = false
		}
	}
	add("configuration", "ok", c.Paths.ConfigFile, "")
	client := ipc.New(c.Paths.Socket)
	defer client.Close()
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	snap, err := client.Status(probeCtx)
	cancel()
	if err != nil {
		add("daemon / local IPC", "error", "daemon is not reachable", "Run relay to start it, or relay service install for background operation.")
	} else {
		add("daemon / local IPC", "ok", fmt.Sprintf("%s · protocol %d", snap.Version, model.ProtocolVersion), "")
		if snap.TrustMode == "tailnet" {
			add("access policy", "ok", "automatic Tailscale trust; no pairing required", "")
			if snap.Address != "" {
				address := snap.Address
				if net.ParseIP(address) != nil {
					address = net.JoinHostPort(address, fmt.Sprint(c.Network.Port))
				}
				if _, e := tailscale.WhoIs(ctx, address); e != nil {
					add("Tailscale identity API", "error", e.Error(), "Ensure local tailscaled is running and allows your user to read identity information.")
				} else {
					add("Tailscale identity API", "ok", "local peer identity verification available", "")
				}
			}
		}
		if snap.Fingerprint != "" {
			add("device identity", "ok", snap.Fingerprint, "")
		}
		if snap.Version != model.Version {
			add("client compatibility", "warn", "client and daemon versions differ", "Run relay service restart after upgrading.")
		}
		online := 0
		trusted := 0
		for _, p := range snap.Peers {
			if p.Relay {
				online++
			}
			if p.Trusted {
				trusted++
			}
			if p.Protocol != 0 && p.Protocol != model.ProtocolVersion {
				add("peer protocol", "warn", p.Name+" has an incompatible protocol", "Upgrade Relay on both devices.")
			}
		}
		add("Relay discovery", "ok", fmt.Sprintf("%d reachable Relay peers · %d trusted · %d Tailnet devices", online, trusted, len(snap.Peers)), "")
		for _, n := range snap.Notices {
			add("daemon notice", "warn", n, "")
		}
	}
	if _, e := exec.LookPath("tailscale"); e != nil {
		add("Tailscale", "error", "CLI not installed", "Install Tailscale from https://tailscale.com/download, then run tailscale up.")
	} else {
		tc, stop := context.WithTimeout(ctx, 5*time.Second)
		b, e := exec.CommandContext(tc, "tailscale", "status", "--json").Output()
		stop()
		var ts struct {
			BackendState string
			TailscaleIPs []string
			Peer         map[string]json.RawMessage
			Health       []string
		}
		if e != nil || json.Unmarshal(b, &ts) != nil {
			add("Tailscale", "error", "local Tailscale daemon unavailable", "Run tailscale status; ensure tailscaled is running and readable by your user.")
		} else if ts.BackendState != "Running" {
			add("Tailscale authentication", "error", ts.BackendState, "Run tailscale up to connect this device.")
		} else {
			add("Tailscale authentication", "ok", "connected", "")
			add("Tailnet address", "ok", strings.Join(ts.TailscaleIPs, ", "), "")
			add("Tailnet peer map", "ok", fmt.Sprintf("%d devices known; Relay checks their application port", len(ts.Peer)), "")
			for _, h := range ts.Health {
				add("Tailscale health", "warn", h, "Run tailscale netcheck for transport diagnostics.")
			}
		}
	}
	if f, e := os.CreateTemp(c.Receive.Directory, ".relay-doctor-*"); e != nil {
		add("receive directory", "error", c.Receive.Directory+": "+e.Error(), "Create the directory or set receive.directory to a writable path.")
	} else {
		_, e = f.WriteString("relay write test\n")
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		os.Remove(f.Name())
		if e != nil {
			add("receive directory", "error", e.Error(), "Check available space and filesystem permissions.")
		} else {
			add("receive directory", "ok", c.Receive.Directory, "")
		}
	}
	if info, e := os.Stat(c.Paths.StateDir); e == nil {
		if info.Mode().Perm()&0077 != 0 {
			add("state permissions", "error", "state directory is accessible to other users", "Set its permissions to 0700 and restart Relay.")
		} else {
			add("state permissions", "ok", "private state directory (0700)", "")
		}
	} else {
		add("state permissions", "warn", "state not initialized", "Run relay once to create your identity and private state.")
	}
	keyPath := filepath.Join(c.Paths.StateDir, "identity.pem")
	if info, e := os.Lstat(keyPath); e != nil {
		add("identity key health", "warn", "identity not initialized", "Run relay once to generate a private device identity.")
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		add("identity key health", "error", "identity key must be a private regular file", "Preserve identity.pem; restore owner-only permissions (0600).")
	} else {
		b, e := os.ReadFile(keyPath)
		block, _ := pem.Decode(b)
		valid := false
		if e == nil && block != nil {
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				_, valid = key.(ed25519.PrivateKey)
			}
		}
		if valid {
			add("identity key health", "ok", "valid Ed25519 private key (0600)", "")
		} else {
			add("identity key health", "error", "identity.pem is invalid", "Restore a backup; do not delete the identity unless you intend to re-pair every device.")
		}
	}
	trustPath := filepath.Join(c.Paths.StateDir, "daemon.json")
	if b, e := os.ReadFile(trustPath); e == nil {
		if json.Valid(b) {
			add("trust registry", "ok", "state JSON is readable", "")
		} else {
			add("trust registry", "error", "state JSON is invalid", "Preserve the file and consult daemon logs; do not delete your identity.")
		}
	} else {
		add("trust registry", "warn", "not initialized", "Start the daemon and pair another device.")
	}
	backend := clipboard.New(c.Paths.StateDir).Backend()
	add("clipboard", "ok", backend, "")
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		add("session", "ok", "headless environment; no GUI required", "")
	}
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		add("terminal", "warn", "TERM is missing or dumb; CLI remains available", "Use an xterm-compatible terminal for the TUI.")
	} else {
		add("terminal", "ok", term, "")
	}
	if os.Getenv("TMUX") != "" {
		add("multiplexer", "ok", "tmux detected; enable tmux mouse support to pass clicks", "")
	} else if os.Getenv("STY") != "" {
		add("multiplexer", "ok", "screen detected", "")
	}
	if _, e := os.Stat(service.UnitPath()); e != nil {
		add("systemd service", "warn", "not installed (optional)", "Run relay service install to start Relay with your session.")
	} else {
		sc, stop := context.WithTimeout(ctx, 3*time.Second)
		e := exec.CommandContext(sc, "systemctl", "--user", "is-active", "--quiet", "relay.service").Run()
		stop()
		if e == nil {
			add("systemd service", "ok", "active", "")
		} else {
			add("systemd service", "warn", "installed but inactive", "Run relay service start; on a VPS, enable user lingering.")
		}
	}
	return r
}
