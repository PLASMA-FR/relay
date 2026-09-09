package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/tailscale"
)

func addressHost(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return ""
	}
	return ip.Unmap().String()
}

func deviceHasAddress(device tailscale.Device, host string) bool {
	if host == "" {
		return false
	}
	for _, address := range device.Addresses {
		if addressHost(address) == host {
			return true
		}
	}
	return false
}

func (d *Daemon) blockedLocked(id string) bool {
	blocked := func(key string) bool { return d.blocked[key] || (d.blockPendingID != "" && d.blockBefore[key]) }
	if blocked(id) {
		return true
	}
	if host := addressHost(d.peers[id].Address); host != "" && blocked("ip:"+host) {
		return true
	}
	for _, address := range d.members[id].Addresses {
		if blocked("ip:" + addressHost(address)) {
			return true
		}
	}
	return false
}

// Observations never grant durable manual trust. Each automatic pin depends on
// a fresh authenticated Tailscale map, and disappears on restart or removal.
func (d *Daemon) effectivePinLocked(id string) string {
	if d.blockedLocked(id) {
		return ""
	}
	if d.cfg.Network.TrustTailnet {
		if !time.Now().Before(d.membersUntil) {
			return ""
		}
		if _, ok := d.members[id]; !ok {
			return ""
		}
		return d.autoPins[id]
	}
	if d.trustPending[id] {
		return ""
	}
	return d.trust[id]
}

func (d *Daemon) cancelPeerJobsLocked(id, reason string) {
	d.pausePeerJobsLocked(id, reason, "")
}

func (d *Daemon) pausePeerJobsLocked(id, reason, keepFingerprint string) {
	for jobID, j := range d.jobs {
		if j.Transfer.PeerID != id || j.Transfer.Terminal() || (keepFingerprint != "" && j.Fingerprint == keepFingerprint) {
			continue
		}
		j.Transfer.Status = "paused"
		j.Transfer.Error = reason
		j.Transfer.Updated = time.Now()
		if stop := d.cancels[jobID]; stop != nil {
			stop()
		}
		d.engine.Cancel(jobID)
		if ch := d.decisions[jobID]; ch != nil {
			select {
			case ch <- false:
			default:
			}
		}
	}
}

// interruptPeerJobsLocked preserves resumable work during a temporary loss of
// network authorization. A missing approval is cancellation, not user rejection.
func (d *Daemon) interruptPeerJobsLocked(id string) {
	for jobID, j := range d.jobs {
		if j.Transfer.PeerID != id || j.Transfer.Terminal() || j.Transfer.Status == "paused" {
			continue
		}
		j.Transfer.Status = "interrupted"
		j.Transfer.Error = "Tailscale authorization temporarily unavailable"
		j.Transfer.Updated = time.Now()
		if stop := d.cancels[jobID]; stop != nil {
			stop()
		}
		if stop := d.decisionStops[jobID]; stop != nil {
			stop(errors.New("Tailscale authorization temporarily unavailable"))
		}
		d.engine.Cancel(jobID)
	}
}

func (d *Daemon) clearMembersLocked() {
	if d.cfg.Network.TrustTailnet {
		for id := range d.peers {
			d.interruptPeerJobsLocked(id)
		}
	}
	d.members = map[string]tailscale.Device{}
	d.autoPins = map[string]string{}
	d.membersUntil = time.Time{}
}

func (d *Daemon) updateMembersLocked(status tailscale.Status) {
	if status.State != "Running" {
		d.clearMembersLocked()
		return
	}
	members := make(map[string]tailscale.Device, len(status.Peers))
	for _, device := range status.Peers {
		if device.ID != "" && len(device.Addresses) > 0 {
			members[device.ID] = device
		}
	}
	for id, peer := range d.peers {
		current, exists := members[id]
		if !exists || !deviceHasAddress(current, addressHost(peer.Address)) {
			delete(d.autoPins, id)
			if d.cfg.Network.TrustTailnet {
				d.cancelPeerJobsLocked(id, "Device is no longer authorized by Tailscale")
			}
		}
	}
	d.members = members
	// Fail closed when discovery stalls as well as when it explicitly fails.
	interval := max(2, d.cfg.Network.DiscoverySeconds)
	d.membersUntil = time.Now().Add(time.Duration(min(300, interval*2+10)) * time.Second)
}

