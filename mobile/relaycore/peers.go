package relaycore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PLASMA-FR/relay/internal/invite"
	"github.com/PLASMA-FR/relay/internal/protocol"
)

func validHello(h protocol.Hello) bool {
	if !validName(h.Name) || len(h.Hostname) > 128 || len(h.OS) > 64 || len(h.Arch) > 64 || len(h.Version) > 64 || len(h.Capabilities) > 16 {
		return false
	}
	for _, s := range h.Capabilities {
		if len(s) > 64 {
			return false
		}
	}
	return true
}

func (c *Client) peerAddress(address string) (string, error) {
	if ip, e := netip.ParseAddr(address); e == nil {
		address = net.JoinHostPort(ip.String(), "7331")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("enter a literal Tailscale IP, optionally with port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !c.allowAddress(ip) {
		return "", errors.New("peer address must be a literal Tailscale IP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", errors.New("invalid Relay port")
	}
	return net.JoinHostPort(ip.String(), port), nil
}
func (c *Client) beginNetwork() (context.Context, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, errors.New("client is closed")
	}
	if !c.running {
		return nil, nil, errors.New("make Relay available on Tailscale first")
	}
	ctx, cancel := context.WithTimeout(c.networkCtx, 6*time.Second)
	c.netWG.Add(1)
	return ctx, func() { cancel(); c.netWG.Done() }, nil
}

// AddDevice probes a numeric Tailscale endpoint and remembers its observed identity.
func (c *Client) AddDevice(address string) (string, error) { return c.addDevice(address, "") }
func (c *Client) addDevice(address, pin string) (string, error) {
	address, err := c.peerAddress(strings.TrimSpace(address))
	if err != nil {
		return "", err
	}
	ctx, finish, err := c.beginNetwork()
	if err != nil {
		return "", err
	}
	defer finish()
	c.mu.Lock()
	generation := c.policyGeneration
	c.mu.Unlock()
	p, err := c.probePeer(ctx, address, pin, generation)
	if err != nil {
		return "", err
	}
	signal(c.discoveryWake)
	b, _ := json.Marshal(p)
	return string(b), nil
}

// ImportInvite checks the invitation pin against a live TLS probe and applies the trust policy.
func (c *Client) ImportInvite(uri string) (string, error) {
	i, err := invite.Parse(uri)
	if err != nil {
		return "", err
	}
	return c.addDevice(i.Address, i.Fingerprint)
}

// Invite returns only this device's public name, endpoint and identity fingerprint.
func (c *Client) Invite() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.running {
		return "", errors.New("make Relay available on Tailscale first")
	}
	i, err := invite.New(c.name, c.address, c.identity.Fingerprint)
	if err != nil {
		return "", err
	}
	return i.URL(), nil
}

// Trust clears a block in automatic mode, or durably pins an observed key in manual mode.
func (c *Client) Trust(peerID, fingerprint string) error {
	b, err := hex.DecodeString(fingerprint)
	if err != nil || len(b) != 32 || strings.ToLower(fingerprint) != fingerprint {
		return errors.New("provide the full lowercase SHA-256 fingerprint")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("client is closed")
	}
	p, ok := c.state.Peers[peerID]
	if !ok || p.Fingerprint != fingerprint {
		return errors.New("fingerprint does not match the observed device")
	}
	old := c.state.Trust[peerID]
	oldBlocks := c.state.Blocks
	c.state.Blocks = make(map[string]bool, len(oldBlocks))
	for key, blocked := range oldBlocks {
		c.state.Blocks[key] = blocked
	}
	delete(c.state.Blocks, "id:"+peerID)
	delete(c.state.Blocks, "address:"+p.Address)
	delete(c.state.Blocks, "fingerprint:"+fingerprint)
	if !c.state.TrustTailnet {
		c.state.Trust[peerID] = fingerprint
	}
	// The lock prevents engine authorization until durable commit succeeds.
	if err = c.persistLocked(); err != nil {
		if old == "" {
			delete(c.state.Trust, peerID)
		} else {
			c.state.Trust[peerID] = old
		}
		c.state.Blocks = oldBlocks
		return err
	}
	c.policyGeneration++
	signal(c.discoveryWake)
	c.changedLocked()
	return nil
}

// Untrust revokes authorization immediately and aborts that peer's active transfers.
func (c *Client) Untrust(peerID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("client is closed")
	}
	if _, ok := c.state.Peers[peerID]; !ok {
		return errors.New("device not found")
	}
	pin := c.state.Peers[peerID].Fingerprint
	if c.state.TrustTailnet {
		p := c.state.Peers[peerID]
		c.state.Blocks["id:"+peerID] = true
		c.state.Blocks["address:"+p.Address] = true
		c.state.Blocks["fingerprint:"+pin] = true
	}
	c.policyGeneration++
	delete(c.autoPins, peerID)
	delete(c.state.Trust, peerID)
	if pin != "" {
		for id, other := range c.state.Trust {
			if other == pin {
				delete(c.state.Trust, id)
			}
		}
	}
	for id, j := range c.state.Jobs {
		if j.Transfer.PeerID != peerID && j.Fingerprint != pin {
			continue
		}
		if !j.Transfer.Terminal() {
			j.Transfer.Status = "paused"
			j.Transfer.Error = "Trust revoked"
			j.Transfer.Updated = time.Now()
		}
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
	err := c.persistLocked()
	if err != nil {
		err = fmt.Errorf("trust revoked for this session only; repair storage and retry Untrust before restarting: %w", err)
		c.trustPersistenceError = err.Error()
	}
	c.changedLocked()
	return err
}

// Refresh probes saved endpoints and bounded directory hints with four workers.
// Hints are discovery candidates only; every automatic key comes from live TLS.
func (c *Client) Refresh() error {
	ctx, finish, err := c.beginNetwork()
	if err != nil {
		return err
	}
	defer finish()
	return c.refresh(ctx)
}

func (c *Client) refresh(ctx context.Context) error {
	// Coalesce public refresh with the periodic worker, retaining context cancellation.
	if !c.refreshMu.TryLock() {
		return nil
	}
	defer c.refreshMu.Unlock()
	c.mu.Lock()
	generation := c.policyGeneration
	addresses := map[string]bool{}
	for _, p := range c.state.Peers {
		if !c.blockedLocked(p) {
			addresses[p.Address] = true
		}
	}
	if c.state.TrustTailnet {
		for address := range c.candidates {
			addresses[address] = true
		}
	}
	c.candidates = map[string]bool{}
	c.mu.Unlock()
	jobs := make(chan string, len(addresses))
	for address := range addresses {
		jobs <- address
	}
	close(jobs)
	var wg sync.WaitGroup
	for n := 0; n < min(4, len(addresses)); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for address := range jobs {
				if ctx.Err() != nil {
					return
				}
				probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
				p, err := c.probePeer(probeCtx, address, "", generation)
				if err == nil {
					c.exchangeDirectory(probeCtx, p, generation)
				}
				cancel()
				if err != nil {
					c.mu.Lock()
					if ctx.Err() == nil && generation == c.policyGeneration {
						for id, current := range c.state.Peers {
							if current.Address == address {
								current.Online = false
								current.Error = err.Error()
								c.state.Peers[id] = current
								delete(c.autoPins, id)
							}
						}
						c.changedLocked()
					}
					c.mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}
