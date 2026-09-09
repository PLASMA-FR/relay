package relaycore

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"sort"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

// Android supplies this app's active VPN address to Start. Public Android APIs
// cannot attest the VPN provider: automatic trust assumes that VPN is Tailscale.
// A numeric Tailnet address alone never enables authorization while stopped.
func (c *Client) effectivePinLocked(id string) string {
	p, ok := c.state.Peers[id]
	if !ok || c.blockedLocked(p) {
		return ""
	}
	if c.state.TrustTailnet {
		if !c.running || c.networkCtx == nil || c.networkCtx.Err() != nil {
			return ""
		}
		return c.autoPins[id]
	}
	return c.state.Trust[id]
}

func (c *Client) blockedLocked(p model.Peer) bool {
	return c.state.Blocks["id:"+p.ID] || c.state.Blocks["address:"+p.Address] || c.state.Blocks["fingerprint:"+p.Fingerprint]
}

// SetTrustTailnet selects automatic VPN trust or explicit manual fingerprint pins.
// Automatic observations never become durable manual trust grants.
func (c *Client) SetTrustTailnet(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("client is closed")
	}
	old := c.state.TrustTailnet
	if old == enabled {
		return nil
	}
	c.state.TrustTailnet = enabled
	if err := c.persistLocked(); err != nil {
		c.state.TrustTailnet = old
		return err
	}
	c.policyGeneration++
	c.autoPins = map[string]string{}
	for id, j := range c.state.Jobs {
		if c.effectivePinLocked(j.Transfer.PeerID) != j.Fingerprint && !j.Transfer.Terminal() {
			if stop := c.cancels[id]; stop != nil {
				stop()
			}
			c.engine.Cancel(id)
			if ch := c.decisions[id]; ch != nil {
				select {
				case ch <- false:
				default:
				}
			}
		}
	}
	c.changedLocked()
	signal(c.discoveryWake)
	return nil
}

