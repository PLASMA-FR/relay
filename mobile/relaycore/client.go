// Package relaycore is the in-process mobile bridge to Relay's v1 transfer engine.
// The native app supplies its private directories and a literal Tailscale address.
package relaycore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

const maxPeers = 128
const maxPending = 128
const maxHistory = 200
const maxStateBytes = 32 << 20

// Observer receives coalesced snapshots on a background goroutine. Implementations
// should post to their UI dispatcher and return promptly; callbacks may call Snapshot.
type Observer interface{ OnChange(snapshotJSON string) }

type job struct {
	Transfer    model.Transfer     `json:"transfer"`
	Fingerprint string             `json:"fingerprint"`
	Prepared    *transfer.Prepared `json:"prepared,omitempty"`
	Accepted    bool               `json:"accepted,omitempty"`
	Digest      string             `json:"digest,omitempty"`
}
type diskState struct {
	Version     int                   `json:"version"`
	Peers       map[string]model.Peer `json:"peers"`
	Trust       map[string]string     `json:"trust"`
	Jobs        map[string]*job       `json:"jobs"`
	AutoAccept  bool                  `json:"auto_accept"`
	ClipboardID string                `json:"clipboard_transfer_id,omitempty"`
}
type snapshot struct {
	model.Snapshot
	Running     bool   `json:"running"`
	AutoAccept  bool   `json:"auto_accept"`
	Clipboard   string `json:"clipboard"`
	ClipboardID string `json:"clipboard_transfer_id"`
}

// Client owns one installation identity, private inbox, persistent queue and listener.
// Call Close before creating another Client for the same directories.
type Client struct {
	mu                    sync.Mutex
	lifecycle             sync.Mutex
	stateDir, inbox, name string
	identity              *identity.Identity
	engine                *transfer.Engine
	clipboard             *clipboard.Manager
	state                 diskState
	notices               []string
	trustPersistenceError string
	clipboardText         string
	running, closed       bool
	address               string
	listener              net.Listener
	networkCtx            context.Context
	networkCancel         context.CancelFunc
	netWG                 sync.WaitGroup
	workerWG              sync.WaitGroup
	cancels               map[string]context.CancelFunc
	preparing             map[string][]string
	decisions             map[string]chan bool
	changes, wake         chan struct{}
	done                  chan struct{}
	observer              Observer
	// Only package-local tests can inject loopback. Public methods have no insecure mode.
	allowAddress func(netip.Addr) bool
}

// NewClient loads an existing identity and queue; it does not start network activity.
func NewClient(stateDirectory, inboxDirectory, deviceName string, events Observer) (*Client, error) {
	return newClient(stateDirectory, inboxDirectory, deviceName, events, tailscaleAddress)
}

func newClient(stateDirectory, inboxDirectory, deviceName string, events Observer, allow func(netip.Addr) bool) (*Client, error) {
	if !validName(deviceName) {
		return nil, errors.New("device name must be 1–128 printable UTF-8 bytes")
	}
	stateDir, err := privateDirectory(stateDirectory)
	if err != nil {
		return nil, err
	}
	inbox, err := privateDirectory(inboxDirectory)
	if err != nil {
		return nil, err
	}
	if within(stateDir, inbox) || within(inbox, stateDir) {
		return nil, errors.New("inbox and state directories must be separate")
	}
	if _, err = privateDirectory(filepath.Join(stateDir, "staging")); err != nil {
		return nil, err
	}
	id, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		return nil, err
	}
	c := &Client{stateDir: stateDir, inbox: inbox, name: deviceName, identity: id, clipboard: clipboard.New(stateDir), state: diskState{Version: 1, Peers: map[string]model.Peer{}, Trust: map[string]string{}, Jobs: map[string]*job{}}, cancels: map[string]context.CancelFunc{}, preparing: map[string][]string{}, decisions: map[string]chan bool{}, changes: make(chan struct{}, 1), wake: make(chan struct{}, 1), done: make(chan struct{}), observer: events, allowAddress: allow}
	if err = c.load(); err != nil {
		return nil, err
	}
	c.cleanupStagingLocked()
	c.clipboardText, err = c.clipboard.ReadInternal()
	if err != nil {
		return nil, err
	}
	c.engine, err = transfer.New(transfer.Options{Identity: id, StateDir: stateDir, ReceiveDir: inbox, Conflict: "rename", MaxBytes: 2 << 30, Hello: protocol.Hello{Name: deviceName, Hostname: deviceName, OS: "android", Arch: runtime.GOARCH, Version: model.Version, Capabilities: []string{"files", "text", "url", "sha256", "resume"}}, Trusted: c.trusted, Decide: c.decide, Progress: c.progress})
	if err != nil {
		return nil, err
	}
	c.workerWG.Add(1)
	go func() { defer c.workerWG.Done(); c.scheduler() }()
	if events != nil {
		go c.observe()
	}
	return c, nil
}

