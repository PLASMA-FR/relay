package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

func testConfig(t *testing.T, name string) config.Config {
	t.Helper()
	root, err := os.MkdirTemp("", "relay-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	c := config.Default()
	c.Name = name
	c.Paths = config.Paths{ConfigFile: filepath.Join(root, "config", "config.toml"), StateDir: filepath.Join(root, "state"), CacheDir: filepath.Join(root, "cache"), Socket: filepath.Join(root, "run", "relay.sock")}
	c.Receive.Directory = filepath.Join(root, "inbox")
	c.Network.TailscaleOnly = false
	c.Network.TrustTailnet = false
	c.Network.Listen = "127.0.0.1:0"
	return c
}
func startDaemon(t *testing.T, c config.Config) (*Daemon, *http.Client) {
	t.Helper()
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Paths.Socket)
	}}}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, e := client.Get("http://relay/v1/status")
		if e == nil {
			r.Body.Close()
			d.mu.Lock()
			ready := d.address != ""
			d.mu.Unlock()
			if ready {
				return d, client
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
	return nil, nil
}
func connectDaemons(a, b *Daemon) {
	sa, sb := a.snapshot(), b.snapshot()
	a.mu.Lock()
	a.peers["b"] = model.Peer{ID: "b", Name: sb.Name, Address: sb.Address, Online: true, Relay: true, Fingerprint: sb.Fingerprint, Protocol: 1}
	a.trust["b"] = sb.Fingerprint
	a.mu.Unlock()
	b.mu.Lock()
	b.peers["a"] = model.Peer{ID: "a", Name: sa.Name, Address: sa.Address, Online: true, Relay: true, Fingerprint: sa.Fingerprint, Protocol: 1}
	b.trust["a"] = sa.Fingerprint
	b.mu.Unlock()
}
func waitTransfer(t *testing.T, d *Daemon, id, status string) model.Transfer {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last model.Transfer
	for time.Now().Before(deadline) {
		s := d.snapshot()
		for _, tr := range append(s.Transfers, s.History...) {
			if tr.ID == id {
				last = tr
				if tr.Status == status {
					return tr
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transfer did not reach %s: %+v", status, last)
	return last
}
func TestDaemonsTransferDirectoryClipboardAndCollision(t *testing.T) {
	a, client := startDaemon(t, testConfig(t, "desktop"))
	b, _ := startDaemon(t, testConfig(t, "laptop"))
	connectDaemons(a, b)
	dir := filepath.Join(t.TempDir(), "project 漢字")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("hello Relay\n"), 10000)
	if err := os.WriteFile(filepath.Join(dir, "a file.txt"), content, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(model.SendRequest{Peer: "laptop", Paths: []string{dir}})
	r, err := client.Post("http://relay/v1/send", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var result model.Result
	if err = json.NewDecoder(r.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 202 {
		t.Fatalf("send status %d", r.StatusCode)
	}
	tr := waitTransfer(t, a, result.ID, "completed")
	if !tr.Verified {
		t.Fatal("not verified")
	}
	received, err := os.ReadFile(filepath.Join(b.cfg.Receive.Directory, filepath.Base(dir), "a file.txt"))
	if err != nil || !bytes.Equal(received, content) {
		t.Fatalf("received: %v", err)
	}
	result, err = a.send(model.SendRequest{Peer: "laptop", Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, a, result.ID, "completed")
	if _, err = os.Stat(filepath.Join(b.cfg.Receive.Directory, "project 漢字 (1)", "empty")); err != nil {
		t.Fatal(err)
	}
	result, err = a.send(model.SendRequest{Peer: "laptop", Kind: "text", Text: "from an SSH session"})
	if err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, a, result.ID, "completed")
	waitTransfer(t, b, result.ID, "completed")
	text, err := b.clipboard.ReadInternal()
	if err != nil || text != "from an SSH session" {
		t.Fatalf("clipboard %q %v", text, err)
	}
}
func TestTrustApprovalAndRevocation(t *testing.T) {
	a, _ := startDaemon(t, testConfig(t, "desktop"))
	c := testConfig(t, "laptop")
	c.Receive.AutoAcceptTrusted = false
	b, _ := startDaemon(t, c)
	connectDaemons(a, b)
	req := &http.Request{}
	req = req.WithContext(context.Background())
	if _, err := a.action(req, model.Action{Action: "trust", Peer: "laptop", Fingerprint: "bad"}); err == nil {
		t.Fatal("accepted short fingerprint")
	}
	result, err := a.send(model.SendRequest{Peer: "laptop", Kind: "text", Text: "needs approval"})
	if err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, b, result.ID, "offered")
	if _, err = b.action(req, model.Action{Action: "accept", ID: result.ID}); err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, a, result.ID, "completed")
	if _, err = a.action(req, model.Action{Action: "untrust", Peer: "laptop"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.send(model.SendRequest{Peer: "laptop", Kind: "text", Text: "denied"}); err == nil {
		t.Fatal("send to untrusted device succeeded")
	}
}
func TestPrivateIPCAndMalformedRequests(t *testing.T) {
	d, client := startDaemon(t, testConfig(t, "test"))
	info, err := os.Stat(d.cfg.Paths.Socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v", err)
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, strings.Repeat("x", 2<<20+1)} {
		r, err := client.Post("http://relay/v1/send", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Errorf("malformed request status %d", r.StatusCode)
		}
	}
}
func TestStateRecovery(t *testing.T) {
	c := testConfig(t, "test")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.jobs["job"] = &job{Transfer: model.Transfer{ID: "job", Status: "transferring", Direction: "send"}}
	d.trust["peer"] = "fingerprint"
	if err = d.persist(); err != nil {
		t.Fatal(err)
	}
	again, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if again.jobs["job"].Transfer.Status != "interrupted" || again.trust["peer"] != "fingerprint" {
		t.Fatal("state not recovered")
	}
}
func TestRefuseForeignSocketPath(t *testing.T) {
	c := testConfig(t, "test")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(c.Paths.Socket), 0700)
	os.WriteFile(c.Paths.Socket, []byte("valuable"), 0600)
	if _, err = d.openSocket(); err == nil {
		t.Fatal("replaced ordinary file")
	}
	b, _ := os.ReadFile(c.Paths.Socket)
	if string(b) != "valuable" {
		t.Fatal("file changed")
	}
}

func TestQueuedSendRecoversAfterRestart(t *testing.T) {
	b, _ := startDaemon(t, testConfig(t, "laptop"))
	c := testConfig(t, "desktop")
	before, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	connectDaemons(before, b)
	before.mu.Lock()
	p := before.peers["b"]
	p.Online = false
	p.Relay = false
	before.peers["b"] = p
	before.mu.Unlock()
	queued, err := before.send(model.SendRequest{Peer: "laptop", Kind: "text", Text: "survives daemon restart"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := startDaemon(t, c)
	connectDaemons(a, b)
	signal(a.wake)
	waitTransfer(t, a, queued.ID, "completed")
	waitTransfer(t, b, queued.ID, "completed")
	text, err := b.clipboard.ReadInternal()
	if err != nil || text != "survives daemon restart" {
		t.Fatalf("clipboard %q: %v", text, err)
	}
}

func TestInterruptedUnacceptedOfferStillRequiresApproval(t *testing.T) {
	c := testConfig(t, "laptop")
	c.Receive.AutoAcceptTrusted = false
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.peers["sender"] = model.Peer{ID: "sender", Name: "desktop", Fingerprint: "fingerprint"}
	d.trust["sender"] = "fingerprint"
	id := newID()
	d.jobs[id] = &job{Fingerprint: "fingerprint", Transfer: model.Transfer{ID: id, Direction: "receive", Status: "interrupted"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = d.decide(ctx, "fingerprint", transfer.Offer{ID: id, Name: "text", Kind: "text", Text: "needs permission", Total: 16})
	if err == nil {
		t.Fatal("unaccepted offer bypassed manual approval after interruption")
	}
	if d.jobs[id].Accepted {
		t.Fatal("offer became accepted")
	}
}

func TestRejectEndsBothTransfers(t *testing.T) {
	a, _ := startDaemon(t, testConfig(t, "desktop"))
	c := testConfig(t, "laptop")
	c.Receive.AutoAcceptTrusted = false
	b, _ := startDaemon(t, c)
	connectDaemons(a, b)
	result, err := a.send(model.SendRequest{Peer: "laptop", Kind: "text", Text: "reject me"})
	if err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, b, result.ID, "offered")
	req := (&http.Request{}).WithContext(context.Background())
	if _, err = b.action(req, model.Action{Action: "reject", ID: result.ID}); err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, a, result.ID, "rejected")
	waitTransfer(t, b, result.ID, "rejected")
}

func TestEventStreamDeliversInitialAndChangedSnapshots(t *testing.T) {
	d, client := startDaemon(t, testConfig(t, "test"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://relay/v1/events", nil)
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	var s model.Snapshot
	if err = dec.Decode(&s); err != nil || s.Name != "test" {
		t.Fatalf("initial snapshot: %v", err)
	}
	d.mu.Lock()
	d.notices = []string{"Changed"}
	d.notifyLocked()
	d.mu.Unlock()
	if err = dec.Decode(&s); err != nil || len(s.Notices) != 1 || s.Notices[0] != "Changed" {
		t.Fatalf("changed snapshot: %+v %v", s, err)
	}
}

func TestAcceptedOfferBindsManifestBeforeEngineJournal(t *testing.T) {
	c := testConfig(t, "receiver")
	c.Receive.AutoAcceptTrusted = true
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.peers["sender"] = model.Peer{ID: "sender", Name: "sender", Fingerprint: "fingerprint"}
	d.trust["sender"] = "fingerprint"
	original := transfer.Offer{ID: newID(), Name: "text", Kind: "text", Text: "approved", Total: 8}
	if err = d.decide(context.Background(), "fingerprint", original); err != nil {
		t.Fatal(err)
	}
	// Restart after durable approval but before any transfer engine journal exists.
	c.Receive.AutoAcceptTrusted = false
	again, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Text = "different"
	changed.Total = 9
	if err = again.decide(context.Background(), "fingerprint", changed); err == nil || !strings.Contains(err.Error(), "different manifest") {
		t.Fatalf("approval was reused for changed content: %v", err)
	}
	if err = again.decide(context.Background(), "fingerprint", original); err != nil {
		t.Fatalf("identical approved offer did not resume: %v", err)
	}
}

func TestQueuedClipboardCancellationClearsPersistentContent(t *testing.T) {
	c := testConfig(t, "sender")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.peers["receiver"] = model.Peer{ID: "receiver", Name: "receiver", Fingerprint: "fingerprint"}
	d.trust["receiver"] = "fingerprint"
	result, err := d.send(model.SendRequest{Peer: "receiver", Kind: "text", Text: "cancelled clipboard sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	req := (&http.Request{}).WithContext(context.Background())
	if _, err = d.action(req, model.Action{Action: "cancel", ID: result.ID}); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(c.Paths.StateDir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("cancelled clipboard sentinel")) {
		t.Fatal("cancelled clipboard content remained in history")
	}
	if d.jobs[result.ID].Request.Text != "" {
		t.Fatal("cancelled clipboard remained in memory")
	}
}

func TestIncomingPersistenceFailureCannotAutoAccept(t *testing.T) {
	c := testConfig(t, "receiver")
	c.Receive.AutoAcceptTrusted = true
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.peers["sender"] = model.Peer{ID: "sender", Name: "sender", Fingerprint: "fingerprint"}
	d.trust["sender"] = "fingerprint"
	// A directory at the journal filename reliably forces atomic publication to
	// fail, even when tests are run with elevated filesystem privileges.
	if err = os.Mkdir(filepath.Join(c.Paths.StateDir, "daemon.json"), 0700); err != nil {
		t.Fatal(err)
	}
	offer := transfer.Offer{ID: newID(), Name: "text", Kind: "text", Text: "hello", Total: 5}
	if err = d.decide(context.Background(), "fingerprint", offer); err == nil {
		t.Fatal("accepted despite persistence failure")
	}
	if d.jobs[offer.ID].Accepted {
		t.Fatal("approval recorded despite failed persistence")
	}
	if len(d.decisions) != 0 {
		t.Fatal("failed offer leaked a decision channel")
	}
}

func TestManualAcceptancePersistenceFailureDoesNotReleasePayload(t *testing.T) {
	c := testConfig(t, "receiver")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	id := newID()
	d.jobs[id] = &job{Transfer: model.Transfer{ID: id, Direction: "receive", Status: "offered"}}
	ch := make(chan bool, 1)
	d.decisions[id] = ch
	if err = os.Mkdir(filepath.Join(c.Paths.StateDir, "daemon.json"), 0700); err != nil {
		t.Fatal(err)
	}
	req := (&http.Request{}).WithContext(context.Background())
	if _, err = d.action(req, model.Action{Action: "accept", ID: id}); err == nil {
		t.Fatal("accept reported success without durable state")
	}
	select {
	case <-ch:
		t.Fatal("released incoming payload despite persistence failure")
	default:
	}
	if d.jobs[id].Accepted {
		t.Fatal("failed approval retained")
	}
}

func TestPrivateShutdownAcknowledgesThenStops(t *testing.T) {
	d, client := startDaemon(t, testConfig(t, "shutdown-test"))
	response, err := client.Post("http://relay/v1/action", "application/json", strings.NewReader(`{"action":"shutdown"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result model.Result
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal("shutdown response lost", err)
	}
	if response.StatusCode != 200 || !result.OK {
		t.Fatalf("shutdown result: %+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(d.cfg.Paths.Socket); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("shutdown did not remove the local IPC socket")
}

func TestFailedTrustCannotEnableTransfers(t *testing.T) {
	c := testConfig(t, "trust-failure")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("a", 64)
	d.peers["peer"] = model.Peer{ID: "peer", Name: "peer", Fingerprint: fp, Online: true, Relay: true}
	if err = os.Mkdir(filepath.Join(c.Paths.StateDir, "daemon.json"), 0700); err != nil {
		t.Fatal(err)
	}
	req := (&http.Request{}).WithContext(context.Background())
	if _, err = d.action(req, model.Action{Action: "trust", Peer: "peer", Fingerprint: fp}); err == nil {
		t.Fatal("trust succeeded without persistence")
	}
	if d.trusted(fp) || d.snapshot().Peers[0].Trusted {
		t.Fatal("failed trust enabled authorization")
	}
	if _, err = d.send(model.SendRequest{Peer: "peer", Kind: "text", Text: "must not send"}); err == nil {
		t.Fatal("failed trust allowed a send")
	}
}

func TestPendingTrustDoesNotAuthorizeBeforeDurableCommit(t *testing.T) {
	c := testConfig(t, "pending-trust")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("b", 64)
	d.peers["peer"] = model.Peer{ID: "peer", Name: "peer", Fingerprint: fp, Online: true, Relay: true}
	d.persistMu.Lock()
	done := make(chan error, 1)
	go func() {
		req := (&http.Request{}).WithContext(context.Background())
		_, e := d.action(req, model.Action{Action: "trust", Peer: "peer", Fingerprint: fp})
		done <- e
	}()
	deadline := time.Now().Add(2 * time.Second)
	pending := false
	for time.Now().Before(deadline) {
		d.mu.Lock()
		pending = d.trustPending["peer"]
		d.mu.Unlock()
		if pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !pending {
		d.persistMu.Unlock()
		t.Fatal("trust did not enter pending state")
	}
	if d.trusted(fp) || d.snapshot().Peers[0].Trusted {
		d.persistMu.Unlock()
		t.Fatal("pending trust authorized before persistence")
	}
	if _, err = d.send(model.SendRequest{Peer: "peer", Kind: "text", Text: "must wait"}); err == nil {
		d.persistMu.Unlock()
		t.Fatal("pending trust allowed queued send")
	}
	d.persistMu.Unlock()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if !d.trusted(fp) {
		t.Fatal("durable trust was not activated")
	}
}

func TestOrdinaryPersistenceExcludesPendingTrust(t *testing.T) {
	c := testConfig(t, "trust-snapshot")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("c", 64)
	d.trust["peer"] = fp
	d.trustPending["peer"] = true
	d.trustBefore["peer"] = ""
	if err = d.persist(); err != nil {
		t.Fatal(err)
	}
	again, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if again.trusted(fp) {
		t.Fatal("background persistence committed a pending trust addition")
	}
}

func TestFailedRevocationStaysRevokedAndWarnsUntilDurable(t *testing.T) {
	c := testConfig(t, "revoke-failure")
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("d", 64)
	d.peers["peer"] = model.Peer{ID: "peer", Name: "peer", Fingerprint: fp}
	d.trust["peer"] = fp
	if err = os.Mkdir(filepath.Join(c.Paths.StateDir, "daemon.json"), 0700); err != nil {
		t.Fatal(err)
	}
	req := (&http.Request{}).WithContext(context.Background())
	if _, err = d.action(req, model.Action{Action: "untrust", Peer: "peer"}); err == nil || !strings.Contains(err.Error(), "NOT durable") {
		t.Fatal("revocation failure was not explicit", err)
	}
	d.notices = nil // Ordinary discovery notices may change; durability must persist.
	if d.trusted(fp) || d.snapshot().Peers[0].Trusted {
		t.Fatal("failed persistence restored runtime trust")
	}
	if !strings.Contains(strings.Join(d.snapshot().Notices, " "), "Do not restart") {
		t.Fatal("missing restart warning")
	}
	if err = os.Remove(filepath.Join(c.Paths.StateDir, "daemon.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = d.action(req, model.Action{Action: "untrust", Peer: "peer"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(d.snapshot().Notices, " "), "Do not restart") {
		t.Fatal("durable revocation retained stale warning")
	}
}

func TestOversizedStateRejectedBeforeReading(t *testing.T) {
	c := testConfig(t, "oversized-state")
	if _, err := New(c); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(c.Paths.StateDir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(33 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = New(c); err == nil || !strings.Contains(err.Error(), "32 MiB") {
		t.Fatal("oversized state accepted", err)
	}
}
