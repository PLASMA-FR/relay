package daemon

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/tailscale"
)

func autoDaemon(t *testing.T) (*Daemon, tailscale.Device) {
	t.Helper()
	cfg := testConfig(t, "automatic")
	cfg.Network.TrustTailnet = true
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	member := tailscale.Device{ID: "node", Hostname: "phone", Addresses: []string{"100.64.0.2"}, Online: true}
	d.whoIs = func(context.Context, string) (tailscale.Device, error) { return member, nil }
	d.mu.Lock()
	d.updateMembersLocked(tailscale.Status{State: "Running", Peers: []tailscale.Device{member}})
	d.mu.Unlock()
	return d, member
}

func TestTailnetFirstTransferAndDirectoryBootstrapWithoutPins(t *testing.T) {
	ca, cb := testConfig(t, "desktop"), testConfig(t, "phone")
	ca.Network.TrustTailnet, cb.Network.TrustTailnet = true, true
	cb.Receive.AutoAcceptTrusted = true
	a, _ := startDaemon(t, ca)
	b, _ := startDaemon(t, cb)
	sa, sb := a.snapshot(), b.snapshot()
	// Only these test-local verifier closures authorize fixture loopback traffic.
	// Production WhoIs rejects loopback even when development listening is enabled.
	ma := tailscale.Device{ID: "desktop-node", Hostname: sa.Name, Addresses: []string{sa.Address}, Online: true}
	mb := tailscale.Device{ID: "phone-node", Hostname: sb.Name, Addresses: []string{sb.Address}, Online: true}
	a.whoIs = func(context.Context, string) (tailscale.Device, error) { return mb, nil }
	b.whoIs = func(context.Context, string) (tailscale.Device, error) { return ma, nil }
	a.mu.Lock()
	a.updateMembersLocked(tailscale.Status{State: "Running", Peers: []tailscale.Device{mb}})
	a.peers[mb.ID] = model.Peer{ID: mb.ID, Name: mb.Hostname, Address: sb.Address, Online: true}
	a.mu.Unlock()
	b.mu.Lock()
	b.updateMembersLocked(tailscale.Status{State: "Running", Peers: []tailscale.Device{ma}})
	b.mu.Unlock()
	a.probe(context.Background(), mb.ID)
	// The authenticated directory callback introduces a desktop to an empty peer
	// registry before any file or offer is sent.
	bs := b.snapshot()
	if len(bs.Peers) != 1 || bs.Peers[0].ID != ma.ID || !bs.Peers[0].Trusted {
		t.Fatalf("directory did not bootstrap desktop: %+v", bs.Peers)
	}
	a.mu.Lock()
	manualA := len(a.trust)
	a.mu.Unlock()
	b.mu.Lock()
	manualB := len(b.trust)
	b.mu.Unlock()
	if manualA+manualB != 0 {
		t.Fatal("auto observation became manual trust")
	}
	result, err := a.send(model.SendRequest{Peer: mb.ID, Kind: "text", Text: "No pairing required"})
	if err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, a, result.ID, "completed")
	waitTransfer(t, b, result.ID, "completed")
	text, err := b.clipboard.ReadInternal()
	if err != nil || text != "No pairing required" {
		t.Fatalf("received %q: %v", text, err)
	}
}

func TestTailnetAuthorizationRequiresExactCurrentIdentity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Daemon, tailscale.Device)
		remote string
	}{
		{"wrong address", func(d *Daemon, m tailscale.Device) {}, "100.64.0.3:32000"},
		{"unverified", func(d *Daemon, m tailscale.Device) {
			d.whoIs = func(context.Context, string) (tailscale.Device, error) {
				return tailscale.Device{}, errors.New("expired or unauthorized node")
			}
		}, "100.64.0.2:32000"},
		{"wrong node", func(d *Daemon, m tailscale.Device) {
			m.ID = "other-node"
			d.whoIs = func(context.Context, string) (tailscale.Device, error) { return m, nil }
		}, "100.64.0.2:32000"},
		{"stale discovery", func(d *Daemon, m tailscale.Device) { d.membersUntil = time.Now().Add(-time.Second) }, "100.64.0.2:32000"},
		{"removed node", func(d *Daemon, m tailscale.Device) { d.updateMembersLocked(tailscale.Status{State: "Running"}) }, "100.64.0.2:32000"},
		{"unavailable", func(d *Daemon, m tailscale.Device) { d.clearMembersLocked() }, "100.64.0.2:32000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, m := autoDaemon(t)
			tc.change(d, m)
			fp := strings.Repeat("a", 64)
			if err := d.authorizePeer(context.Background(), tc.remote, fp); err == nil {
				t.Fatal("unauthorized endpoint accepted")
			}
			if d.trusted(fp) {
				t.Fatal("rejected peer received effective trust")
			}
		})
	}
}