func validName(s string) bool {
	if s == "" || len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
func privateDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("app directory must be absolute")
	}
	path = filepath.Clean(path)
	if path == string(filepath.Separator) {
		return "", errors.New("app directory cannot be filesystem root")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if resolved != path {
		return "", errors.New("app directory must not contain symlinks")
	}
	if err = os.Chmod(path, 0700); err != nil {
		return "", err
	}
	return path, nil
}
func within(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func tailscaleAddress(a netip.Addr) bool {
	return a.IsValid() && a.Zone() == "" && (netip.MustParsePrefix("100.64.0.0/10").Contains(a) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(a))
}
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (c *Client) changedLocked() { signal(c.changes); signal(c.wake) }
func (c *Client) observe() {
	for {
		select {
		case <-c.done:
			return
		case <-c.changes:
			timer := time.NewTimer(75 * time.Millisecond)
			select {
			case <-c.done:
				timer.Stop()
				return
			case <-timer.C:
			}
			// No application mutex is held across foreign code.
			c.observer.OnChange(c.Snapshot())
		}
	}
}

// Snapshot returns current UI state, never remote filesystem destinations.
func (c *Client) Snapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := snapshot{Snapshot: model.Snapshot{Version: model.Version, Name: c.name, Fingerprint: c.identity.Fingerprint, Address: c.address, ReceiveDirectory: c.inbox, ClipboardBackend: "Relay Clipboard", Peers: []model.Peer{}, Transfers: []model.Transfer{}, History: []model.Transfer{}, Notices: append([]string(nil), c.notices...)}, Running: c.running, AutoAccept: c.state.AutoAccept, Clipboard: c.clipboardText, ClipboardID: c.state.ClipboardID}
	if c.trustPersistenceError != "" {
		s.Notices = append(s.Notices, c.trustPersistenceError)
	}
	if c.running {
		s.Tailscale = "Connected"
	} else {
		s.Tailscale = "Unavailable"
	}
	for _, p := range c.state.Peers {
		p.Trusted = c.state.Trust[p.ID] != "" && c.state.Trust[p.ID] == p.Fingerprint
		s.Peers = append(s.Peers, p)
	}
	for _, j := range c.state.Jobs {
		t := j.Transfer
		if t.Terminal() {
			s.History = append(s.History, t)
		} else {
			s.Transfers = append(s.Transfers, t)
		}
	}
	sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i].Name < s.Peers[j].Name })
	sort.Slice(s.Transfers, func(i, j int) bool { return s.Transfers[i].Started.After(s.Transfers[j].Started) })
	sort.Slice(s.History, func(i, j int) bool { return s.History[i].Updated.After(s.History[j].Updated) })
	b, _ := json.Marshal(s)
	return string(b)
}

// Start binds only the literal Tailscale IP selected by the native VPN helper.
// An empty address has the same effect as Stop. The v1 service uses port 7331.
func (c *Client) Start(bindAddress string) error {
	if bindAddress == "" {
		return c.Stop()
	}
	ip, err := netip.ParseAddr(bindAddress)
	if err != nil || !c.allowAddress(ip) {
		return errors.New("bind address must be a literal Tailscale IP")
	}
	return c.startAddress(net.JoinHostPort(ip.String(), "7331"))
}
func (c *Client) startAddress(address string) error {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("client is closed")
	}
	if c.running && c.address == address {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if err := c.stopNetwork(); err != nil {
		return err
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.listener = l
	c.address = l.Addr().String()
	c.networkCtx = ctx
	c.networkCancel = cancel
	c.running = true
	c.netWG.Add(1)
	c.changedLocked()
	c.mu.Unlock()
	go func() { defer c.netWG.Done(); c.accept(ctx, l) }()
	return nil
}
func (c *Client) accept(ctx context.Context, l net.Listener) {
	slots := make(chan struct{}, 8)
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		c.mu.Lock()
		if !c.running || ctx.Err() != nil {
			c.mu.Unlock()
			conn.Close()
			<-slots
			continue
		}
		c.netWG.Add(1)
		c.mu.Unlock()
		go func() {
			defer c.netWG.Done()
			defer func() { <-slots }()
			_ = c.engine.Handle(ctx, tls.Server(conn, c.identity.TLSConfig()))
		}()
	}
}

