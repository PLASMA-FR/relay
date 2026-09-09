// Package daemon owns peer connections, jobs, durable metadata, and private IPC.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/protocol"
	"github.com/PLASMA-FR/relay/internal/tailscale"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

type job struct {
	Transfer    model.Transfer    `json:"transfer"`
	Request     model.SendRequest `json:"request"`
	Fingerprint string            `json:"fingerprint"`
	Accepted    bool              `json:"accepted,omitempty"`
	OfferDigest string            `json:"offer_digest,omitempty"`
}
type diskState struct {
	Trust   map[string]string `json:"trust"`
	Blocked map[string]bool   `json:"blocked,omitempty"`
	Jobs    map[string]*job   `json:"jobs"`
	Peers   []model.Peer      `json:"peers"`
}
type Daemon struct {
	cfg              config.Config
	identity         *identity.Identity
	engine           *transfer.Engine
	clipboard        *clipboard.Manager
	mu               sync.Mutex
	peers            map[string]model.Peer
	trust            map[string]string
	autoPins         map[string]string
	blocked          map[string]bool
	blockPendingID   string
	blockBefore      map[string]bool
	members          map[string]tailscale.Device
	membersUntil     time.Time
	whoIs            func(context.Context, string) (tailscale.Device, error)
	readStatus       func(context.Context) (tailscale.Status, error)
	trustPending     map[string]bool
	trustBefore      map[string]string
	durabilityNotice string
	jobs             map[string]*job
	cancels          map[string]context.CancelFunc
	decisions        map[string]chan bool
	subscribers      map[chan struct{}]struct{}
	state, address   string
	notices          []string
	ctx              context.Context
	stop             context.CancelFunc
	wake, refresh    chan struct{}
	listener         net.Listener
	listenerAddress  string
	wg               sync.WaitGroup
	persistMu        sync.Mutex
	trustMu          sync.Mutex
}

func New(cfg config.Config) (*Daemon, error) {
	if err := config.Ensure(cfg); err != nil {
		return nil, err
	}
	id, err := identity.LoadOrCreate(cfg.Paths.StateDir)
	if err != nil {
		return nil, err
	}
	d := &Daemon{cfg: cfg, identity: id, clipboard: clipboard.New(cfg.Paths.StateDir), peers: map[string]model.Peer{}, trust: map[string]string{}, autoPins: map[string]string{}, blocked: map[string]bool{}, members: map[string]tailscale.Device{}, whoIs: tailscale.WhoIs, readStatus: tailscale.Read, trustPending: map[string]bool{}, trustBefore: map[string]string{}, jobs: map[string]*job{}, cancels: map[string]context.CancelFunc{}, decisions: map[string]chan bool{}, subscribers: map[chan struct{}]struct{}{}, state: "Starting", wake: make(chan struct{}, 1), refresh: make(chan struct{}, 1)}
	if err := d.load(); err != nil {
		return nil, err
	}
	d.clipboard.FallbackInternal = cfg.Clipboard.FallbackInternal
	host, _ := os.Hostname()
	d.engine, err = transfer.New(transfer.Options{Identity: id, StateDir: cfg.Paths.StateDir, ReceiveDir: cfg.Receive.Directory, Conflict: cfg.Receive.Conflict, MaxBytes: cfg.Receive.MaxBytes, Hello: protocol.Hello{Name: cfg.Name, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: model.Version, Protocol: model.ProtocolVersion, Capabilities: []string{"files", "directories", "text", "url", "sha256", "resume"}, Fingerprint: id.Fingerprint}, Trusted: d.trusted, AuthorizePeer: d.authorizePeer, PeerDirectory: d.peerDirectory, Decide: d.decide, Progress: d.progress})
	return d, err
}

