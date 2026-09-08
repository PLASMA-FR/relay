package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/protocol"
)

type harness struct {
	sender, receiver *Engine
	addr             string
	receiveDir       string
	receiverID       *identity.Identity
	cancel           context.CancelFunc
	listener         net.Listener
	mu               sync.Mutex
	errs             []error
	wrap             func(net.Conn) net.Conn
}

func setup(t testing.TB, progress func(Event)) *harness {
	t.Helper()
	base := t.TempDir()
	a, e := identity.LoadOrCreate(filepath.Join(base, "a"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := identity.LoadOrCreate(filepath.Join(base, "b"))
	if e != nil {
		t.Fatal(e)
	}
	h := &harness{receiveDir: filepath.Join(base, "inbox"), receiverID: b}
	h.sender, e = New(Options{Identity: a, StateDir: filepath.Join(base, "a"), ReceiveDir: filepath.Join(base, "out"), Trusted: func(fp string) bool { return fp == b.Fingerprint }})
	if e != nil {
		t.Fatal(e)
	}
	h.receiver, e = New(Options{Identity: b, StateDir: filepath.Join(base, "b"), ReceiveDir: h.receiveDir, Conflict: "rename", MaxBytes: 8 << 30, Trusted: func(fp string) bool { return fp == a.Fingerprint }, Decide: func(context.Context, string, Offer) error { return nil }, Progress: progress})
	if e != nil {
		t.Fatal(e)
	}
	h.listener, e = net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	h.addr = h.listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		for {
			c, e := h.listener.Accept()
			if e != nil {
				return
			}
			go func() {
				h.mu.Lock()
				receiver := h.receiver
				wrap := h.wrap
				h.mu.Unlock()
				if wrap != nil {
					c = wrap(c)
				}
				e := receiver.Handle(ctx, tls.Server(c, b.TLSConfig()))
				if e != nil {
					h.mu.Lock()
					h.errs = append(h.errs, e)
					h.mu.Unlock()
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); h.listener.Close() })
	return h
}
func prepareFile(t testing.TB, name string, data []byte) *Prepared {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if e := os.WriteFile(path, data, 0755); e != nil {
		t.Fatal(e)
	}
	p, e := Prepare(context.Background(), "", []string{path}, "file", "", "")
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func send(t testing.TB, h *harness, p *Prepared) Receipt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, e := h.sender.Send(ctx, h.addr, h.receiverID.Fingerprint, p)
	if e != nil {
		t.Fatal(e)
	}
	if !r.Verified {
		t.Fatal("unverified")
	}
	return r
}
func TestTransferCollisionAndReplay(t *testing.T) {
	h := setup(t, nil)
	data := bytes.Repeat([]byte("relay"), 200000)
	p := prepareFile(t, "Résumé file.txt", data)
	r := send(t, h, p)
	if len(r.Paths) != 1 {
		t.Fatal(r)
	}
	b, e := os.ReadFile(r.Paths[0])
	if e != nil || !bytes.Equal(b, data) {
		t.Fatal("content mismatch", e)
	}
	r2 := send(t, h, p)
	if r2.Paths[0] != r.Paths[0] {
		t.Fatal("replay duplicated transfer")
	}
	p.Offer.ID = NewID()
	r3 := send(t, h, p)
	if r3.Paths[0] == r.Paths[0] || !strings.Contains(r3.Paths[0], "(1)") {
		t.Fatal("did not rename collision")
	}
	i, _ := os.Stat(r.Paths[0])
	if i.Mode().Perm() != 0755 {
		t.Fatal("executable bit lost")
	}
}
func TestDirectoryAndEmpty(t *testing.T) {
	h := setup(t, nil)
	dir := filepath.Join(t.TempDir(), "project")
	if e := os.MkdirAll(filepath.Join(dir, "nested", "empty"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "nested", "empty.txt"), nil, 0644); e != nil {
		t.Fatal(e)
	}
	p, e := Prepare(context.Background(), "", []string{dir}, "directory", "", "")
	if e != nil {
		t.Fatal(e)
	}
	send(t, h, p)
	if i, e := os.Stat(filepath.Join(h.receiveDir, "project", "nested", "empty")); e != nil || !i.IsDir() {
		t.Fatal(e)
	}
	if i, e := os.Stat(filepath.Join(h.receiveDir, "project", "nested", "empty.txt")); e != nil || i.Size() != 0 {
		t.Fatal(e)
	}
}
func TestTextAndPin(t *testing.T) {
	h := setup(t, nil)
	p, e := Prepare(context.Background(), "", nil, "text", "git diff\nhello", "")
	if e != nil {
		t.Fatal(e)
	}
	r := send(t, h, p)
	if r.Text != p.Offer.Text {
		t.Fatal("lost text")
	}
	if _, e = h.sender.Send(context.Background(), h.addr, strings.Repeat("0", 64), p); e == nil {
		t.Fatal("accepted incorrect identity pin")
	}
}
func TestTrustRequired(t *testing.T) {
	h := setup(t, nil)
	h.receiver.opts.Trusted = func(string) bool { return false }
	p := prepareFile(t, "secret", []byte("hello"))
	if _, e := h.sender.Send(context.Background(), h.addr, h.receiverID.Fingerprint, p); e == nil {
		t.Fatal("accepted untrusted sender")
	}
	hello, e := h.sender.Probe(context.Background(), h.addr)
	if e != nil || hello.Fingerprint != h.receiverID.Fingerprint {
		t.Fatal("discovery should work without trust", e)
	}
}
func TestPrepareRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if e := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := Prepare(context.Background(), "", []string{dir}, "file", "", ""); e == nil {
		t.Fatal("accepted symlink")
	}
}
func TestMalformedManifests(t *testing.T) {
	base := Offer{ID: NewID(), Kind: "file", Total: 1, Entries: []Entry{{Path: "ok", Size: 1, Mode: 0644}}}
	for _, name := range []string{"../escape", "/absolute", "a/../b", "a//b", "a\\b", "bad\x1bname", ".relay-part-secret", "a/missing-parent", "."} {
		o := base
		o.Entries = []Entry{{Path: name, Size: 1, Mode: 0644}}
		if e := Validate(o, 100); e == nil {
			t.Errorf("accepted %q", name)
		}
	}
	o := base
	o.Entries = []Entry{{Path: "ok", Size: 1, Mode: 0644}, {Path: "ok", Size: 0}}
	if e := Validate(o, 100); e == nil {
		t.Fatal("duplicate accepted")
	}
	o = base
	o.Entries = []Entry{{Path: "ok", Size: 1 << 62}}
	if e := Validate(o, 100); e == nil {
		t.Fatal("giant size accepted")
	}
}
func TestDestinationSymlinkDoesNotEscape(t *testing.T) {
	h := setup(t, nil)
	p := prepareFile(t, "secret", []byte("incoming"))
	outside := filepath.Join(t.TempDir(), "precious")
	os.WriteFile(outside, []byte("keep"), 0600)
	os.Symlink(outside, filepath.Join(h.receiveDir, "secret"))
	r := send(t, h, p)
	if r.Paths[0] == filepath.Join(h.receiveDir, "secret") {
		t.Fatal("followed link")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "keep" {
		t.Fatal("overwrote outside")
	}
}
func TestResumeAcrossEngineRestart(t *testing.T) {
	h := setup(t, nil)
	data := bytes.Repeat([]byte("resume-test"), 400000)
	p := prepareFile(t, "big.bin", data)
	// Establish a valid transfer, send a prefix, then disconnect like a network loss.
	c, e := dial(context.Background(), h.addr, h.sender.opts.Identity)
	if e != nil {
		t.Fatal(e)
	}
	protocol.WriteJSON(c, protocol.OfferFrame, p.Offer)
	var decision any
	if e = protocol.ReadJSON(c, protocol.DecisionFrame, &decision); e != nil {
		t.Fatal(e)
	}
	var r resume
	if e = protocol.ReadJSON(c, protocol.ResumeFrame, &r); e != nil {
		t.Fatal(e)
	}
	if e = protocol.Write(c, protocol.DataFrame, data[:protocol.ChunkSize]); e != nil {
		t.Fatal(e)
	}
	c.Close()
	// Wait for the server to flush the interrupted partial before replacing engine.
	for deadline := time.Now().Add(3 * time.Second); ; {
		h.receiver.mu.Lock()
		n := len(h.receiver.active)
		h.receiver.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("did not disconnect")
		}
		time.Sleep(time.Millisecond)
	}
	old := h.receiver
	replacement, e := New(old.opts)
	if e != nil {
		t.Fatal(e)
	}
	h.mu.Lock()
	h.receiver = replacement
	h.mu.Unlock()
	key := recordKey(h.sender.opts.Identity.Fingerprint, p.Offer.ID)
	partial := filepath.Join(h.receiveDir, ".relay-part-"+key, "000000.part")
	info, e := os.Stat(partial)
	if e != nil || info.Size() != protocol.ChunkSize {
		t.Fatalf("partial: %v %v", info, e)
	}
	receipt := send(t, h, p)
	b, e := os.ReadFile(receipt.Paths[0])
	if e != nil || !bytes.Equal(b, data) {
		t.Fatal("resumed content mismatch", e)
	}
}
func TestChecksumFailureNeverFinalizes(t *testing.T) {
	h := setup(t, nil)
	p := prepareFile(t, "bad.bin", []byte("original"))
	c, e := dial(context.Background(), h.addr, h.sender.opts.Identity)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	protocol.WriteJSON(c, protocol.OfferFrame, p.Offer)
	var decision any
	protocol.ReadJSON(c, protocol.DecisionFrame, &decision)
	var r resume
	protocol.ReadJSON(c, protocol.ResumeFrame, &r)
	protocol.Write(c, protocol.DataFrame, []byte("tampered"))
	protocol.WriteJSON(c, protocol.FileEndFrame, checksum{strings.Repeat("0", 64)})
	var ack checksum
	if e = protocol.ReadJSON(c, protocol.FileAckFrame, &ack); e == nil {
		t.Fatal("accepted corrupted payload")
	}
	if _, e = os.Stat(filepath.Join(h.receiveDir, "bad.bin")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("corruption finalized")
	}
}
func TestResumePrefixMismatch(t *testing.T) {
	h := setup(t, nil)
	p := prepareFile(t, "prefix.bin", []byte("original"))
	key := recordKey(h.sender.opts.Identity.Fingerprint, p.Offer.ID)
	dir := filepath.Join(h.receiveDir, ".relay-part-"+key)
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "000000.part"), []byte("wrong"), 0600)
	if _, e := h.sender.Send(context.Background(), h.addr, h.receiverID.Fingerprint, p); e == nil || !strings.Contains(e.Error(), "prefix") {
		t.Fatal("accepted corrupt prefix", e)
	}
}
func BenchmarkSHA256(b *testing.B) {
	buf := make([]byte, protocol.ChunkSize)
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	for b.Loop() {
		sha256.Sum256(buf)
	}
}
func BenchmarkSmallFileRoundtrip(b *testing.B) {
	h := setup(b, nil)
	p := prepareFile(b, "tiny.txt", []byte("relay"))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p.Offer.ID = NewID()
		send(b, h, p)
	}
}
func BenchmarkDirectoryTraversal(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 100; i++ {
		name := hex.EncodeToString([]byte{byte(i)})
		os.WriteFile(filepath.Join(dir, name), nil, 0600)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, e := Prepare(context.Background(), "", []string{dir}, "directory", "", ""); e != nil {
			b.Fatal(e)
		}
	}
}

