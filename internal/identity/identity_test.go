package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentIdentity(t *testing.T) {
	dir := t.TempDir()
	a, e := LoadOrCreate(dir)
	if e != nil {
		t.Fatal(e)
	}
	b, e := LoadOrCreate(dir)
	if e != nil {
		t.Fatal(e)
	}
	if a.Fingerprint != b.Fingerprint || len(a.Fingerprint) != 64 {
		t.Fatal("identity changed")
	}
	if e = os.Chmod(filepath.Join(dir, "identity.pem"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadOrCreate(dir); e == nil {
		t.Fatal("accepted public private key")
	}
}
func TestRejectSymlinkIdentity(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "key")
	if e := os.WriteFile(outside, []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(dir, "identity.pem")); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadOrCreate(dir); e == nil {
		t.Fatal("accepted symlink identity")
	}
}