func (d *Daemon) load() error {
	f, err := os.Open(filepath.Join(d.cfg.Paths.StateDir, "daemon.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	const maxState = 32 << 20
	if info, statErr := f.Stat(); statErr != nil {
		return statErr
	} else if info.Size() > maxState {
		return errors.New("daemon state exceeds 32 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxState+1))
	if err != nil {
		return err
	}
	if len(b) > maxState {
		return errors.New("daemon state exceeds 32 MiB")
	}
	var s diskState
	if err = json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("read daemon state: %w (restore daemon.json from backup; identity is separate)", err)
	}
	if s.Blocked != nil {
		d.blocked = s.Blocked
	}
	if s.Trust != nil {
		d.trust = s.Trust
	}
	if s.Jobs != nil {
		d.jobs = s.Jobs
	}
	for id, j := range d.jobs {
		if j == nil {
			delete(d.jobs, id)
			continue
		}
		if !j.Transfer.Terminal() && j.Transfer.Status != "paused" {
			j.Transfer.Status = "interrupted"
			j.Transfer.Error = "Recovered after daemon restart; ready to resume"
		}
	}
	for _, p := range s.Peers {
		p.Relay = false
		p.Online = false
		d.peers[p.ID] = p
	}
	return nil
}

func (d *Daemon) persist() error { return d.persistPending("") }

// persistPending atomically saves a proposed trust addition before publishing it
// to authorization gates. Unrelated state saves retain the prior durable pin.
func (d *Daemon) persistPending(commitID string) error {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	d.mu.Lock()
	// Keep history bounded without removing resumable work.
	var done []string
	for id, j := range d.jobs {
		if j.Transfer.Terminal() {
			done = append(done, id)
		}
	}
	sort.Slice(done, func(i, j int) bool { return d.jobs[done[i]].Transfer.Updated.After(d.jobs[done[j]].Transfer.Updated) })
	if len(done) > 500 {
		for _, id := range done[500:] {
			delete(d.jobs, id)
		}
	}
	savedTrust := make(map[string]string, len(d.trust))
	for id, pin := range d.trust {
		if d.trustPending[id] && id != commitID {
			pin = d.trustBefore[id]
		}
		if pin != "" {
			savedTrust[id] = pin
		}
	}
	savedBlocked := d.blocked
	if d.blockPendingID != "" && d.blockPendingID != commitID {
		savedBlocked = d.blockBefore
	}
	s := diskState{Trust: savedTrust, Blocked: savedBlocked, Jobs: d.jobs}
	for _, p := range d.peers {
		s.Peers = append(s.Peers, p)
	}
	b, err := json.Marshal(s)
	for len(b) > 16<<20 && len(done) > 0 {
		last := done[len(done)-1]
		done = done[:len(done)-1]
		delete(d.jobs, last)
		b, err = json.Marshal(s)
	}
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return errors.New("pending transfer metadata exceeds 16 MiB; finish or cancel transfers first")
	}
	f, err := os.CreateTemp(d.cfg.Paths.StateDir, ".daemon-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(d.cfg.Paths.StateDir, "daemon.json")); err != nil {
		return err
	}
	dir, err := os.Open(d.cfg.Paths.StateDir)
	if err == nil {
		defer dir.Close()
		err = dir.Sync()
	}
	if err == nil {
		d.mu.Lock()
		if commitID != "" {
			delete(d.trustPending, commitID)
			delete(d.trustBefore, commitID)
			if d.blockPendingID == commitID {
				d.blockPendingID = ""
				d.blockBefore = nil
			}
		}
		d.durabilityNotice = ""
		d.notifyLocked()
		d.mu.Unlock()
	}
	return err
}

func (d *Daemon) save() {
	if err := d.persist(); err != nil {
		d.mu.Lock()
		d.notices = []string{"Cannot persist transfer state: " + err.Error()}
		d.notifyLocked()
		d.mu.Unlock()
	}
}
func (d *Daemon) notifyLocked() {
	for ch := range d.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (d *Daemon) trusted(fp string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.peers {
		if fp != "" && d.effectivePinLocked(id) == fp {
			return true
		}
	}
	return false
}

func (d *Daemon) snapshot() model.Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := model.Snapshot{Version: model.Version, Name: d.cfg.Name, Fingerprint: d.identity.Fingerprint, Tailscale: d.state, Address: d.address, ReceiveDirectory: d.cfg.Receive.Directory, ClipboardBackend: d.clipboard.Backend(), Peers: []model.Peer{}, Transfers: []model.Transfer{}, History: []model.Transfer{}, Notices: append([]string(nil), d.notices...)}
	s.TrustMode = "manual"
	if d.cfg.Network.TrustTailnet {
		s.TrustMode = "tailnet"
	}
	if d.durabilityNotice != "" {
		s.Notices = append(s.Notices, d.durabilityNotice)
	}
	for _, p := range d.peers {
		p.Trusted = p.Fingerprint != "" && d.effectivePinLocked(p.ID) == p.Fingerprint
		p.Blocked = d.blockedLocked(p.ID)
		s.Peers = append(s.Peers, p)
	}
	sort.Slice(s.Peers, func(i, j int) bool {
		if s.Peers[i].Relay != s.Peers[j].Relay {
			return s.Peers[i].Relay
		}
		return strings.ToLower(s.Peers[i].Name) < strings.ToLower(s.Peers[j].Name)
	})
	for _, j := range d.jobs {
		t := j.Transfer
		t.Paths = append([]string(nil), t.Paths...)
		if t.Terminal() {
			s.History = append(s.History, t)
		} else {
			s.Transfers = append(s.Transfers, t)
		}
	}
	sort.Slice(s.Transfers, func(i, j int) bool { return s.Transfers[i].Started.Before(s.Transfers[j].Started) })
	sort.Slice(s.History, func(i, j int) bool { return s.History[i].Updated.After(s.History[j].Updated) })
	return s
}

func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.mu.Lock()
	d.ctx = ctx
	d.stop = cancel
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.stop = nil; d.mu.Unlock() }()
	// flock also prevents two daemons racing while replacing a stale IPC socket.
	lock, err := os.OpenFile(filepath.Join(d.cfg.Paths.StateDir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("Relay daemon already running for this state directory")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ipcListener, err := d.openSocket()
	if err != nil {
		return err
	}
	defer ipcListener.Close()
	defer os.Remove(d.cfg.Paths.Socket)
	server := d.httpServer()
	d.wg.Add(2)
	go func() { defer d.wg.Done(); d.discoveryLoop(ctx) }()
	go func() { defer d.wg.Done(); d.scheduler(ctx) }()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ipcListener) }()
	select {
	case <-ctx.Done():
		err = nil
	case err = <-serveErr:
		cancel()
	}
	cancel()
	server.Close()
	d.mu.Lock()
	if d.listener != nil {
		d.listener.Close()
	}
	for _, stop := range d.cancels {
		stop()
	}
	d.mu.Unlock()
	d.wg.Wait()
	d.save()
	return err
}

