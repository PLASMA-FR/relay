package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/pelletier/go-toml/v2"
)

// TestInteractiveFixture exposes two real loopback-only daemons for a human or
// terminal harness to exercise the compiled CLI/TUI. It never contacts a Tailnet
// or reads a real trust registry. Normal test runs skip it.
//
//	RELAY_INTERACTIVE_DIR=/tmp/relay-interactive-check go test ./internal/daemon \
//	  -run '^TestInteractiveFixture$' -count=1 -timeout=12m -v
//
// Read fixture.json after it appears. Set a device's environment map when
// launching Relay. Create <RELAY_INTERACTIVE_DIR>/stop to finish and clean up the
// daemon state, sources, and inboxes. Metadata is retained for the harness.
func TestInteractiveFixture(t *testing.T) {
	output := os.Getenv("RELAY_INTERACTIVE_DIR")
	if output == "" {
		t.Skip("set RELAY_INTERACTIVE_DIR to enable the live loopback fixture")
	}
	if !filepath.IsAbs(output) {
		t.Fatal("RELAY_INTERACTIVE_DIR must be an absolute private directory")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(output)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fixture directory must be a real directory", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("fixture directory must have mode 0700")
	}
	metadataPath := filepath.Join(output, "fixture.json")
	for _, path := range []string{metadataPath, filepath.Join(output, "stop")} {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("use a fresh fixture directory; %s already exists", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}

	desktopConfig, laptopConfig := testConfig(t, "desktop"), testConfig(t, "laptop")
	// Paths are passed to clients through environment values because they are
	// deliberately excluded from editable TOML configuration.
	for _, cfg := range []*config.Config{&desktopConfig, &laptopConfig} {
		cfg.Paths.CacheDir = filepath.Join(cfg.Paths.CacheDir, "relay")
		cfg.Receive.AutoAcceptTrusted = true
		cfg.UI.Mouse = true
		cfg.UI.VimKeys = true
		cfg.UI.ASCII = false
		if err := os.MkdirAll(filepath.Dir(cfg.Paths.ConfigFile), 0700); err != nil {
			t.Fatal(err)
		}
		encoded, err := toml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.Paths.ConfigFile, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	desktop, _ := startDaemon(t, desktopConfig)
	laptop, _ := startDaemon(t, laptopConfig)
	connectDaemons(desktop, laptop)
	for _, daemon := range []*Daemon{desktop, laptop} {
		daemon.mu.Lock()
		for id, peer := range daemon.peers {
			peer.Hostname = peer.Name
			peer.OS = "Linux"
			peer.Arch = runtime.GOARCH
			peer.Version = model.Version
			peer.Capabilities = []string{"files", "directories", "text", "url", "sha256", "resume"}
			peer.LastSeen = time.Now()
			daemon.peers[id] = peer
		}
		daemon.notices = []string{"Interactive fixture: real encrypted transfers on loopback; fictional device identities."}
		daemon.notifyLocked()
		daemon.mu.Unlock()
	}

	sourceRoot := filepath.Join(t.TempDir(), "sources")
	project := filepath.Join(sourceRoot, "project space")
	if err := os.MkdirAll(filepath.Join(project, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	unicodeFile, emptyFile := filepath.Join(sourceRoot, "hello 世界.txt"), filepath.Join(sourceRoot, "empty.txt")
	contents := map[string][]byte{
		unicodeFile:                              []byte("Hello from Relay's isolated desktop.\n"),
		emptyFile:                                nil,
		filepath.Join(project, "README.md"):      []byte("# Interactive fixture\nFictional development context.\n"),
		filepath.Join(project, "src", "main.go"): []byte("package main\n\nfunc main() {}\n"),
	}
	for path, data := range contents {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(project, "run.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'Relay fixture\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	type device struct {
		Name              string            `json:"name"`
		Socket            string            `json:"socket"`
		Config            string            `json:"config"`
		State             string            `json:"state"`
		Inbox             string            `json:"inbox"`
		AutoAcceptTrusted bool              `json:"auto_accept_trusted"`
		Env               map[string]string `json:"env"`
	}
	describe := func(c config.Config) device {
		return device{
			Name: c.Name, Socket: c.Paths.Socket, Config: c.Paths.ConfigFile, State: c.Paths.StateDir, Inbox: c.Receive.Directory, AutoAcceptTrusted: c.Receive.AutoAcceptTrusted,
			Env: map[string]string{"RELAY_CONFIG": c.Paths.ConfigFile, "RELAY_STATE_DIR": c.Paths.StateDir, "RELAY_SOCKET": c.Paths.Socket, "XDG_CACHE_HOME": filepath.Dir(c.Paths.CacheDir)},
		}
	}
	metadata := struct {
		Desktop          device    `json:"desktop"`
		Laptop           device    `json:"laptop"`
		SourceDirectory  string    `json:"source_directory"`
		UnicodeFile      string    `json:"unicode_file"`
		EmptyFile        string    `json:"empty_file"`
		ProjectDirectory string    `json:"project_directory"`
		Sources          []string  `json:"sources"`
		StopFile         string    `json:"stop_file"`
		ExpiresAt        time.Time `json:"expires_at"`
	}{Desktop: describe(desktopConfig), Laptop: describe(laptopConfig), SourceDirectory: sourceRoot, UnicodeFile: unicodeFile, EmptyFile: emptyFile, ProjectDirectory: project, Sources: []string{unicodeFile, emptyFile, project}, StopFile: filepath.Join(output, "stop"), ExpiresAt: time.Now().Add(10 * time.Minute)}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := os.CreateTemp(output, ".fixture-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		t.Fatal(err)
	}
	if err = temporary.Sync(); err != nil {
		temporary.Close()
		t.Fatal(err)
	}
	if err = temporary.Close(); err != nil {
		t.Fatal(err)
	}
	// Link is atomic and refuses to overwrite an existing metadata document.
	if err = os.Link(temporaryPath, metadataPath); err != nil {
		t.Fatal(err)
	}
	t.Logf("Interactive fixture ready: %s", metadataPath)
	t.Log("Both devices auto-accept explicitly trusted peers; transports are loopback-only TLS.")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Until(metadata.ExpiresAt))
	defer timeout.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(metadata.StopFile); err == nil {
				t.Log("Stop requested; cleaning up both daemons and temporary files.")
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		case <-timeout.C:
			t.Log("Ten-minute fixture limit reached; cleaning up.")
			return
		}
	}
}
