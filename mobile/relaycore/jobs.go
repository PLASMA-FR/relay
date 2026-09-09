package relaycore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

func (c *Client) sourcePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("source must be an absolute app-private path")
	}
	root := filepath.Join(c.stateDir, "staging")
	if !within(root, path) {
		root = c.inbox
		if !within(root, path) {
			return errors.New("source must be in app staging or inbox")
		}
	}
	if path == root {
		return errors.New("select a file or subdirectory, not the app storage root")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("symlink sources are not supported")
	}
	return nil
}
func (c *Client) pendingLocked() int {
	n := 0
	for _, j := range c.state.Jobs {
		if !j.Transfer.Terminal() {
			n++
		}
	}
	return n
}
func result(id, message string) string {
	b, _ := json.Marshal(model.Result{OK: true, ID: id, Message: message})
	return string(b)
}

// Send commits a prepared manifest and queues a transfer. File paths must belong
// to stateDirectory/staging or inboxDirectory. Text stays in the private queue
// only until completion, and never appears in transfer history.
func (c *Client) Send(peerID, kind, text, pathsJSON string) (string, error) {
	if kind != "file" && kind != "text" && kind != "url" {
		return "", errors.New("kind must be file, text, or url")
	}
	if len(pathsJSON) > 1<<20 || len(text) > transfer.MaxText {
		return "", errors.New("content exceeds metadata limit")
	}
	var paths []string
	if pathsJSON != "" {
		if err := json.Unmarshal([]byte(pathsJSON), &paths); err != nil {
			return "", errors.New("paths must be a JSON array of absolute paths")
		}
	}
	if kind == "file" {
		if len(paths) == 0 || len(paths) > 32 {
			return "", errors.New("select between 1 and 32 paths")
		}
		if text != "" {
			return "", errors.New("file transfer cannot contain text")
		}
		for _, p := range paths {
			if err := c.sourcePath(p); err != nil {
				return "", err
			}
		}
	} else {
		if len(paths) > 0 {
			return "", errors.New("text transfer cannot contain files")
		}
		if text == "" {
			return "", errors.New("enter text to send")
		}
	}
	if kind == "url" {
		u, err := url.Parse(text)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", errors.New("URL must use http:// or https://")
		}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", errors.New("client is closed")
	}
	p, ok := c.state.Peers[peerID]
	pin := c.effectivePinLocked(peerID)
	if !ok || pin == "" || pin != p.Fingerprint {
		err := errors.New("verify and trust this device before sending")
		if c.state.TrustTailnet {
			err = errors.New("connect to Tailscale and wait for this device to become available")
		}
		if c.blockedLocked(p) {
			err = errors.New("device is blocked")
		}
		c.mu.Unlock()
		return "", err
	}
	if c.pendingLocked() >= maxPending {
		c.mu.Unlock()
		return "", errors.New("transfer queue is full")
	}
	id := transfer.NewID()
	c.preparing[id] = append([]string(nil), paths...)
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.preparing, id); c.mu.Unlock() }()
	name := ""
	if kind == "text" {
		name = "Clipboard text"
	} else if kind == "url" {
		name = "Shared URL"
	}
	prepared, err := transfer.Prepare(context.Background(), id, paths, kind, text, name)
	if err != nil {
		return "", err
	}
	if err = transfer.Validate(prepared.Offer, 2<<30); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("client is closed")
	}
	if c.effectivePinLocked(peerID) != pin || c.state.Peers[peerID].Fingerprint != pin {
		return "", errors.New("device trust changed")
	}
	if c.pendingLocked() >= maxPending {
		return "", errors.New("transfer queue is full")
	}
	now := time.Now()
	c.state.Jobs[id] = &job{Fingerprint: pin, Prepared: prepared, Transfer: model.Transfer{ID: id, Name: prepared.Offer.Name, Kind: kind, Peer: p.Name, PeerID: peerID, Direction: "send", Status: "queued", Total: prepared.Offer.Total, Paths: paths, Started: now, Updated: now}}
	if err = c.persistLocked(); err != nil {
		delete(c.state.Jobs, id)
		return "", err
	}
	c.changedLocked()
	return result(id, "Queued; transfer resumes when the device is reachable"), nil
}