// Opt in to 1 and 3 GiB end-to-end streaming tests (sparse source, real TLS and
// disk destination). CI remains quick. No large payload is retained in memory.
func TestLargeFiles(t *testing.T) {
	if os.Getenv("RELAY_LARGE_TEST") != "1" {
		t.Skip("set RELAY_LARGE_TEST=1 for 1GiB and 3GiB disk/TLS tests")
	}
	for _, size := range []int64{1 << 30, 3 << 30} {
		t.Run(itoa(size>>30)+"GiB", func(t *testing.T) {
			h := setup(t, nil)
			src := filepath.Join(t.TempDir(), "large.bin")
			f, e := os.Create(src)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.Truncate(size); e != nil {
				t.Fatal(e)
			}
			f.Close()
			p, e := Prepare(context.Background(), "", []string{src}, "file", "", "")
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			start := time.Now()
			var peak atomic.Uint64
			sampling := make(chan struct{})
			go func() {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-sampling:
						return
					case <-ticker.C:
						var m runtime.MemStats
						runtime.ReadMemStats(&m)
						if m.HeapAlloc > peak.Load() {
							peak.Store(m.HeapAlloc)
						}
					}
				}
			}()
			defer close(sampling)
			r, e := h.sender.Send(ctx, h.addr, h.receiverID.Fingerprint, p)
			if e != nil {
				t.Fatal(e)
			}
			dest, e := os.Open(r.Paths[0])
			if e != nil {
				t.Fatal(e)
			}
			defer dest.Close()
			n, e := io.Copy(io.Discard, dest)
			if e != nil || n != size {
				t.Fatal(n, e)
			}
			t.Logf("%d GiB in %s: %.1f MiB/s; sampled peak heap %.1f MiB", size>>30, time.Since(start), float64(size)/(1<<20)/time.Since(start).Seconds(), float64(peak.Load())/(1<<20))
		})
	}
}
func itoa(n int64) string {
	if n == 1 {
		return "1"
	}
	return "3"
}

