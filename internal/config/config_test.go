package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("RELAY_CONFIG", "")
	t.Setenv("RELAY_STATE_DIR", "")
	t.Setenv("RELAY_SOCKET", "")
	c, e := Load("")
	if e != nil {
		t.Fatal(e)
	}
	if c.Receive.Directory != filepath.Join(dir, "relay-inbox") {
		t.Fatal(c)
	}
	p := filepath.Join(dir, "config.toml")
	if e = os.WriteFile(p, []byte("name = 'laptop'\n[receive]\ndirectory = '~/inbox'\nauto_accept_trusted = false\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c, e = Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Name != "laptop" || c.Receive.AutoAcceptTrusted || !c.UI.Mouse || c.Receive.Directory != filepath.Join(dir, "inbox") {
		t.Fatal(c)
	}
	if e = Ensure(c); e != nil {
		t.Fatal(e)
	}
	if st, _ := os.Stat(c.Paths.StateDir); st.Mode().Perm() != 0700 {
		t.Fatal(st.Mode())
	}
}
func TestInvalidConfig(t *testing.T) {
	t.Setenv("RELAY_SOCKET", "/tmp/relay-test.sock")
	for _, body := range []string{"oops = 1", "[receive]\nconflict = 'overwrite'", "[network]\nport = -1", "[receive]\nmax_bytes = -1", "[receive]\ndirectory = 'relative'", "[network]\ntailscale_only = false\nlisten = '0.0.0.0'"} {
		t.Run(body, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			_ = os.WriteFile(p, []byte(body), 0600)
			if _, e := Load(p); e == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	if ExpandPath("~") != "/tmp/home" {
		t.Fatal(ExpandPath("~"))
	}
}
