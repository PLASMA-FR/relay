package relaycore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

func autoClient(t *testing.T) *Client {
	t.Helper()
	base := t.TempDir()
	c, err := newClient(filepath.Join(base, "state"), filepath.Join(base, "inbox"), "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.startAddress("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return c
}

func addedPeer(t *testing.T, c *Client, address string) model.Peer {
	t.Helper()
	out, err := c.AddDevice(address)
	if err != nil {
		t.Fatal(err)
	}
	var p model.Peer
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAutomaticTrustDefaultMigrationAndManualSeparation(t *testing.T) {
	c := autoClient(t)
	if snap(t, c).TrustMode != "tailnet" {
		t.Fatal("automatic trust is not default")
	}
	d := testDesktop(t, c)
	p := addedPeer(t, c, d.address)
	if !p.Trusted || !c.trusted(p.Fingerprint) {
		t.Fatal("live discovery did not authorize device")
	}
	c.mu.Lock()
	manual := len(c.state.Trust)
	c.mu.Unlock()
	if manual != 0 {
		t.Fatal("automatic trust became a manual grant")
	}
	if err := c.SetTrustTailnet(false); err != nil {
		t.Fatal(err)
	}
	if c.trusted(p.Fingerprint) {
		t.Fatal("manual mode retained automatic trust")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// State written by previous releases has no policy field.
	path := filepath.Join(c.stateDir, "mobile.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	delete(old, "trust_tailnet")
	data, _ = json.Marshal(old)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if snap(t, reopened).TrustMode != "tailnet" || reopened.trusted(p.Fingerprint) {
		t.Fatal("migration must enable auto policy without granting offline authorization")
	}
	if err := reopened.startAddress("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return reopened.trusted(p.Fingerprint) })
}

func TestAutomaticTrustIncomingStillRequiresReceiveApproval(t *testing.T) {
	c := autoClient(t)
	d := testDesktop(t, c)
	p := addedPeer(t, c, d.address)
	id, done := desktopSend(t, c, d, "text", "approval remains separate", nil)
	await(t, func() bool { return jobStatus(t, c, id) == "offered" })
	if snap(t, c).Clipboard != "" {
		t.Fatal("auto trust bypassed receive approval")
	}
	if _, err := c.Action("accept", id); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := sendID(t, c, p.ID, "text", "send without pairing", nil)
	await(t, func() bool { return jobStatus(t, c, out) == "completed" })
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if c.trusted(p.Fingerprint) {
		t.Fatal("VPN loss retained automatic authorization")
	}
	if err := c.authorizePeer(context.Background(), d.address, p.Fingerprint); err == nil {
		t.Fatal("stopped client authorized incoming traffic")
	}
}

func TestAutomaticBlockSurvivesDiscoveryRestartAndUnblock(t *testing.T) {
	c := autoClient(t)
	d := testDesktop(t, c)
	p := addedPeer(t, c, d.address)
	if err := c.Untrust(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	if c.trusted(p.Fingerprint) || !snap(t, c).Peers[0].Blocked {
		t.Fatal("refresh undid block")
	}
	if _, err := c.AddDevice(d.address); err == nil {
		t.Fatal("add undid block")
	}
	if err := c.authorizePeer(context.Background(), d.address, p.Fingerprint); err == nil {
		t.Fatal("blocked incoming sender authorized")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.startAddress("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Refresh(); err != nil {
		t.Fatal(err)
	}
	if reopened.trusted(p.Fingerprint) || !snap(t, reopened).Peers[0].Blocked {
		t.Fatal("restart undid block")
	}
	if err := reopened.Trust(p.ID, p.Fingerprint); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return reopened.trusted(p.Fingerprint) })
	reopened.mu.Lock()
	manual := len(reopened.state.Trust)
	reopened.mu.Unlock()
	if manual != 0 {
		t.Fatal("unblock created a manual grant")
	}
}

func TestAutomaticAuthorizationRequiresActualSourceAndExactKey(t *testing.T) {
	c := autoClient(t)
	d := testDesktop(t, c)
	p := addedPeer(t, c, d.address)
	for _, remote := range []string{"192.168.1.4:7331", "100.64.0.1:7331", "example.org:7331", "garbage"} {
		if err := c.authorizePeer(context.Background(), remote, p.Fingerprint); err == nil {
			t.Fatalf("accepted unverified source %q", remote)
		}
	}
	// A saved source port cannot authorize a different TLS client key.
	wrong := strings.Repeat("a", 64)
	if wrong == p.Fingerprint {
		wrong = strings.Repeat("b", 64)
	}
	if err := c.authorizePeer(context.Background(), d.address, wrong); err == nil {
		t.Fatal("accepted mismatched TLS key")
	}
	// Cancellation must prevent even a successful reverse probe from persisting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.authorizePeer(ctx, d.address, p.Fingerprint); err == nil {
		t.Fatal("cancelled authorization succeeded")
	}
}

func TestAutomaticDiscoveryRequiresDurablePeerBeforeTrust(t *testing.T) {
	c := autoClient(t)
	d := testDesktop(t, c)
	path := filepath.Join(c.stateDir, "mobile.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddDevice(d.address); err == nil {
		t.Fatal("auto trust succeeded without durable state")
	}
	if c.trusted(d.id.Fingerprint) || len(snap(t, c).Peers) != 0 {
		t.Fatal("failed state write enabled trust")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// The fresh phone knows no endpoint at all. The desktop's source IP listener is
// the only bootstrap, then its authenticated directory introduces a third peer.
func TestAutomaticDirectoryBootstrapsFreshPhoneAndThirdPeer(t *testing.T) {
	c := autoClient(t)
	third := testDesktop(t, c)
	base := t.TempDir()
	id, err := identity.LoadOrCreate(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := transfer.New(transfer.Options{
		Identity: id, StateDir: filepath.Join(base, "state"), ReceiveDir: filepath.Join(base, "inbox"),
		Hello:   protocol.Hello{Name: "Bootstrap desktop", OS: "linux"},
		Trusted: func(fp string) bool { return fp == c.identity.Fingerprint },
		PeerDirectory: func(context.Context, string, string, []protocol.PeerHint) ([]protocol.PeerHint, error) {
			return []protocol.PeerHint{{Name: "Third device", Address: third.address, OS: "linux"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:7331")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); _ = engine.Handle(ctx, tls.Server(conn, id.TLSConfig())) }()
		}
	}()
	defer func() { cancel(); listener.Close(); wg.Wait() }()
	wrong := strings.Repeat("a", 64)
	if wrong == id.Fingerprint {
		wrong = strings.Repeat("b", 64)
	}
	if err := c.authorizePeer(ctx, listener.Addr().String(), wrong); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("reverse probe did not reject mismatched TLS key", err)
	}
	phoneAddress := snap(t, c).Address
	hello, err := engine.Probe(ctx, phoneAddress)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap(t, c).Peers) != 0 {
		t.Fatal("Hello alone granted trust")
	}
	exchangeCtx, exchangeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer exchangeCancel()
	_, err = engine.ExchangePeers(exchangeCtx, phoneAddress, hello.Fingerprint,
		[]protocol.PeerHint{{Name: "Third device", Address: third.address, OS: "linux"}})
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return c.trusted(id.Fingerprint) && c.trusted(third.id.Fingerprint) })
	if len(snap(t, c).Peers) != 2 {
		t.Fatal("fresh phone did not discover both devices")
	}
	p := addedPeer(t, c, third.address)
	out := sendID(t, c, p.ID, "text", "directory discovered this recipient", nil)
	await(t, func() bool { return jobStatus(t, c, out) == "completed" })
}

func TestDirectoryHintsAreBoundedAndNeverTrustAssertions(t *testing.T) {
	c := autoClient(t)
	c.mu.Lock()
	c.queueHintsLocked([]protocol.PeerHint{
		{Name: "unprobed", Address: "127.0.0.2:7331"},
		{Name: "dns", Address: "attacker.example:7331"},
		{Name: "public", Address: "203.0.113.2:7331"},
	})
	if len(c.candidates) != 1 || len(c.autoPins) != 0 || len(c.state.Peers) != 0 {
		c.mu.Unlock()
		t.Fatal("directory hint granted trust or accepted nonnumeric/non-VPN target")
	}
	for i := 0; i < maxPeers+2; i++ {
		c.queueHintsLocked([]protocol.PeerHint{{Address: fmt.Sprintf("127.0.0.2:%d", 10000+i)}})
	}
	count := len(c.candidates)
	c.mu.Unlock()
	if count != maxPeers {
		t.Fatal("candidate queue was not bounded", count)
	}
}

func TestAutomaticIdentityChangeDoesNotRetargetQueuedJob(t *testing.T) {
	c := autoClient(t)
	d := testDesktop(t, c)
	p := addedPeer(t, c, d.address)
	c.mu.Lock()
	id := transfer.NewID()
	c.state.Jobs[id] = &job{Fingerprint: p.Fingerprint, Transfer: model.Transfer{
		ID: id, PeerID: p.ID, Direction: "send", Status: "paused", Updated: time.Now(),
	}}
	generation := c.policyGeneration
	hello := protocol.Hello{Name: "Replacement device", Fingerprint: strings.Repeat("b", 64)}
	if hello.Fingerprint == p.Fingerprint {
		hello.Fingerprint = strings.Repeat("a", 64)
	}
	_, err := c.rememberLocked(c.networkCtx, p.Address, hello, 0, generation)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Action("resume", id); err == nil {
		t.Fatal("identity change retargeted old transfer")
	}
	c.mu.Lock()
	j := *c.state.Jobs[id]
	c.mu.Unlock()
	if j.Fingerprint != p.Fingerprint || j.Transfer.Status != "paused" {
		t.Fatal("old job binding was rewritten")
	}
}

// A source can stall its reverse TLS handshake; losing the VPN must release
// that authorization immediately and must never install its pending key.
func TestVPNLossCancelsAutomaticReverseProbe(t *testing.T) {
	c := autoClient(t)
	listener, err := net.Listen("tcp", "127.0.0.1:7331")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	result := make(chan error, 1)
	go func() { result <- c.authorizePeer(context.Background(), "127.0.0.1:12345", strings.Repeat("a", 64)) }()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("reverse probe did not start")
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("VPN loss allowed authorization")
		}
	case <-time.After(time.Second):
		t.Fatal("VPN loss did not cancel reverse probe")
	}
	if len(snap(t, c).Peers) != 0 {
		t.Fatal("cancelled probe registered a peer")
	}
}

func TestManualTrustAfterAutomaticBlockCommitsPinAndUnblockTogether(t *testing.T) {
	c := autoClient(t)
	d := testDesktop(t, c)
	p := addedPeer(t, c, d.address)
	if err := c.Untrust(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTrustTailnet(false); err != nil {
		t.Fatal(err)
	}
	if c.trusted(p.Fingerprint) || !snap(t, c).Peers[0].Blocked {
		t.Fatal("switching policy silently unblocked the peer")
	}
	wrong := strings.Repeat("a", 64)
	if wrong == p.Fingerprint {
		wrong = strings.Repeat("b", 64)
	}
	if err := c.Trust(p.ID, wrong); err == nil {
		t.Fatal("manual unblock accepted a different fingerprint")
	}
	stateFile := filepath.Join(c.stateDir, "mobile.json")
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateFile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.Trust(p.ID, p.Fingerprint); err == nil {
		t.Fatal("manual unblock succeeded without durable commit")
	}
	c.mu.Lock()
	pin := c.state.Trust[p.ID]
	blocksIntact := c.state.Blocks["id:"+p.ID] && c.state.Blocks["address:"+p.Address] && c.state.Blocks["fingerprint:"+p.Fingerprint]
	c.mu.Unlock()
	if pin != "" || !blocksIntact || c.trusted(p.Fingerprint) || !snap(t, c).Peers[0].Blocked {
		t.Fatal("failed commit did not roll back the manual pin and all block keys")
	}
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := c.Trust(p.ID, p.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if !c.trusted(p.Fingerprint) || snap(t, c).Peers[0].Blocked {
		t.Fatal("confirmed manual trust did not unblock the peer")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClient(c.stateDir, c.inbox, "Phone", nil, loopback)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.mu.Lock()
	pin = reopened.state.Trust[p.ID]
	blockCount := len(reopened.state.Blocks)
	reopened.mu.Unlock()
	if snap(t, reopened).TrustMode != "manual" || pin != p.Fingerprint || blockCount != 0 || !reopened.trusted(p.Fingerprint) {
		t.Fatal("manual pin and unblock did not survive restart together")
	}
}