type slowConn struct {
	net.Conn
	rate int64
}

func (c slowConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	if n > 0 {
		time.Sleep(time.Duration(int64(n) * int64(time.Second) / c.rate))
	}
	return n, e
}
func TestSlowConnection(t *testing.T) {
	h := setup(t, nil)
	h.mu.Lock()
	h.wrap = func(c net.Conn) net.Conn { return slowConn{c, 2 << 20} }
	h.mu.Unlock()
	data := bytes.Repeat([]byte("slow"), 1<<18)
	p := prepareFile(t, "slow.bin", data)
	start := time.Now()
	r := send(t, h, p)
	if time.Since(start) < 500*time.Millisecond {
		t.Fatal("limiter did not run")
	}
	b, e := os.ReadFile(r.Paths[0])
	if e != nil || !bytes.Equal(b, data) {
		t.Fatal("slow transfer mismatch", e)
	}
	t.Logf("1MiB over simulated 2MiB/s connection: %s", time.Since(start))
}
func TestClipboardContentNotRetainedInJournal(t *testing.T) {
	h := setup(t, nil)
	text := "private clipboard sentinel"
	p, e := Prepare(context.Background(), "", nil, "text", text, "")
	if e != nil {
		t.Fatal(e)
	}
	send(t, h, p)
	key := recordKey(h.sender.opts.Identity.Fingerprint, p.Offer.ID)
	b, e := os.ReadFile(filepath.Join(h.receiver.opts.StateDir, "incoming", key+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte(text)) {
		t.Fatal("clipboard retained in journal")
	}
	if r := send(t, h, p); r.Text != text {
		t.Fatal("replay lost text")
	}
}
func TestCrashAfterPublicationRecovered(t *testing.T) {
	h := setup(t, nil)
	data := []byte("committed before crash")
	p := prepareFile(t, "recovery.bin", data)
	key := recordKey(h.sender.opts.Identity.Fingerprint, p.Offer.ID)
	rec, e := h.receiver.loadRecord(key, p.Offer)
	if e != nil {
		t.Fatal(e)
	}
	rec.Names["recovery.bin"] = "recovery.bin"
	hash := sha256.Sum256(data)
	rec.Checksums["recovery.bin"] = hex.EncodeToString(hash[:])
	if e = h.receiver.saveRecord(key, rec); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(h.receiveDir, "recovery.bin"), data, 0600); e != nil {
		t.Fatal(e)
	}
	r := send(t, h, p)
	if len(r.Paths) != 1 || filepath.Base(r.Paths[0]) != "recovery.bin" {
		t.Fatal("crash replay duplicated or lost file")
	}
}
func TestCollisionPolicies(t *testing.T) {
	for _, policy := range []string{"skip", "cancel", "replace"} {
		t.Run(policy, func(t *testing.T) {
			h := setup(t, nil)
			h.receiver.opts.Conflict = policy
			p := prepareFile(t, "existing", []byte("new"))
			dest := filepath.Join(h.receiveDir, "existing")
			os.WriteFile(dest, []byte("old"), 0600)
			r, e := h.sender.Send(context.Background(), h.addr, h.receiverID.Fingerprint, p)
			if policy == "cancel" && e == nil {
				t.Fatal("collision should cancel")
			}
			if policy != "cancel" && e != nil {
				t.Fatal(e)
			}
			b, _ := os.ReadFile(dest)
			if policy == "replace" && string(b) != "new" {
				t.Fatal("replace failed")
			}
			if policy != "replace" && string(b) != "old" {
				t.Fatal("overwrote collision")
			}
			if policy == "skip" && len(r.Paths) != 0 {
				t.Fatal("skip incorrectly reported a received path")
			}
		})
	}
}

