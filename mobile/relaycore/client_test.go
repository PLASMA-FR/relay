package relaycore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

func loopback(a netip.Addr) bool { return a.IsLoopback() }
func testClient(t *testing.T) *Client {
	t.Helper()
	base := t.TempDir()
	c, e := newClient(filepath.Join(base, "state"), filepath.Join(base, "inbox"), "Phone", nil, loopback)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetTrustTailnet(false); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close() })
	if e = c.startAddress("127.0.0.1:0"); e != nil {
		t.Fatal(e)
	}
	return c
}
func snap(t *testing.T, c *Client) snapshot {
	t.Helper()
	var s snapshot
	if e := json.Unmarshal([]byte(c.Snapshot()), &s); e != nil {
		t.Fatal(e)
	}
	return s
}
func await(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func jobStatus(t *testing.T, c *Client, id string) string {
	t.Helper()
	s := snap(t, c)
	for _, list := range [][]model.Transfer{s.Transfers, s.History} {
		for _, v := range list {
			if v.ID == id {
				return v.Status
			}
		}
	}
	return ""
}
func sendID(t *testing.T, c *Client, peer, kind, text string, paths []string) string {
	t.Helper()
	b, _ := json.Marshal(paths)
	out, e := c.Send(peer, kind, text, string(b))
	if e != nil {
		t.Fatal(e)
	}
	var r model.Result
	if e = json.Unmarshal([]byte(out), &r); e != nil {
		t.Fatal(e)
	}
	return r.ID
}

type desktop struct {
	engine         *transfer.Engine
	id             *identity.Identity
	address, inbox string
	cancel         context.CancelFunc
	listener       net.Listener
	wg             sync.WaitGroup
	trust          atomic.Bool
	progress       func(transfer.Event)
}

func testDesktop(t *testing.T, c *Client) *desktop {
	t.Helper()
	base := t.TempDir()
	id, e := identity.LoadOrCreate(filepath.Join(base, "state"))
	if e != nil {
		t.Fatal(e)
	}
	d := &desktop{id: id, inbox: filepath.Join(base, "inbox")}
	d.trust.Store(true)
	d.engine, e = transfer.New(transfer.Options{Identity: id, StateDir: filepath.Join(base, "state"), ReceiveDir: d.inbox, Hello: protocol.Hello{Name: "Desktop", OS: "linux", Version: model.Version}, Trusted: func(fp string) bool { return d.trust.Load() && fp == c.identity.Fingerprint }, Decide: func(context.Context, string, transfer.Offer) error { return nil }, Progress: func(e transfer.Event) {
		if d.progress != nil {
			d.progress(e)
		}
	}})
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	d.listener = l
	d.address = l.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			conn, e := l.Accept()
			if e != nil {
				return
			}
			d.wg.Add(1)
			go func() { defer d.wg.Done(); _ = d.engine.Handle(ctx, tls.Server(conn, id.TLSConfig())) }()
		}
	}()
	t.Cleanup(func() { cancel(); l.Close(); d.wg.Wait() })
	return d
}
func pairDesktop(t *testing.T, c *Client, d *desktop) model.Peer {
	t.Helper()
	out, e := c.AddDevice(d.address)
	if e != nil {
		t.Fatal(e)
	}
	var p model.Peer
	if e = json.Unmarshal([]byte(out), &p); e != nil {
		t.Fatal(e)
	}
	if p.Trusted {
		t.Fatal("discovery granted trust")
	}
	if e = c.Trust(p.ID, p.Fingerprint); e != nil {
		t.Fatal(e)
	}
	return p
}
func desktopSend(t *testing.T, c *Client, d *desktop, kind, text string, paths []string) (string, <-chan error) {
	t.Helper()
	p, e := transfer.Prepare(context.Background(), "", paths, kind, text, "")
	if e != nil {
		t.Fatal(e)
	}
	address := snap(t, c).Address
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_, e := d.engine.Send(ctx, address, c.identity.Fingerprint, p)
		done <- e
	}()
	return p.Offer.ID, done
}