func (d *Daemon) discoveryLoop(ctx context.Context) {
	seconds := d.cfg.Network.DiscoverySeconds
	if seconds < 2 {
		seconds = 10
	}
	ticker := time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for {
		d.discover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.refresh:
		}
	}
}

func (d *Daemon) discover(ctx context.Context) {
	if !d.cfg.Network.TailscaleOnly {
		d.mu.Lock()
		d.state = "Loopback development mode"
		d.mu.Unlock()
		address := d.cfg.Network.Listen
		if _, _, e := net.SplitHostPort(address); e != nil {
			address = net.JoinHostPort(address, fmt.Sprint(d.cfg.Network.Port))
		}
		d.bind(ctx, address)
		return
	}
	status, err := d.readStatus(ctx)
	if err != nil {
		d.mu.Lock()
		d.state = "Unavailable"
		d.clearMembersLocked()
		d.notices = []string{err.Error()}
		for id, p := range d.peers {
			p.Relay = false
			p.Error = "Discovery unavailable; last known device"
			d.peers[id] = p
		}
		d.notifyLocked()
		d.mu.Unlock()
		d.bind(ctx, "")
		return
	}
	bind := ""
	if status.State == "Running" && len(status.Self.Addresses) > 0 {
		bind = net.JoinHostPort(status.Self.Addresses[0], fmt.Sprint(d.cfg.Network.Port))
	}
	d.mu.Lock()
	d.state = status.State
	d.updateMembersLocked(status)
	d.notices = nil
	d.address = ""
	if len(status.Self.Addresses) > 0 {
		d.address = status.Self.Addresses[0]
	}
	seen := map[string]bool{}
	for _, device := range status.Peers {
		if len(device.Addresses) == 0 {
			continue
		}
		seen[device.ID] = true
		p := d.peers[device.ID]
		p.ID = device.ID
		p.Hostname = device.Hostname
		if p.Name == "" {
			p.Name = device.Hostname
		}
		p.Address = device.Addresses[0]
		p.OS = device.OS
		p.Online = device.Online
		p.LastSeen = device.LastSeen
		if !p.Online {
			p.Relay = false
		}
		d.peers[p.ID] = p
	}
	for id, p := range d.peers {
		if !seen[id] {
			p.Online = false
			p.Relay = false
			d.peers[id] = p
		}
	}
	d.notifyLocked()
	d.mu.Unlock()
	d.bind(ctx, bind)
	if status.State != "Running" {
		return
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, device := range status.Peers {
		if !device.Online {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(id string) { defer wg.Done(); defer func() { <-sem }(); d.probe(ctx, id) }(device.ID)
	}
	wg.Wait()
	d.save()
	signal(d.wake)
}

func (d *Daemon) probe(ctx context.Context, id string) {
	d.mu.Lock()
	p, ok := d.peers[id]
	blocked := d.blockedLocked(id)
	d.mu.Unlock()
	if !ok || (d.cfg.Network.TrustTailnet && blocked) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	address := d.peerAddress(p)
	start := time.Now()
	hello, err := d.engine.Probe(ctx, address)
	if d.cfg.Network.TrustTailnet && err == nil {
		if err = d.learnTailnetPeer(ctx, address, hello.Fingerprint, &hello, id); err == nil {
			for _, capability := range hello.Capabilities {
				if capability == protocol.PeerDirectoryCapability {
					_, _ = d.engine.ExchangePeers(ctx, address, hello.Fingerprint, d.directoryHints())
					break
				}
			}
		}
		if err == nil {
			d.mu.Lock()
			p = d.peers[id]
			p.LatencyMS = time.Since(start).Milliseconds()
			d.peers[id] = p
			d.notifyLocked()
			d.mu.Unlock()
			return
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p = d.peers[id]
	// A completed probe may not update a peer whose discovery address changed.
	if d.peerAddress(p) != address {
		return
	}
	if err != nil && (hello.Fingerprint == "" || d.cfg.Network.TrustTailnet) {
		p.Relay = false
		p.Error = "Relay unavailable; start Relay on this device"
		if d.cfg.Network.TrustTailnet {
			delete(d.autoPins, id)
		}
	} else {
		applyHello(&p, hello)
		p.Relay = err == nil && hello.Protocol == model.ProtocolVersion
		p.LatencyMS = time.Since(start).Milliseconds()
		p.LastSeen = time.Now()
		p.Error = ""
		if !p.Relay {
			p.Error = "Protocol mismatch; update Relay on both devices"
		}
		if pin := d.trust[id]; pin != "" && pin != p.Fingerprint {
			p.Relay = false
			p.Error = "Identity changed; verify fingerprint before trusting again"
		}
	}
	d.peers[id] = p
	d.notifyLocked()
}

func applyHello(p *model.Peer, hello protocol.Hello) {
	p.Fingerprint = hello.Fingerprint
	p.Name = limitText(hello.Name, 128)
	p.Arch = limitText(hello.Arch, 32)
	p.Version = limitText(hello.Version, 32)
	p.Protocol = hello.Protocol
	p.Capabilities = nil
	for _, capability := range hello.Capabilities[:min(len(hello.Capabilities), 32)] {
		p.Capabilities = append(p.Capabilities, limitText(capability, 64))
	}
}
func (d *Daemon) peerAddress(p model.Peer) string {
	if _, _, err := net.SplitHostPort(p.Address); err == nil {
		return p.Address
	}
	return net.JoinHostPort(p.Address, fmt.Sprint(d.cfg.Network.Port))
}

func (d *Daemon) bind(ctx context.Context, address string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if address == d.listenerAddress {
		return
	}
	if d.listener != nil {
		d.listener.Close()
		d.listener = nil
	}
	d.listenerAddress = ""
	if address == "" {
		return
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		d.notices = []string{"Invalid listener: " + err.Error()}
		d.notifyLocked()
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || (d.cfg.Network.TailscaleOnly && !tailscale.IsAddress(host)) || (!d.cfg.Network.TailscaleOnly && !ip.IsLoopback()) {
		d.notices = []string{"Refused listener: only Tailscale IPs or explicit development loopback are allowed"}
		d.notifyLocked()
		return
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		d.notices = []string{"Relay listener: " + err.Error()}
		d.notifyLocked()
		return
	}
	d.listener = ln
	d.listenerAddress = address
	if !d.cfg.Network.TailscaleOnly {
		d.address = ln.Addr().String()
	}
	d.notifyLocked()
	d.wg.Add(1)
	go func() { defer d.wg.Done(); d.servePeers(ctx, ln) }()
}

func (d *Daemon) servePeers(ctx context.Context, ln net.Listener) {
	max := d.cfg.Network.MaxConcurrent
	if max < 1 {
		max = 2
	}
	sem := make(chan struct{}, max+8)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { <-sem }()
			defer conn.Close()
			tlsConn := tls.Server(conn, d.identity.TLSConfig())
			_ = d.engine.Handle(ctx, tlsConn)
		}()
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func limitText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