func (c *Client) probePeer(ctx context.Context, address, expected string, generation uint64) (model.Peer, error) {
	address, err := c.peerAddress(address)
	if err != nil {
		return model.Peer{}, err
	}
	start := time.Now()
	hello, err := c.engine.Probe(ctx, address)
	if err != nil {
		return model.Peer{}, err
	}
	if expected != "" && hello.Fingerprint != expected {
		return model.Peer{}, errors.New("invitation fingerprint does not match this device")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rememberLocked(ctx, address, hello, time.Since(start).Milliseconds(), generation)
}

func (c *Client) rememberLocked(ctx context.Context, address string, hello protocol.Hello, latency int64, generation uint64) (model.Peer, error) {
	if c.closed || !c.running || ctx.Err() != nil || c.networkCtx.Err() != nil || generation != c.policyGeneration {
		return model.Peer{}, errors.New("connection or trust policy changed")
	}
	fp, err := hex.DecodeString(hello.Fingerprint)
	if !validHello(hello) || err != nil || len(fp) != 32 || hello.Fingerprint == c.identity.Fingerprint {
		return model.Peer{}, errors.New("peer sent an invalid identity")
	}
	var previous model.Peer
	for _, p := range c.state.Peers {
		if p.Address == address {
			previous = p
			break
		}
	}
	id := previous.ID
	if id == "" {
		if len(c.state.Peers) >= maxPeers {
			return model.Peer{}, errors.New("saved device limit reached")
		}
		id = transfer.NewID()
	}
	p := model.Peer{ID: id, Name: hello.Name, Hostname: hello.Hostname, Address: address, OS: hello.OS, Arch: hello.Arch, Online: true, Relay: true, Version: hello.Version, Protocol: hello.Protocol, Capabilities: hello.Capabilities, Fingerprint: hello.Fingerprint, LastSeen: time.Now(), LatencyMS: latency}
	p.Blocked = c.blockedLocked(p) || c.blockedLocked(previous)
	if p.Blocked {
		return model.Peer{}, errors.New("device is blocked")
	}
	if !c.state.TrustTailnet && c.state.Trust[id] != "" && c.state.Trust[id] != p.Fingerprint {
		p.Error = "Identity changed; verify the full fingerprint again"
	}
	c.state.Peers[id] = p
	if err := c.persistLocked(); err != nil {
		if previous.ID == "" {
			delete(c.state.Peers, id)
		} else {
			c.state.Peers[id] = previous
		}
		return model.Peer{}, err
	}
	if c.state.TrustTailnet {
		c.autoPins[id] = p.Fingerprint
	}
	if previous.Fingerprint != "" && previous.Fingerprint != p.Fingerprint {
		// Keep queued manifests bound to their original TLS key. A new network
		// observation must never turn an old queued send into a send to a new key.
		for jobID, j := range c.state.Jobs {
			if j.Transfer.PeerID == id && j.Fingerprint != p.Fingerprint && !j.Transfer.Terminal() {
				j.Transfer.Status = "paused"
				j.Transfer.Error = "Device identity changed; create a new transfer"
				j.Transfer.Updated = time.Now()
				if cancel := c.cancels[jobID]; cancel != nil {
					cancel()
				}
				c.engine.Cancel(jobID)
				if ch := c.decisions[jobID]; ch != nil {
					select {
					case ch <- false:
					default:
					}
				}
			}
		}
		c.noticeLocked(c.persistLocked())
	}
	p.Trusted = c.effectivePinLocked(id) == p.Fingerprint
	c.changedLocked()
	return p, nil
}

// authorizePeer proves that the actual source IP also serves Relay using the
// exact TLS client key. Reverse Hello probes never authorize or recurse.
func (c *Client) authorizePeer(ctx context.Context, remote, fingerprint string) error {
	c.mu.Lock()
	if !c.state.TrustTailnet {
		c.mu.Unlock()
		return nil
	}
	if c.closed || !c.running || c.networkCtx == nil || c.networkCtx.Err() != nil {
		c.mu.Unlock()
		return errors.New("Tailscale VPN is unavailable")
	}
	host, _, err := net.SplitHostPort(remote)
	ip, ipErr := netip.ParseAddr(host)
	if err != nil || ipErr != nil || !c.allowAddress(ip.Unmap()) {
		c.mu.Unlock()
		return errors.New("sender is outside Tailscale")
	}
	host = ip.Unmap().String()
	address := net.JoinHostPort(host, "7331")
	// Saved custom ports are usable only for the same source IP. Prefer the
	// known key when fixture/dev peers share a host; never use a hinted port.
	for _, p := range c.state.Peers {
		peerHost, _, _ := net.SplitHostPort(p.Address)
		if peerHost == host && p.Fingerprint == fingerprint {
			address = p.Address
			break
		}
	}
	if c.state.Blocks["address:"+address] || c.state.Blocks["fingerprint:"+fingerprint] {
		c.mu.Unlock()
		return errors.New("device is blocked")
	}
	networkCtx, generation := c.networkCtx, c.policyGeneration
	c.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(networkCtx, 5*time.Second)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	hello, err := c.engine.Probe(probeCtx, address)
	if err != nil {
		return err
	}
	if hello.Fingerprint != fingerprint {
		return errors.New("sender TLS identity does not match its Relay listener")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil || networkCtx != c.networkCtx || !c.state.TrustTailnet {
		return errors.New("connection or trust policy changed")
	}
	known := false
	for id, p := range c.state.Peers {
		if p.Address == address && c.effectivePinLocked(id) == fingerprint {
			known = true
			break
		}
	}
	_, err = c.rememberLocked(probeCtx, address, hello, 0, generation)
	if err == nil && !known {
		signal(c.discoveryWake)
	}
	return err
}

func (c *Client) peerHintsLocked() []protocol.PeerHint {
	if !c.running || !c.state.TrustTailnet {
		return nil
	}
	hints := []protocol.PeerHint{{Name: c.name, Address: c.address, OS: "android"}}
	for id, p := range c.state.Peers {
		if p.Online && !c.blockedLocked(p) && c.effectivePinLocked(id) == p.Fingerprint {
			if _, err := c.peerAddress(p.Address); err == nil {
				hints = append(hints, protocol.PeerHint{Name: p.Name, Address: p.Address, OS: p.OS})
			}
		}
	}
	sort.Slice(hints, func(i, j int) bool { return hints[i].Address < hints[j].Address })
	return hints
}

func (c *Client) queueHintsLocked(hints []protocol.PeerHint) {
	for _, h := range hints {
		address, err := c.peerAddress(h.Address)
		if err != nil || address == c.address || c.state.Blocks["address:"+address] {
			continue
		}
		known := false
		for _, p := range c.state.Peers {
			if p.Address == address {
				known = true
				break
			}
		}
		if known || len(c.candidates) >= maxPeers {
			continue
		}
		c.candidates[address] = true
	}
	if len(c.candidates) > 0 {
		signal(c.discoveryWake)
	}
}

func (c *Client) peerDirectory(ctx context.Context, remote, fp string, hints []protocol.PeerHint) ([]protocol.PeerHint, error) {
	if err := protocol.ValidatePeerHints(hints); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running || c.closed || ctx.Err() != nil {
		return nil, errors.New("connection stopped")
	}
	if !c.state.TrustTailnet {
		return nil, nil
	}
	authorized := false
	for id := range c.state.Peers {
		if fp != "" && c.effectivePinLocked(id) == fp {
			authorized = true
			break
		}
	}
	if !authorized {
		return nil, errors.New("sender is not trusted")
	}
	c.queueHintsLocked(hints)
	return c.peerHintsLocked(), nil
}

func (c *Client) exchangeDirectory(ctx context.Context, p model.Peer, generation uint64) {
	capable := false
	for _, capability := range p.Capabilities {
		if capability == "peer-directory-v1" {
			capable = true
		}
	}
	if !capable {
		return
	}
	c.mu.Lock()
	if !c.state.TrustTailnet || generation != c.policyGeneration || c.effectivePinLocked(p.ID) != p.Fingerprint {
		c.mu.Unlock()
		return
	}
	hints := c.peerHintsLocked()
	c.mu.Unlock()
	peers, err := c.engine.ExchangePeers(ctx, p.Address, p.Fingerprint, hints)
	if err != nil || protocol.ValidatePeerHints(peers) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() == nil && c.running && c.state.TrustTailnet && generation == c.policyGeneration && c.effectivePinLocked(p.ID) == p.Fingerprint {
		c.queueHintsLocked(peers)
	}
}

func (c *Client) discover(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		enabled := c.state.TrustTailnet
		c.mu.Unlock()
		if enabled && ctx.Err() == nil {
			_ = c.refresh(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.discoveryWake:
			// Coalesce reciprocal directory notifications and cap probe frequency.
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