func TestTailnetMetadataPersistenceFailureDoesNotAuthorize(t *testing.T) {
	d, _ := autoDaemon(t)
	if err := os.Mkdir(filepath.Join(d.cfg.Paths.StateDir, "daemon.json"), 0700); err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("a", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err == nil {
		t.Fatal("accepted without durable metadata")
	}
	if d.trusted(fp) || len(d.trust) != 0 {
		t.Fatal("failed persistence granted trust")
	}
}

func TestTailnetBlockPersistsAcrossRefreshRestartAndUnblocksWithoutFingerprint(t *testing.T) {
	d, m := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://relay/v1/action", nil)
	if _, err := d.action(req, model.Action{Action: "untrust", Peer: m.ID}); err != nil {
		t.Fatal(err)
	}
	d.updateMembersLocked(tailscale.Status{State: "Running", Peers: []tailscale.Device{m}})
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err == nil {
		t.Fatal("refresh unblocked device")
	}
	reloaded, err := New(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.whoIs = d.whoIs
	reloaded.updateMembersLocked(tailscale.Status{State: "Running", Peers: []tailscale.Device{m}})
	if err := reloaded.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err == nil {
		t.Fatal("restart unblocked device")
	}
	if !reloaded.snapshot().Peers[0].Blocked {
		t.Fatal("blocked state missing")
	}
	// Unblock's best-effort probe may fail because this is a synthetic endpoint;
	// the local action still requires no fingerprint or pairing ritual.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reloaded.action(req.WithContext(ctx), model.Action{Action: "trust", Peer: m.ID}); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.trust) != 0 {
		t.Fatal("unblock created manual grant")
	}
}

func TestTailnetKeyRotationNeverRetargetsQueuedJobs(t *testing.T) {
	d, m := autoDaemon(t)
	old, newPin := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", old); err != nil {
		t.Fatal(err)
	}
	first, err := d.send(model.SendRequest{Peer: m.ID, Kind: "text", Text: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", newPin); err != nil {
		t.Fatal(err)
	}
	second, err := d.send(model.SendRequest{Peer: m.ID, Kind: "text", Text: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if d.jobs[first.ID].Fingerprint != old || d.jobs[second.ID].Fingerprint != newPin {
		t.Fatal("rotation retargeted existing job")
	}
	if d.trusted(old) || !d.trusted(newPin) {
		t.Fatal("rotation left wrong effective pin")
	}
	reloaded, err := New(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.trusted(newPin) || len(reloaded.trust) != 0 {
		t.Fatal("restart restored automatic grant without membership")
	}
}

func TestTailnetDirectoryDoesNotAcceptHintMembership(t *testing.T) {
	d, _ := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
		t.Fatal(err)
	}
	hints, err := d.peerDirectory(context.Background(), "100.64.0.2:31000", fp, []protocol.PeerHint{{Name: "imposter", Address: "100.64.0.99:7331"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 || hints[0].Address != "100.64.0.2:7331" {
		t.Fatalf("directory escaped membership: %+v", hints)
	}
	if len(d.members) != 1 || len(d.peers) != 1 {
		t.Fatal("unverified hint registered a device")
	}
}

func TestDevelopmentLoopbackDoesNotImplyAutomaticTrust(t *testing.T) {
	d, _ := autoDaemon(t)
	d.whoIs = tailscale.WhoIs
	if err := d.authorizePeer(context.Background(), "127.0.0.1:32000", strings.Repeat("a", 64)); err == nil {
		t.Fatal("loopback bypassed Tailscale identity")
	}
}

func TestTailnetBlockWinsAgainstInFlightWhoIs(t *testing.T) {
	d, m := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	d.whoIs = func(context.Context, string) (tailscale.Device, error) { close(started); <-release; return m, nil }
	done := make(chan error, 1)
	go func() { done <- d.authorizePeer(context.Background(), "100.64.0.2:31000", strings.Repeat("b", 64)) }()
	<-started
	if _, err := d.action(httptest.NewRequest("POST", "http://relay/v1/action", nil), model.Action{Action: "untrust", Peer: m.ID}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("in-flight identity lookup undid a block")
	}
	if d.trusted(fp) || !d.snapshot().Peers[0].Blocked {
		t.Fatal("block did not win")
	}
}

func TestTailnetRemovalDuringPersistenceFailsClosed(t *testing.T) {
	d, _ := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	d.persistMu.Lock()
	done := make(chan error, 1)
	go func() { done <- d.authorizePeer(context.Background(), "100.64.0.2:31000", fp) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d.mu.Lock()
		_, observed := d.peers["node"]
		d.mu.Unlock()
		if observed {
			break
		}
		if time.Now().After(deadline) {
			d.persistMu.Unlock()
			t.Fatal("authorization did not reach persistence")
		}
		time.Sleep(time.Millisecond)
	}
	d.mu.Lock()
	d.clearMembersLocked()
	d.mu.Unlock()
	d.persistMu.Unlock()
	if err := <-done; err == nil {
		t.Fatal("authorization ignored membership removal during save")
	}
	if d.trusted(fp) {
		t.Fatal("removed member became authorized")
	}
}

func TestTailnetBlockFollowsAddressAcrossNodeReplacement(t *testing.T) {
	d, m := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
		t.Fatal(err)
	}
	if _, err := d.action(httptest.NewRequest("POST", "http://relay/v1/action", nil), model.Action{Action: "untrust", Peer: m.ID}); err != nil {
		t.Fatal(err)
	}
	m.ID = "replacement-node"
	d.whoIs = func(context.Context, string) (tailscale.Device, error) { return m, nil }
	d.updateMembersLocked(tailscale.Status{State: "Running", Peers: []tailscale.Device{m}})
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", strings.Repeat("b", 64)); err == nil {
		t.Fatal("replacement identity evaded address block")
	}
}

func TestTailnetUnrelatedSaveCannotPublishPendingUnblock(t *testing.T) {
	d, m := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
		t.Fatal(err)
	}
	// Model an unblock that has staged its proposed state while waiting for the
	// serialized durable write. An unrelated job save must retain the old block.
	d.mu.Lock()
	d.blockBefore = map[string]bool{m.ID: true, "ip:100.64.0.2": true}
	d.blockPendingID = m.ID
	d.blocked = map[string]bool{}
	d.mu.Unlock()
	if d.trusted(fp) {
		t.Fatal("pending unblock authorized existing key")
	}
	if err := d.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.snapshot().Peers[0].Blocked {
		t.Fatal("unrelated save published uncommitted unblock")
	}
}

func TestTailnetOutgoingProbeCannotReassignDiscoveredNode(t *testing.T) {
	d, m := autoDaemon(t)
	fp := strings.Repeat("a", 64)
	hello := protocol.Hello{Fingerprint: fp, Protocol: model.ProtocolVersion}
	if err := d.learnTailnetPeer(context.Background(), "100.64.0.2:7331", fp, &hello, "different-discovered-node"); err == nil {
		t.Fatal("probe authorized a node other than the discovered target")
	}
	if d.trusted(fp) || d.autoPins[m.ID] != "" {
		t.Fatal("mismatched discovery node obtained a pin")
	}
}

func TestTailnetBlockSurvivesManualModeAndVerifiedTrustClearsIt(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		name := "durable trust"
		if failSave {
			name = "failed trust"
		}
		t.Run(name, func(t *testing.T) {
			d, m := autoDaemon(t)
			fp := strings.Repeat("a", 64)
			if err := d.authorizePeer(context.Background(), "100.64.0.2:31000", fp); err != nil {
				t.Fatal(err)
			}
			d.trust[m.ID] = fp // A manual grant predating the user's automatic-mode block.
			req := httptest.NewRequest("POST", "http://relay/v1/action", nil)
			if _, err := d.action(req, model.Action{Action: "untrust", Peer: m.ID}); err != nil {
				t.Fatal(err)
			}
			cfg := d.cfg
			cfg.Network.TrustTailnet = false
			manual, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if manual.trusted(fp) || !manual.snapshot().Peers[0].Blocked || manual.snapshot().Peers[0].Trusted {
				t.Fatal("mode change bypassed a device block")
			}
			if _, err := manual.action(req, model.Action{Action: "trust", Peer: m.ID}); err == nil {
				t.Fatal("manual unblock did not require fingerprint verification")
			}
			path := filepath.Join(cfg.Paths.StateDir, "daemon.json")
			if failSave {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err = manual.action(req, model.Action{Action: "trust", Peer: m.ID, Fingerprint: fp})
			if failSave {
				if err == nil || manual.trusted(fp) || !manual.snapshot().Peers[0].Blocked {
					t.Fatal("failed manual trust unblocked the device")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := manual.persist(); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			reloaded, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.trusted(fp) == failSave || reloaded.snapshot().Peers[0].Blocked != failSave {
				t.Fatal("manual trust/block state was not durably committed together")
			}
		})
	}
}