func (d *Daemon) authorizePeer(ctx context.Context, remote, fingerprint string) error {
	if !d.cfg.Network.TrustTailnet {
		return nil
	}
	return d.learnTailnetPeer(ctx, remote, fingerprint, nil, "")
}

func (d *Daemon) learnTailnetPeer(ctx context.Context, remote, fingerprint string, hello *protocol.Hello, expectedID string) error {
	raw, err := hex.DecodeString(fingerprint)
	if err != nil || len(raw) != 32 {
		return errors.New("invalid peer identity")
	}
	device, err := d.whoIs(ctx, remote)
	if err != nil {
		return fmt.Errorf("Tailscale peer identity: %w", err)
	}
	host := addressHost(remote)
	if device.ID == "" || (expectedID != "" && device.ID != expectedID) || !deviceHasAddress(device, host) {
		return errors.New("Tailscale identity does not match connection address")
	}
	// Serialize trust publication with local block/unblock actions. Network WhoIs
	// runs outside this lock; membership is rechecked before and after persistence.
	d.trustMu.Lock()
	defer d.trustMu.Unlock()
	d.mu.Lock()
	member, exists := d.members[device.ID]
	if !exists || !deviceHasAddress(member, host) || !time.Now().Before(d.membersUntil) || d.blockedLocked(device.ID) {
		d.mu.Unlock()
		return errors.New("device is blocked or absent from current Tailscale membership")
	}
	p := d.peers[device.ID]
	if hello == nil && p.Fingerprint == fingerprint && d.autoPins[device.ID] == fingerprint {
		d.mu.Unlock()
		return nil
	}
	if p.ID == "" {
		p = model.Peer{ID: device.ID, Name: member.Hostname, Hostname: member.Hostname, Address: member.Addresses[0], OS: member.OS}
	}
	previousFingerprint := p.Fingerprint
	if hello != nil {
		if hello.Protocol != model.ProtocolVersion {
			d.mu.Unlock()
			return errors.New("Relay protocol mismatch")
		}
		applyHello(&p, *hello)
	} else {
		p.Fingerprint = fingerprint
	}
	if previousFingerprint != "" && previousFingerprint != fingerprint {
		d.pausePeerJobsLocked(p.ID, "Device identity changed; create a new transfer", fingerprint)
	}
	p.Online, p.Relay, p.Error, p.Blocked = true, true, "", false
	p.LastSeen = time.Now()
	d.peers[p.ID] = p
	// A new or rotated key cannot authorize until its metadata is durable.
	// Re-observing the same key must not interrupt an active transfer.
	if d.autoPins[p.ID] != fingerprint {
		delete(d.autoPins, p.ID)
	}
	d.mu.Unlock()
	if err = d.persist(); err != nil {
		d.mu.Lock()
		d.durabilityNotice = "Automatic device trust could not be saved; repair storage and refresh devices."
		d.notifyLocked()
		d.mu.Unlock()
		return fmt.Errorf("persist automatic peer identity: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	member, exists = d.members[p.ID]
	if !exists || !deviceHasAddress(member, host) || !time.Now().Before(d.membersUntil) || d.blockedLocked(p.ID) {
		return errors.New("Tailscale membership changed during authorization")
	}
	d.autoPins[p.ID] = fingerprint
	d.notifyLocked()
	return nil
}

func (d *Daemon) directoryHints() []protocol.PeerHint {
	d.mu.Lock()
	defer d.mu.Unlock()
	hints := []protocol.PeerHint{}
	if !d.cfg.Network.TrustTailnet || !time.Now().Before(d.membersUntil) {
		return hints
	}
	// Include self: a fresh phone otherwise has no way to discover the desktop
	// that contacted it. Numeric addresses are hints, never authorization claims.
	if tailscale.IsAddress(addressHost(d.address)) {
		hints = append(hints, protocol.PeerHint{Name: limitText(d.cfg.Name, 128), Address: net.JoinHostPort(addressHost(d.address), fmt.Sprint(d.cfg.Network.Port))})
	}
	ids := make([]string, 0, len(d.members))
	for id := range d.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if len(hints) >= protocol.MaxPeerHints {
			break
		}
		member := d.members[id]
		if d.blockedLocked(id) || len(member.Addresses) == 0 || !tailscale.IsAddress(member.Addresses[0]) {
			continue
		}
		p := d.peers[id]
		name := p.Name
		if name == "" {
			name = member.Hostname
		}
		hints = append(hints, protocol.PeerHint{Name: limitText(name, 128), OS: limitText(member.OS, 64), Address: net.JoinHostPort(member.Addresses[0], fmt.Sprint(d.cfg.Network.Port))})
	}
	return hints
}

func (d *Daemon) peerDirectory(ctx context.Context, remote, fingerprint string, hints []protocol.PeerHint) ([]protocol.PeerHint, error) {
	if err := protocol.ValidatePeerHints(hints); err != nil {
		return nil, err
	}
	if !d.cfg.Network.TrustTailnet || !d.trusted(fingerprint) {
		return nil, errors.New("peer directory requires current Tailscale authorization")
	}
	// The desktop already has the authenticated device map. Unsolicited hints
	// cannot add members, change their addresses, or replace an application pin.
	return d.directoryHints(), nil
}

func (d *Daemon) tailnetTrustAction(r *http.Request, a model.Action) (model.Result, error) {
	result := model.Result{OK: true}
	d.mu.Lock()
	p, err := d.findPeerLocked(a.Peer)
	d.mu.Unlock()
	if err != nil {
		return result, err
	}
	if a.Action == "pair" {
		d.probe(r.Context(), p.ID)
		d.mu.Lock()
		p = d.peers[p.ID]
		p.Trusted = p.Fingerprint != "" && d.effectivePinLocked(p.ID) == p.Fingerprint
		p.Blocked = d.blockedLocked(p.ID)
		d.mu.Unlock()
		result.Peer = &p
		result.Message = "Devices on your Tailscale network connect automatically; no pairing is needed"
		return result, nil
	}
	d.trustMu.Lock()
	d.mu.Lock()
	p = d.peers[p.ID]
	previous := make(map[string]bool, len(d.blocked))
	for key, value := range d.blocked {
		previous[key] = value
	}
	if a.Action == "trust" {
		d.blockPendingID = p.ID
		d.blockBefore = previous
	}
	keys := d.blockKeysLocked(p)
	for _, key := range keys {
		if a.Action == "untrust" {
			d.blocked[key] = true
		} else {
			delete(d.blocked, key)
		}
	}
	delete(d.autoPins, p.ID)
	p.Trusted = false
	p.Blocked = a.Action == "untrust"
	d.peers[p.ID] = p
	if p.Blocked {
		d.cancelPeerJobsLocked(p.ID, "Device blocked")
	}
	d.notifyLocked()
	d.mu.Unlock()
	if a.Action == "trust" {
		err = d.persistPending(p.ID)
	} else {
		err = d.persist()
	}
	if err != nil {
		d.mu.Lock()
		if a.Action == "trust" {
			d.blockPendingID = ""
			d.blockBefore = nil
			d.blocked = previous
			p.Blocked = d.blockedLocked(p.ID)
			d.peers[p.ID] = p
			d.durabilityNotice = "Unblock was not enabled because it could not be saved. Repair storage and retry."
		} else {
			d.durabilityNotice = "Device is blocked in memory, but the block could not be saved. Repair storage and repeat relay untrust before restarting."
		}
		d.notifyLocked()
		d.mu.Unlock()
		d.trustMu.Unlock()
		return result, fmt.Errorf("save device block policy: %w", err)
	}
	d.trustMu.Unlock()
	if a.Action == "trust" {
		d.probe(r.Context(), p.ID)
		result.Message = "Device unblocked; Tailscale devices connect automatically"
	} else {
		result.Message = "Device blocked; active transfers stopped"
	}
	d.mu.Lock()
	p = d.peers[p.ID]
	p.Trusted = p.Fingerprint != "" && d.effectivePinLocked(p.ID) == p.Fingerprint
	p.Blocked = d.blockedLocked(p.ID)
	d.mu.Unlock()
	result.Peer = &p
	signal(d.wake)
	signal(d.refresh)
	return result, nil
}

// blockKeysLocked scopes a user decision to the stable node and all addresses
// currently associated with it, preserving blocks on unrelated devices.
func (d *Daemon) blockKeysLocked(p model.Peer) []string {
	keys := []string{p.ID}
	if host := addressHost(p.Address); host != "" {
		keys = append(keys, "ip:"+host)
	}
	for _, address := range d.members[p.ID].Addresses {
		keys = append(keys, "ip:"+addressHost(address))
	}
	return keys
}
