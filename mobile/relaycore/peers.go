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
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/transfer"
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
	start := time.Now()
	hello, err := c.engine.Probe(ctx, address)
	if err != nil {
		return "", err
	}
	if pin != "" && hello.Fingerprint != pin {
		return "", errors.New("invitation fingerprint does not match this device")
	}
	if !validHello(hello) {
		return "", errors.New("peer sent an invalid name")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil {
		return "", errors.New("connection stopped")
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
			return "", errors.New("saved device limit reached")
		}
		id = transfer.NewID()
	}
	p := model.Peer{ID: id, Name: hello.Name, Hostname: hello.Hostname, Address: address, OS: hello.OS, Arch: hello.Arch, Online: true, Relay: true, Version: hello.Version, Protocol: hello.Protocol, Capabilities: hello.Capabilities, Fingerprint: hello.Fingerprint, LastSeen: time.Now(), LatencyMS: time.Since(start).Milliseconds()}
	p.Trusted = c.state.Trust[id] != "" && c.state.Trust[id] == p.Fingerprint
	if c.state.Trust[id] != "" && !p.Trusted {
		p.Error = "Identity changed; verify the full fingerprint again"
	}
	c.state.Peers[id] = p
	if err = c.persistLocked(); err != nil {
		if previous.ID == "" {
			delete(c.state.Peers, id)
		} else {
			c.state.Peers[id] = previous
		}
		return "", err
	}
	c.changedLocked()
	b, _ := json.Marshal(p)
	return string(b), nil
}

// ImportInvite checks the invitation pin against a live TLS probe. Trust remains explicit.
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

// Trust persists a full observed fingerprint before enabling transfers.
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
	c.state.Trust[peerID] = fingerprint
	// The lock prevents engine authorization until durable commit succeeds.
	if err = c.persistLocked(); err != nil {
		if old == "" {
			delete(c.state.Trust, peerID)
		} else {
			c.state.Trust[peerID] = old
		}
		return err
	}
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
	pin := c.state.Trust[peerID]
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

// Refresh probes only saved numeric Tailscale endpoints, with four bounded workers.
func (c *Client) Refresh() error {
	ctx, finish, err := c.beginNetwork()
	if err != nil {
		return err
	}
	defer finish()
	c.mu.Lock()
	peers := make([]model.Peer, 0, len(c.state.Peers))
	for _, p := range c.state.Peers {
		peers = append(peers, p)
	}
	c.mu.Unlock()
	jobs := make(chan model.Peer, len(peers))
	for _, p := range peers {
		jobs <- p
	}
	close(jobs)
	var wg sync.WaitGroup
	for n := 0; n < min(4, len(peers)); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if ctx.Err() != nil {
					return
				}
				address, e := c.peerAddress(p.Address)
				started := time.Now()
				if e == nil {
					h, probeErr := c.engine.Probe(ctx, address)
					e = probeErr
					if e == nil {
						if !validHello(h) {
							e = errors.New("peer sent an invalid name")
						} else {
							p.Name = h.Name
							p.Hostname = h.Hostname
							p.Fingerprint = h.Fingerprint
							p.OS = h.OS
							p.Arch = h.Arch
							p.Protocol = h.Protocol
							p.Version = h.Version
							p.Capabilities = h.Capabilities
							p.LastSeen = time.Now()
							p.LatencyMS = time.Since(started).Milliseconds()
						}
					}
				}
				c.mu.Lock()
				if ctx.Err() == nil && !c.closed {
					current, ok := c.state.Peers[p.ID]
					if ok && current.Address == p.Address {
						p.Online = e == nil
						p.Relay = e == nil
						p.Error = ""
						if e != nil {
							p.Error = e.Error()
						} else if pin := c.state.Trust[p.ID]; pin != "" && pin != p.Fingerprint {
							p.Error = "Identity changed; verify the full fingerprint again"
						}
						c.state.Peers[p.ID] = p
						c.changedLocked()
					}
				}
				c.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return c.persistLocked()
}