// Stop cancels discovery and active connections and keeps incomplete jobs resumable.
func (c *Client) Stop() error { c.lifecycle.Lock(); defer c.lifecycle.Unlock(); return c.stopNetwork() }
func (c *Client) stopNetwork() error {
	c.mu.Lock()
	c.running = false
	c.address = ""
	if c.networkCancel != nil {
		c.networkCancel()
		c.networkCancel = nil
	}
	if c.listener != nil {
		_ = c.listener.Close()
		c.listener = nil
	}
	for _, stop := range c.cancels {
		stop()
	}
	for id, ch := range c.decisions {
		select {
		case ch <- false:
		default:
		}
		delete(c.decisions, id)
	}
	for id, p := range c.state.Peers {
		p.Online = false
		c.state.Peers[id] = p
	}
	c.changedLocked()
	c.mu.Unlock()
	c.netWG.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.persistLocked()
	if err == nil {
		c.cleanupStagingLocked()
	} else {
		c.noticeLocked(err)
	}
	return err
}

// Close releases network and scheduler resources. It is idempotent.
func (c *Client) Close() error {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	c.mu.Unlock()
	err := c.stopNetwork()
	c.workerWG.Wait()
	return err
}
func (c *Client) trusted(fp string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, pin := range c.state.Trust {
		if fp != "" && fp == pin {
			return true
		}
	}
	return false
}
func (c *Client) noticeLocked(err error) {
	if err != nil {
		c.notices = append(c.notices, err.Error())
		if len(c.notices) > 8 {
			c.notices = c.notices[len(c.notices)-8:]
		}
		c.changedLocked()
	}
}

func (c *Client) load() error {
	p := filepath.Join(c.stateDir, "mobile.json")
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > maxStateBytes {
		return errors.New("invalid mobile state file")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var s diskState
	if err = json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("read mobile state: %w", err)
	}
	if s.Version != 1 || len(s.Peers) > maxPeers || len(s.Jobs) > maxPending+maxHistory || len(s.Trust) > maxPeers {
		return errors.New("unsupported or oversized mobile state")
	}
	if s.Peers == nil {
		s.Peers = map[string]model.Peer{}
	}
	if s.Trust == nil {
		s.Trust = map[string]string{}
	}
	if s.Jobs == nil {
		s.Jobs = map[string]*job{}
	}
	for id, p := range s.Peers {
		if p.ID != id {
			return errors.New("invalid stored peer")
		}
		if _, err = c.peerAddress(p.Address); err != nil {
			return fmt.Errorf("invalid stored peer: %w", err)
		}
		p.Online = false
		s.Peers[id] = p
	}
	for id, j := range s.Jobs {
		if j == nil || j.Transfer.ID != id {
			return errors.New("invalid stored job")
		}
		if !j.Transfer.Terminal() && j.Transfer.Status != "paused" {
			j.Transfer.Status = "interrupted"
		}
		if j.Prepared != nil {
			if err = transfer.Validate(j.Prepared.Offer, 2<<30); err != nil {
				return err
			}
			for _, p := range j.Prepared.Sources {
				if err = c.sourcePath(p); err != nil {
					j.Transfer.Status = "failed"
					j.Transfer.Error = "Staged source unavailable: " + err.Error()
				}
			}
		}
		if j.Transfer.Direction == "send" {
			j.Transfer.Destination = ""
		}
		if j.Transfer.Direction == "receive" {
			for _, p := range j.Transfer.Paths {
				if !within(c.inbox, p) {
					return errors.New("stored destination is outside inbox")
				}
			}
		}
	}
	c.state = s
	return nil
}
func (c *Client) trimLocked() {
	var terminal []*job
	for _, j := range c.state.Jobs {
		if j.Transfer.Terminal() {
			terminal = append(terminal, j)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].Transfer.Updated.After(terminal[j].Transfer.Updated) })
	remaining := len(terminal)
	for i := len(terminal) - 1; i >= 0 && remaining > maxHistory; i-- {
		j := terminal[i]
		if _, active := c.cancels[j.Transfer.ID]; !active {
			delete(c.state.Jobs, j.Transfer.ID)
			remaining--
		}
	}
}
func (c *Client) persistLocked() error {
	c.trimLocked()
	b, err := json.Marshal(c.state)
	if err != nil {
		return err
	}
	if len(b) > maxStateBytes {
		return errors.New("mobile queue exceeds storage limit")
	}
	f, err := os.CreateTemp(c.stateDir, ".mobile-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), filepath.Join(c.stateDir, "mobile.json")); err != nil {
		return err
	}
	d, err := os.Open(c.stateDir)
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Sync()
	if err == nil {
		c.trustPersistenceError = ""
	}
	return err
}
func (c *Client) SetAutoAccept(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("client is closed")
	}
	old := c.state.AutoAccept
	c.state.AutoAccept = enabled
	if err := c.persistLocked(); err != nil {
		c.state.AutoAccept = old
		return err
	}
	c.changedLocked()
	return nil
}