func TestTrustRevocationInterruptsPayload(t *testing.T) {
	var trusted atomic.Bool
	trusted.Store(true)
	h := setup(t, func(e Event) {
		if e.Status == "transferring" {
			trusted.Store(false)
		}
	})
	h.receiver.opts.Trusted = func(string) bool { return trusted.Load() }
	p := prepareFile(t, "revoked.bin", make([]byte, 2<<20))
	if _, e := h.sender.Send(context.Background(), h.addr, h.receiverID.Fingerprint, p); e == nil {
		t.Fatal("transfer continued after trust revocation")
	}
	if _, e := os.Stat(filepath.Join(h.receiveDir, "revoked.bin")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("revoked transfer finalized")
	}
}
func TestCancelInterruptsPayload(t *testing.T) {
	h := setup(t, nil)
	var once sync.Once
	h.receiver.opts.Progress = func(e Event) {
		if e.Status == "transferring" {
			once.Do(func() { h.receiver.Cancel(e.ID) })
		}
	}
	p := prepareFile(t, "cancelled.bin", make([]byte, 2<<20))
	if _, e := h.sender.Send(context.Background(), h.addr, h.receiverID.Fingerprint, p); e == nil {
		t.Fatal("cancel did not interrupt")
	}
	if _, e := os.Stat(filepath.Join(h.receiveDir, "cancelled.bin")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("cancelled transfer finalized")
	}
}
func TestMutatingSourceNeverFinalizes(t *testing.T) {
	h := setup(t, nil)
	p := prepareFile(t, "changing.bin", make([]byte, 2<<20))
	var once sync.Once
	h.sender.opts.Progress = func(e Event) {
		if e.Status == "transferring" {
			once.Do(func() { src := p.Sources["changing.bin"]; _ = os.Chtimes(src, time.Now(), time.Now().Add(time.Second)) })
		}
	}
	if _, e := h.sender.Send(context.Background(), h.addr, h.receiverID.Fingerprint, p); e == nil || !strings.Contains(e.Error(), "source changed") {
		t.Fatal("changing source was not rejected", e)
	}
	if _, e := os.Stat(filepath.Join(h.receiveDir, "changing.bin")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("changing source finalized")
	}
}

func TestCancelDoesNotWaitForStalledTLSCloseNotify(t *testing.T) {
	a, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	client := tls.Client(left, a.TLSConfig())
	serverConfig := b.TLSConfig()
	serverConfig.SessionTicketsDisabled = true
	server := tls.Server(right, serverConfig)
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	handshake := make(chan error, 1)
	go func() { handshake <- server.HandshakeContext(ctx) }()
	if err = client.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-handshake; err != nil {
		t.Fatal(err)
	}
	// The peer deliberately performs no further reads. A graceful TLS Close would
	// wait trying to write its close-notify record; pause/cancel must close raw IO.
	engine := &Engine{active: map[string]net.Conn{"paused": client}}
	start := time.Now()
	if !engine.Cancel("paused") {
		t.Fatal("active transfer missing")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel waited on stalled TLS peer")
	}
}