func TestDesktopRoundTripApprovalClipboardAndRestart(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	p := pairDesktop(t, c, d)
	content := bytes.Repeat([]byte("hello from phone\n"), 8192)
	source := filepath.Join(c.stateDir, "staging", "notes.txt")
	if e := os.WriteFile(source, content, 0600); e != nil {
		t.Fatal(e)
	}
	id := sendID(t, c, p.ID, "file", "", []string{source})
	await(t, func() bool { return jobStatus(t, c, id) == "completed" })
	received, e := os.ReadFile(filepath.Join(d.inbox, "notes.txt"))
	if e != nil || !bytes.Equal(content, received) {
		t.Fatalf("file roundtrip: %v", e)
	}
	s := snap(t, c)
	if s.History[0].Destination != "" {
		t.Fatal("remote path leaked into mobile destination")
	}
	secret := "private text only in Relay clipboard"
	incoming, done := desktopSend(t, c, d, "text", secret, nil)
	await(t, func() bool { return jobStatus(t, c, incoming) == "offered" })
	if snap(t, c).Clipboard != "" {
		t.Fatal("unapproved text was published")
	}
	if _, e = c.Action("accept", incoming); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	await(t, func() bool { return snap(t, c).Clipboard == secret })
	if snap(t, c).ClipboardID != incoming {
		t.Fatal("clipboard transfer identity missing")
	}
	stored, e := os.ReadFile(filepath.Join(c.stateDir, "mobile.json"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(stored, []byte(secret)) {
		t.Fatal("history contains clipboard content")
	}
	fp := c.identity.Fingerprint
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	s = snap(t, reopened)
	if s.Fingerprint != fp || s.Clipboard != secret || s.ClipboardID != incoming || !s.Peers[0].Trusted || s.Running {
		t.Fatalf("state did not survive restart: %+v", s)
	}
}

func TestAcceptedOfferReplayAfterRestartAndManifestBinding(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	pairDesktop(t, c, d)
	text := "approved exact content"
	id, done := desktopSend(t, c, d, "text", text, nil)
	await(t, func() bool { return jobStatus(t, c, id) == "offered" })
	if _, err := c.Action("accept", id); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.startAddress("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	prepared, err := transfer.Prepare(context.Background(), id, nil, "text", text, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = d.engine.Send(ctx, snap(t, reopened).Address, reopened.identity.Fingerprint, prepared); err != nil {
		t.Fatal("accepted replay prompted again", err)
	}
	prepared.Offer.Text = "changed"
	prepared.Offer.Total = int64(len("changed"))
	if _, err = d.engine.Send(ctx, snap(t, reopened).Address, reopened.identity.Fingerprint, prepared); err == nil {
		t.Fatal("approval was reused for different content")
	}
	if snap(t, reopened).Clipboard != text || jobStatus(t, reopened, id) != "completed" {
		t.Fatal("rejected replay changed approved state")
	}
}

func TestCancelQueuedTextIsDurableAndCannotResume(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	p := pairDesktop(t, c, d)
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	text := "discard cancelled queue content"
	id := sendID(t, c, p.ID, "text", text, nil)
	if _, err := c.Action("cancel", id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Action("resume", id); err == nil {
		t.Fatal("cancelled job resumed")
	}
	b, err := os.ReadFile(filepath.Join(c.stateDir, "mobile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(text)) {
		t.Fatal("cancelled history retained payload")
	}
	c.Close()
	reopened, err := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if jobStatus(t, reopened, id) != "cancelled" {
		t.Fatal("cancel lost after restart")
	}
}

func TestIncomingFilesRejectAndAutoAccept(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	pairDesktop(t, c, d)
	id, done := desktopSend(t, c, d, "text", "rejected content", nil)
	await(t, func() bool { return jobStatus(t, c, id) == "offered" })
	if _, e := c.Action("reject", id); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("rejected send succeeded")
	}
	if snap(t, c).Clipboard != "" || jobStatus(t, c, id) != "rejected" {
		t.Fatal("rejection did not preserve inbox")
	}
	if e := c.SetAutoAccept(true); e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(t.TempDir(), "desktop.txt")
	os.WriteFile(source, []byte("from desktop"), 0600)
	id, done = desktopSend(t, c, d, "file", "", []string{source})
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	await(t, func() bool { return jobStatus(t, c, id) == "completed" })
	data, e := os.ReadFile(filepath.Join(c.inbox, "desktop.txt"))
	if e != nil || string(data) != "from desktop" {
		t.Fatal("incoming file missing", e)
	}
	s := snap(t, c)
	for _, h := range s.History {
		if h.ID == id && (len(h.Paths) != 1 || !within(c.inbox, h.Paths[0])) {
			t.Fatal("inbox path missing")
		}
	}
}

func TestPauseResumeActualPartialAndQueueRestart(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	p := pairDesktop(t, c, d)
	source := filepath.Join(c.stateDir, "staging", "large.bin")
	data := bytes.Repeat([]byte("abc12345"), 2<<20)
	if e := os.WriteFile(source, data, 0600); e != nil {
		t.Fatal(e)
	}
	var paused atomic.Bool
	d.progress = func(e transfer.Event) {
		if e.Direction == "receive" && e.Status == "transferring" && e.Bytes > 0 && paused.CompareAndSwap(false, true) {
			if _, err := c.Action("pause", e.ID); err != nil {
				t.Errorf("pause: %v", err)
			}
		}
	}
	id := sendID(t, c, p.ID, "file", "", []string{source})
	await(t, func() bool { return jobStatus(t, c, id) == "paused" })
	if e := c.Stop(); e != nil {
		t.Fatal(e)
	}
	matches, e := filepath.Glob(filepath.Join(d.inbox, ".relay-part-*", "*.part"))
	if e != nil || len(matches) == 0 {
		t.Fatal("expected actual partial file", e)
	}
	st, e := os.Stat(matches[0])
	if e != nil || st.Size() <= 0 || st.Size() >= int64(len(data)) {
		t.Fatal("transfer did not pause mid-payload", e)
	}
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if jobStatus(t, reopened, id) != "paused" {
		t.Fatal("pause lost on restart")
	}
	if e = reopened.startAddress("127.0.0.1:0"); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.Action("resume", id); e != nil {
		t.Fatal(e)
	}
	await(t, func() bool { return jobStatus(t, reopened, id) == "completed" })
	got, e := os.ReadFile(filepath.Join(d.inbox, "large.bin"))
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("resume content mismatch", e)
	}
}

func TestUntrustedWrongPinAndRevocation(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	out, e := c.AddDevice(d.address)
	if e != nil {
		t.Fatal(e)
	}
	var p model.Peer
	json.Unmarshal([]byte(out), &p)
	if _, e = c.Send(p.ID, "text", "no trust", "[]"); e == nil {
		t.Fatal("untrusted send accepted")
	}
	wrong := strings.Repeat("a", 64)
	if wrong == p.Fingerprint {
		wrong = strings.Repeat("b", 64)
	}
	if e = c.Trust(p.ID, wrong); e == nil {
		t.Fatal("wrong trust pin accepted")
	}
	if _, e = c.addDevice(d.address, wrong); e == nil {
		t.Fatal("wrong invite pin accepted")
	}
	if e = c.Trust(p.ID, p.Fingerprint); e != nil {
		t.Fatal(e)
	}
	incoming, done := desktopSend(t, c, d, "text", "not after revoke", nil)
	await(t, func() bool { return jobStatus(t, c, incoming) == "offered" })
	if e = c.Untrust(p.ID); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("revoked active transfer succeeded")
	}
	if snap(t, c).Clipboard != "" {
		t.Fatal("revoked text published")
	}
	_, done = desktopSend(t, c, d, "text", "untrusted", nil)
	if e = <-done; e == nil {
		t.Fatal("untrusted receiver accepted sender")
	}
	// The existing engine pins the TLS server before sending an offer.
	prep, e := transfer.Prepare(context.Background(), "", nil, "text", "pin test", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.engine.Send(context.Background(), snap(t, c).Address, wrong, prep); e == nil || !strings.Contains(e.Error(), "identity changed") {
		t.Fatal("wrong TLS pin accepted", e)
	}
}

func TestTrustMustCommitBeforeAuthorizationAndPathConfinement(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	out, e := c.AddDevice(d.address)
	if e != nil {
		t.Fatal(e)
	}
	var p model.Peer
	json.Unmarshal([]byte(out), &p)
	stateFile := filepath.Join(c.stateDir, "mobile.json")
	if e = os.Remove(stateFile); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(stateFile, 0700); e != nil {
		t.Fatal(e)
	}
	if e = c.Trust(p.ID, p.Fingerprint); e == nil {
		t.Fatal("trust succeeded without durable commit")
	}
	if c.trusted(p.Fingerprint) {
		t.Fatal("failed trust enabled engine")
	}
	os.Remove(stateFile)
	if e = c.Trust(p.ID, p.Fingerprint); e != nil {
		t.Fatal(e)
	}
	secret := filepath.Join(c.stateDir, "identity.pem")
	b, _ := json.Marshal([]string{secret})
	if _, e = c.Send(p.ID, "file", "", string(b)); e == nil {
		t.Fatal("private identity accepted as source")
	}
	link := filepath.Join(c.stateDir, "staging", "link")
	if e = os.Symlink(filepath.Dir(secret), link); e != nil {
		t.Fatal(e)
	}
	b, _ = json.Marshal([]string{filepath.Join(link, "identity.pem")})
	if _, e = c.Send(p.ID, "file", "", string(b)); e == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestPublicNetworkBoundaryAndLifecycle(t *testing.T) {
	base := t.TempDir()
	c, e := NewClient(filepath.Join(base, "state"), filepath.Join(base, "inbox"), "Phone", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, bad := range []string{"127.0.0.1", "0.0.0.0", "::", "192.168.1.2", "example.com", "100.64.0.1:7331", "fd7a:115c:a1e0::1%eth0"} {
		if e = c.Start(bad); e == nil {
			t.Fatalf("unsafe bind accepted: %s", bad)
		}
	}
	for _, bad := range []string{"127.0.0.1:7331", "example.com:7331", "100.64.0.1:0", "100.64.0.1:080", "[::ffff:100.64.0.1]:7331"} {
		if _, e = c.AddDevice(bad); e == nil {
			t.Fatalf("unsafe peer accepted: %s", bad)
		}
	}
	if _, e = c.Invite(); e == nil {
		t.Fatal("offline invite created")
	}
	if e = c.Start(""); e != nil {
		t.Fatal(e)
	}
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Send("missing", "text", "hello", "[]"); e == nil {
		t.Fatal("closed send allowed")
	}
	local := testClient(t)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 8; k++ {
				_ = local.Snapshot()
				_ = local.Stop()
				_ = local.startAddress("127.0.0.1:0")
			}
		}()
	}
	wg.Wait()
	if e = local.Close(); e != nil {
		t.Fatal(e)
	}
}

type reentrantObserver struct {
	client atomic.Pointer[Client]
	calls  atomic.Int32
}

func (o *reentrantObserver) OnChange(string) {
	if c := o.client.Load(); c != nil {
		_ = c.Snapshot()
		o.calls.Add(1)
	}
}
func TestObserverReentrantAndCoalesced(t *testing.T) {
	base := t.TempDir()
	o := &reentrantObserver{}
	c, e := newClient(filepath.Join(base, "state"), filepath.Join(base, "inbox"), "Phone", o, loopback)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	o.client.Store(c)
	c.mu.Lock()
	for n := 0; n < 1000; n++ {
		c.changedLocked()
	}
	c.mu.Unlock()
	await(t, func() bool { return o.calls.Load() > 0 })
	time.Sleep(150 * time.Millisecond)
	if o.calls.Load() > 3 {
		t.Fatal("observer was not coalesced")
	}
}

func TestCorruptStateNeverRegeneratesIdentity(t *testing.T) {
	c := testClient(t)
	fp := c.identity.Fingerprint
	c.Close()
	os.WriteFile(filepath.Join(c.stateDir, "mobile.json"), []byte("broken"), 0600)
	if _, e := newClient(c.stateDir, c.inbox, "Phone", nil, loopback); e == nil {
		t.Fatal("corrupt state silently reset")
	}
	id, e := identity.LoadOrCreate(c.stateDir)
	if e != nil || id.Fingerprint != fp {
		t.Fatal("identity changed", e)
	}
	if _, e = os.Stat(filepath.Join(c.stateDir, "identity.pem")); errors.Is(e, os.ErrNotExist) {
		t.Fatal("identity deleted")
	}
}

func TestStagedCopiesReleasedOnlyAfterAllJobsFinish(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	peer := pairDesktop(t, c, d)
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	batch := filepath.Join(c.stateDir, "staging", "shared-batch")
	if err := os.Mkdir(batch, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(batch, "shared.txt")
	if err := os.WriteFile(source, []byte("shared source"), 0600); err != nil {
		t.Fatal(err)
	}
	first := sendID(t, c, peer.ID, "file", "", []string{source})
	second := sendID(t, c, peer.ID, "file", "", []string{source})
	if _, err := c.Action("pause", second); err != nil {
		t.Fatal(err)
	}
	if err := c.startAddress("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return jobStatus(t, c, first) == "completed" })
	if _, err := os.Stat(source); err != nil {
		t.Fatal("completed job deleted another paused job's source", err)
	}
	if _, err := c.Action("resume", second); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return jobStatus(t, c, second) == "completed" })
	await(t, func() bool { _, err := os.Stat(source); return errors.Is(err, os.ErrNotExist) })
	if _, err := os.Stat(batch); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty staging batch retained", err)
	}
	// Received files belong to the user's inbox even when forwarded successfully.
	inboxSource := filepath.Join(c.inbox, "keep.txt")
	if err := os.WriteFile(inboxSource, []byte("keep received original"), 0600); err != nil {
		t.Fatal(err)
	}
	forwarded := sendID(t, c, peer.ID, "file", "", []string{inboxSource})
	await(t, func() bool { return jobStatus(t, c, forwarded) == "completed" })
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inboxSource); err != nil {
		t.Fatal("inbox forwarding deleted original", err)
	}
}

