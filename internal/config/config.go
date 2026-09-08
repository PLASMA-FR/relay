// Package config owns XDG locations and validates the human-editable TOML file.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Paths struct{ ConfigFile, StateDir, CacheDir, Socket string }
type Receive struct {
	Directory         string `toml:"directory"`
	AutoAcceptTrusted bool   `toml:"auto_accept_trusted"`
	Conflict          string `toml:"conflict"`
	MaxBytes          int64  `toml:"max_bytes"`
}
type UI struct {
	Mouse   bool `toml:"mouse"`
	VimKeys bool `toml:"vim_keys"`
	ASCII   bool `toml:"ascii"`
}
type Clipboard struct {
	FallbackInternal bool `toml:"fallback_internal"`
}
type Network struct {
	TailscaleOnly    bool   `toml:"tailscale_only"`
	Port             int    `toml:"port"`
	Listen           string `toml:"listen"`
	MaxConcurrent    int    `toml:"max_concurrent"`
	DiscoverySeconds int    `toml:"discovery_seconds"`
}
type Config struct {
	Name      string    `toml:"name"`
	Paths     Paths     `toml:"-"`
	Receive   Receive   `toml:"receive"`
	UI        UI        `toml:"ui"`
	Clipboard Clipboard `toml:"clipboard"`
	Network   Network   `toml:"network"`
}

func xdg(key, fallback string) string {
	if v := os.Getenv(key); filepath.IsAbs(v) {
		return v
	}
	return fallback
}

func Default() Config {
	home, _ := os.UserHomeDir()
	name, _ := os.Hostname()
	state := filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), "relay")
	cache := filepath.Join(xdg("XDG_CACHE_HOME", filepath.Join(home, ".cache")), "relay")
	cf := filepath.Join(xdg("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "relay", "config.toml")
	if v := os.Getenv("RELAY_CONFIG"); v != "" {
		cf = ExpandPath(v)
	}
	socket := filepath.Join(state, "run", "relay.sock")
	if v := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(v) {
		socket = filepath.Join(v, "relay.sock")
	}
	if v := os.Getenv("RELAY_STATE_DIR"); v != "" {
		state = ExpandPath(v)
		socket = filepath.Join(state, "run", "relay.sock")
	}
	if v := os.Getenv("RELAY_SOCKET"); v != "" {
		socket = ExpandPath(v)
	}
	dest := filepath.Join(home, "relay-inbox")
	if info, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && info.IsDir() {
		dest = filepath.Join(home, "Downloads", "Relay")
	}
	return Config{Name: name, Paths: Paths{cf, state, cache, socket},
		Receive: Receive{Directory: dest, AutoAcceptTrusted: true, Conflict: "rename", MaxBytes: 1 << 40},
		UI:      UI{Mouse: true, VimKeys: true}, Clipboard: Clipboard{FallbackInternal: true},
		Network: Network{TailscaleOnly: true, Port: 7331, MaxConcurrent: 3, DiscoverySeconds: 15}}
}

func ExpandPath(s string) string {
	if s == "~" {
		h, _ := os.UserHomeDir()
		return h
	}
	if strings.HasPrefix(s, "~/") {
		if h, e := os.UserHomeDir(); e == nil {
			return filepath.Join(h, strings.TrimPrefix(s, "~/"))
		}
	}
	return s
}

func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		c.Paths.ConfigFile = ExpandPath(path)
	}
	f, err := os.Open(c.Paths.ConfigFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, fmt.Errorf("read config: %w", err)
	}
	if err == nil {
		defer f.Close()
		if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(&c); err != nil {
			var strict *toml.StrictMissingError
			if errors.As(err, &strict) {
				return c, fmt.Errorf("config %s: %s", c.Paths.ConfigFile, strict.String())
			}
			return c, fmt.Errorf("config %s: %w", c.Paths.ConfigFile, err)
		}
	}
	c.Receive.Directory = ExpandPath(c.Receive.Directory)
	if !filepath.IsAbs(c.Receive.Directory) {
		return c, errors.New("receive.directory must be an absolute path or start with ~/")
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 || strings.ContainsAny(c.Name, "\r\n\x00\x1b") {
		return c, errors.New("name must contain 1–128 printable bytes")
	}
	if c.Receive.MaxBytes < 1 || c.Receive.MaxBytes > 1<<50 {
		return c, errors.New("receive.max_bytes must be between 1 and 1125899906842624")
	}
	switch c.Receive.Conflict {
	case "rename", "replace", "skip", "cancel":
	default:
		return c, errors.New("receive.conflict must be rename, replace, skip, or cancel")
	}
	if c.Network.Port < 1 || c.Network.Port > 65535 {
		return c, errors.New("network.port must be between 1 and 65535")
	}
	if c.Network.MaxConcurrent < 1 || c.Network.MaxConcurrent > 16 {
		return c, errors.New("network.max_concurrent must be between 1 and 16")
	}
	if c.Network.DiscoverySeconds < 3 || c.Network.DiscoverySeconds > 3600 {
		return c, errors.New("network.discovery_seconds must be between 3 and 3600")
	}
	if !c.Network.TailscaleOnly {
		host := c.Network.Listen
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return c, errors.New("tailscale_only=false is for local tests only: set network.listen to a numeric loopback address")
		}
	}
	for _, p := range []string{c.Paths.StateDir, c.Paths.CacheDir, c.Paths.Socket} {
		if !filepath.IsAbs(p) {
			return c, fmt.Errorf("XDG/Relay paths must be absolute: %q", p)
		}
	}
	if len(c.Paths.Socket) > 103 {
		return c, errors.New("IPC socket path is too long; set RELAY_SOCKET to a shorter path in a private directory")
	}
	return c, nil
}

// Ensure creates only Relay-owned directories. It never replaces user config.
func Ensure(c Config) error {
	for _, p := range []string{c.Paths.StateDir, c.Paths.CacheDir, filepath.Dir(c.Paths.ConfigFile)} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return fmt.Errorf("create %s: %w", p, err)
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Relay directory must be a real directory: %s", p)
		}
	}
	if err := os.Chmod(c.Paths.StateDir, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Receive.Directory, 0700); err != nil {
		return fmt.Errorf("create inbox: %w", err)
	}
	return nil
}