func (c *Client) scheduler() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		case <-c.wake:
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		if !c.running {
			c.mu.Unlock()
			continue
		}
		for id, j := range c.state.Jobs {
			if len(c.cancels) >= 2 {
				break
			}
			t := j.Transfer
			if t.Direction != "send" || (t.Status != "queued" && t.Status != "interrupted") {
				continue
			}
			if _, active := c.cancels[id]; active {
				continue
			}
			p, ok := c.state.Peers[t.PeerID]
			if !ok || c.effectivePinLocked(p.ID) != j.Fingerprint || p.Fingerprint != j.Fingerprint {
				continue
			}
			if t.Status == "interrupted" && time.Since(t.Updated) < time.Duration(1<<min(t.Retry, 5))*time.Second {
				continue
			}
			ctx, cancel := context.WithCancel(c.networkCtx)
			c.cancels[id] = cancel
			j.Transfer.Status = "preparing"
			j.Transfer.Error = ""
			j.Transfer.Updated = time.Now()
			c.netWG.Add(1)
			go func(id string, p model.Peer, ctx context.Context) { defer c.netWG.Done(); c.runJob(ctx, id, p) }(id, p, ctx)
			c.changedLocked()
		}
		c.mu.Unlock()
	}
}
func (c *Client) runJob(ctx context.Context, id string, p model.Peer) {
	c.mu.Lock()
	j := c.state.Jobs[id]
	prepared := j.Prepared
	pin := j.Fingerprint
	c.mu.Unlock()
	var err error
	if prepared == nil {
		err = errors.New("prepared source is missing")
	} else {
		for _, path := range prepared.Sources {
			if err = c.sourcePath(path); err != nil {
				break
			}
		}
	}
	var receipt transfer.Receipt
	if err == nil {
		address, e := c.peerAddress(p.Address)
		err = e
		if err == nil {
			receipt, err = c.engine.Send(ctx, address, pin, prepared)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	j = c.state.Jobs[id]
	if stop := c.cancels[id]; stop != nil {
		stop()
	}
	delete(c.cancels, id)
	if j == nil {
		return
	}
	if j.Transfer.Status != "paused" && j.Transfer.Status != "cancelled" && j.Transfer.Status != "rejected" {
		if err == nil {
			j.Transfer.Status = "completed"
			j.Transfer.Bytes = receipt.Bytes
			j.Transfer.Total = receipt.Bytes
			j.Transfer.Verified = receipt.Verified
			j.Transfer.Error = ""
		} else {
			j.Transfer.Status = "interrupted"
			j.Transfer.Retry++
			j.Transfer.Error = err.Error()
			if permanent(err) {
				j.Transfer.Status = "failed"
			}
			if strings.Contains(err.Error(), "reject") {
				j.Transfer.Status = "rejected"
			}
		}
		j.Transfer.Updated = time.Now()
	}
	if j.Transfer.Terminal() && j.Transfer.Status != "failed" {
		j.Prepared = nil
	}
	if err := c.persistLocked(); err != nil {
		c.noticeLocked(err)
	} else {
		c.cleanupStagingLocked()
	}
	c.changedLocked()
}
func permanent(err error) bool {
	for _, s := range []string{"not trusted", "identity changed", "source changed", "different manifest", "resume prefix differs", "not supported", "exceeds configured", "invalid manifest", "unsafe path", "source is missing", "no such file", "symlink", "app staging"} {
		if strings.Contains(err.Error(), s) {
			return true
		}
	}
	return false
}

func (c *Client) decide(ctx context.Context, fp string, o transfer.Offer) error {
	b, _ := json.Marshal(o)
	h := sha256.Sum256(b)
	digest := hex.EncodeToString(h[:])
	c.mu.Lock()
	var peer model.Peer
	for id := range c.state.Peers {
		if fp != "" && c.effectivePinLocked(id) == fp {
			peer = c.state.Peers[id]
			break
		}
	}
	if peer.ID == "" || !c.running || c.closed {
		c.mu.Unlock()
		return errors.New("sender is not trusted or receiver unavailable")
	}
	j, exists := c.state.Jobs[o.ID]
	if exists && (j.Fingerprint != fp || j.Transfer.Direction != "receive" || j.Digest != digest) {
		c.mu.Unlock()
		return errors.New("transfer ID reused with a different manifest or sender")
	}
	if exists && (j.Transfer.Status == "paused" || j.Transfer.Status == "cancelled" || j.Transfer.Status == "rejected") {
		status := j.Transfer.Status
		c.mu.Unlock()
		return fmt.Errorf("transfer %s; resume on receiver first", status)
	}
	if !exists && c.pendingLocked() >= maxPending {
		c.mu.Unlock()
		return errors.New("receiver queue is full")
	}
	if len(c.decisions) >= 4 {
		c.mu.Unlock()
		return errors.New("receiver is busy")
	}
	now := time.Now()
	if !exists {
		j = &job{Fingerprint: fp, Digest: digest, Transfer: model.Transfer{ID: o.ID, Name: o.Name, Kind: o.Kind, Peer: peer.Name, PeerID: peer.ID, Direction: "receive", Started: now, Total: o.Total}}
		c.state.Jobs[o.ID] = j
	}
	if j.Accepted || c.state.AutoAccept {
		oldAccepted := j.Accepted
		j.Accepted = true
		j.Transfer.Status = "transferring"
		j.Transfer.Updated = now
		err := c.persistLocked()
		if err != nil {
			j.Accepted = oldAccepted
			j.Transfer.Status = "interrupted"
		}
		c.changedLocked()
		c.mu.Unlock()
		return err
	}
	j.Transfer.Status = "offered"
	j.Transfer.Updated = now
	ch := make(chan bool, 1)
	c.decisions[o.ID] = ch
	err := c.persistLocked()
	if err != nil {
		delete(c.decisions, o.ID)
		j.Transfer.Status = "interrupted"
		c.mu.Unlock()
		return err
	}
	c.changedLocked()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.decisions[o.ID] == ch {
			delete(c.decisions, o.ID)
		}
		c.mu.Unlock()
	}()
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	select {
	case accepted := <-ch:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if accepted {
			return nil
		}
		return errors.New("transfer rejected by receiver")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("offer expired; sender can retry")
	}
}

// Action accepts/rejects an incoming offer, or pauses/resumes/cancels a job.
func (c *Client) Action(action, id string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("client is closed")
	}
	j, ok := c.state.Jobs[id]
	if !ok {
		return "", errors.New("transfer not found")
	}
	old := *j
	old.Transfer = j.Transfer
	switch action {
	case "accept":
		if j.Transfer.Direction != "receive" || j.Transfer.Status != "offered" || c.decisions[id] == nil {
			return "", errors.New("no incoming offer awaiting approval")
		}
		if c.effectivePinLocked(j.Transfer.PeerID) != j.Fingerprint {
			return "", errors.New("sender is not trusted")
		}
		j.Accepted = true
		j.Transfer.Status = "transferring"
	case "reject":
		if j.Transfer.Direction != "receive" || j.Transfer.Status != "offered" {
			return "", errors.New("no incoming offer awaiting approval")
		}
		j.Transfer.Status = "rejected"
		j.Accepted = false
	case "pause":
		if j.Transfer.Terminal() {
			return "", errors.New("transfer is already finished")
		}
		j.Transfer.Status = "paused"
	case "resume":
		if j.Transfer.Status != "paused" && j.Transfer.Status != "interrupted" && j.Transfer.Status != "failed" {
			return "", errors.New("transfer cannot be resumed")
		}
		if _, active := c.cancels[id]; active {
			return "", errors.New("transfer is stopping; retry shortly")
		}
		if c.effectivePinLocked(j.Transfer.PeerID) != j.Fingerprint {
			if c.state.TrustTailnet {
				return "", errors.New("device is unavailable, blocked, or its identity changed")
			}
			return "", errors.New("verify and trust the device before resuming")
		}
		j.Transfer.Status = "interrupted"
		j.Transfer.Retry = 0
		j.Transfer.Error = ""
	case "cancel":
		if j.Transfer.Terminal() {
			return "", errors.New("transfer is already finished")
		}
		j.Transfer.Status = "cancelled"
		j.Accepted = false
	default:
		return "", errors.New("unknown transfer action")
	}
	j.Transfer.Updated = time.Now()
	if action == "cancel" || action == "reject" {
		j.Prepared = nil
	}
	if err := c.persistLocked(); err != nil {
		*j = old
		return "", err
	}
	if action == "accept" || action == "reject" {
		select {
		case c.decisions[id] <- (action == "accept"):
		default:
		}
	}
	if action == "pause" || action == "cancel" {
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
	c.cleanupStagingLocked()
	c.changedLocked()
	return result(id, "Transfer "+action), nil
}

func (c *Client) progress(e transfer.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.state.Jobs[e.ID]
	if !ok || j.Fingerprint != e.Peer || j.Transfer.Direction != e.Direction {
		return
	}
	t := &j.Transfer
	if t.Terminal() || t.Status == "paused" || (e.Direction == "receive" && e.Status == "offered") {
		return
	}
	now := time.Now()
	if elapsed := now.Sub(t.Updated).Seconds(); elapsed > 0 && e.Bytes >= t.Bytes {
		t.Speed = float64(e.Bytes-t.Bytes) / elapsed
	}
	t.Bytes = e.Bytes
	t.Total = e.Total
	t.Verified = e.Verified
	t.Updated = now
	t.Status = e.Status
	t.Error = e.Error
	if t.Speed > 0 {
		t.ETASeconds = float64(max(int64(0), t.Total-t.Bytes)) / t.Speed
	}
	if seconds := now.Sub(t.Started).Seconds(); seconds > 0 {
		t.AverageSpeed = float64(t.Bytes) / seconds
	}
	if e.Direction == "receive" && e.Status == "completed" {
		t.Paths = nil
		for _, path := range e.Paths {
			if within(c.inbox, path) {
				t.Paths = append(t.Paths, path)
			}
		}
		t.Destination = strings.Join(t.Paths, ", ")
		if e.Kind == "text" || e.Kind == "url" {
			if err := c.clipboard.WriteInternal(e.Text); err != nil {
				c.noticeLocked(fmt.Errorf("save Relay clipboard: %w", err))
			} else {
				c.clipboardText = e.Text
				c.state.ClipboardID = e.ID
			}
		}
	}
	if t.Terminal() && t.Status != "failed" {
		j.Prepared = nil
	}
	if t.Terminal() || t.Status == "interrupted" {
		c.noticeLocked(c.persistLocked())
	}
	c.changedLocked()
}