func TestInterruptedPreservesStagingAndCancelledWorkerReleases(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	peer := pairDesktop(t, c, d)
	source := filepath.Join(c.stateDir, "staging", "retry.txt")
	if err := os.WriteFile(source, []byte("retry data"), 0600); err != nil {
		t.Fatal(err)
	}
	// Remembered endpoint remains queued/retryable when the remote listener goes away.
	d.listener.Close()
	id := sendID(t, c, peer.ID, "file", "", []string{source})
	await(t, func() bool { return jobStatus(t, c, id) == "interrupted" })
	if _, err := os.Stat(source); err != nil {
		t.Fatal("interrupted job lost source", err)
	}
	if _, err := c.Action("cancel", id); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled source retained after worker stopped", err)
	}
}

func TestCleanupProtectsActivePreparationAndSymlinkAncestors(t *testing.T) {
	c := testClient(t)
	c.Stop()
	source := filepath.Join(c.stateDir, "staging", "preparing.txt")
	if err := os.WriteFile(source, []byte("still in use"), 0600); err != nil {
		t.Fatal(err)
	}
	id := transfer.NewID()
	c.mu.Lock()
	c.state.Jobs[id] = &job{Transfer: model.Transfer{ID: id, Direction: "send", Status: "completed", Paths: []string{source}, Updated: time.Now()}}
	c.preparing["new-send"] = []string{source}
	if err := c.persistLocked(); err != nil {
		t.Fatal(err)
	}
	c.cleanupStagingLocked()
	c.mu.Unlock()
	if _, err := os.Stat(source); err != nil {
		t.Fatal("active preparation lost source", err)
	}
	c.mu.Lock()
	delete(c.preparing, "new-send")
	c.cleanupStagingLocked()
	c.mu.Unlock()
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("finished source retained", err)
	}
	outside := t.TempDir()
	protected := filepath.Join(outside, "secret")
	os.WriteFile(protected, []byte("not staged"), 0600)
	link := filepath.Join(c.stateDir, "staging", "replaced-batch")
	os.Symlink(outside, link)
	c.mu.Lock()
	c.state.Jobs[id].Transfer.Paths = []string{filepath.Join(link, "secret")}
	if err := c.persistLocked(); err != nil {
		t.Fatal(err)
	}
	c.cleanupStagingLocked()
	c.mu.Unlock()
	if _, err := os.Stat(protected); err != nil {
		t.Fatal("cleanup followed symlink ancestor", err)
	}
}

func TestCleanupRetainsFailedAndActiveCancelledSources(t *testing.T) {
	c := testClient(t)
	c.Stop()
	for _, status := range []string{"failed", "paused", "interrupted", "queued", "cancelled"} {
		source := filepath.Join(c.stateDir, "staging", status+".txt")
		os.WriteFile(source, []byte("retain while needed"), 0600)
		id := transfer.NewID()
		c.mu.Lock()
		c.state.Jobs[id] = &job{Transfer: model.Transfer{ID: id, Direction: "send", Status: status, Paths: []string{source}, Updated: time.Now()}}
		if status == "cancelled" {
			c.cancels[id] = func() {}
		}
		if err := c.persistLocked(); err != nil {
			t.Fatal(err)
		}
		c.cleanupStagingLocked()
		c.mu.Unlock()
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("%s source removed while retained: %v", status, err)
		}
		if status == "cancelled" {
			c.mu.Lock()
			delete(c.cancels, id)
			c.cleanupStagingLocked()
			c.mu.Unlock()
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("released cancelled worker retained staged source", err)
			}
		}
	}
}

func TestUntrustPersistenceFailureExplainsSessionOnlyRevocation(t *testing.T) {
	c := testClient(t)
	d := testDesktop(t, c)
	peer := pairDesktop(t, c, d)
	stateFile := filepath.Join(c.stateDir, "mobile.json")
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateFile, 0700); err != nil {
		t.Fatal(err)
	}
	err := c.Untrust(peer.ID)
	if err == nil || !strings.Contains(err.Error(), "this session only") || !strings.Contains(err.Error(), "before restarting") {
		t.Fatal("missing revocation durability explanation", err)
	}
	if c.trusted(peer.Fingerprint) {
		t.Fatal("failed durable revocation retained runtime authorization")
	}
	c.mu.Lock()
	for n := 0; n < 12; n++ {
		c.noticeLocked(errors.New("unrelated storage notice"))
	}
	c.mu.Unlock()
	notices := snap(t, c).Notices
	if len(notices) == 0 || !strings.Contains(notices[len(notices)-1], "repair storage") {
		t.Fatal("durability notice was not exposed to UI")
	}
	if err = os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err = c.Untrust(peer.ID); err != nil {
		t.Fatal(err)
	}
	for _, notice := range snap(t, c).Notices {
		if strings.Contains(notice, "this session only") {
			t.Fatal("repaired storage retained stale revocation warning")
		}
	}
	c.Close()
	reopened, err := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.trusted(peer.Fingerprint) {
		t.Fatal("retried revocation was not durable")
	}
}
