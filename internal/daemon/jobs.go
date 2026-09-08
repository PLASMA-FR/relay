package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/transfer"
)

func (d *Daemon) findPeerLocked(query string) (model.Peer, error) {
	var matches []model.Peer
	for _, p := range d.peers {
		if p.ID == query || p.Fingerprint == query {
			return p, nil
		}
		if strings.EqualFold(p.Name, query) || strings.EqualFold(p.Hostname, query) || p.Address == query {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return model.Peer{}, errors.New("device name is ambiguous; use its device ID")
	}
	return model.Peer{}, fmt.Errorf("device %q not found; run relay devices or refresh", query)
}

func (d *Daemon) send(req model.SendRequest) (model.Result, error) {
	if req.Kind == "" || req.Kind == "files" {
		req.Kind = "file"
	}
	if req.Kind == "clipboard" {
		req.Kind = "text"
	}
	if req.Kind != "file" && req.Kind != "directory" && req.Kind != "text" && req.Kind != "url" {
		return model.Result{}, errors.New("kind must be files, text, or url")
	}
	if len(req.Text) > 1<<20 {
		return model.Result{}, errors.New("text exceeds 1 MiB; send it as a file")
	}
	if req.Kind == "file" || req.Kind == "directory" {
		if len(req.Paths) == 0 || len(req.Paths) > 1024 {
			return model.Result{}, errors.New("select between 1 and 1024 paths")
		}
		for i, p := range req.Paths {
			absolute, err := filepath.Abs(p)
			if err != nil {
				return model.Result{}, err
			}
			info, err := os.Lstat(absolute)
			if err != nil {
				return model.Result{}, fmt.Errorf("read source: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return model.Result{}, errors.New("symlinks are not transferred in V1")
			}
			req.Paths[i] = absolute
		}
	}
	if req.Kind == "url" {
		u, err := url.Parse(req.Text)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return model.Result{}, errors.New("URL must use http:// or https://")
		}
	}
	d.mu.Lock()
	p, err := d.findPeerLocked(req.Peer)
	if err != nil {
		d.mu.Unlock()
		return model.Result{}, err
	}
	pin := d.trust[p.ID]
	if pin == "" || pin != p.Fingerprint || d.trustPending[p.ID] {
		d.mu.Unlock()
		return model.Result{}, errors.New("device is not trusted; run relay pair, verify its fingerprint, then relay trust on both devices")
	}
	pending := 0
	for _, j := range d.jobs {
		if !j.Transfer.Terminal() {
			pending++
		}
	}
	if pending >= 256 {
		d.mu.Unlock()
		return model.Result{}, errors.New("transfer queue is full; cancel or finish pending transfers")
	}
	id := newID()
	name := req.Name
	if name == "" {
		if req.Kind == "file" || req.Kind == "directory" {
			name = filepath.Base(req.Paths[0])
			if len(req.Paths) > 1 {
				name = fmt.Sprintf("%s + %d more", name, len(req.Paths)-1)
			}
		} else if req.Kind == "url" {
			name = "Shared URL"
		} else {
			name = "Clipboard text"
		}
	}
	now := time.Now()
	req.Peer = p.ID
	d.jobs[id] = &job{Transfer: model.Transfer{ID: id, Name: name, Peer: p.Name, PeerID: p.ID, Direction: "send", Kind: req.Kind, Status: "queued", Started: now, Updated: now, Paths: append([]string(nil), req.Paths...)}, Request: req, Fingerprint: pin}
	d.notifyLocked()
	d.mu.Unlock()
	if err = d.persist(); err != nil {
		d.mu.Lock()
		delete(d.jobs, id)
		d.notifyLocked()
		d.mu.Unlock()
		return model.Result{}, err
	}
	signal(d.wake)
	return model.Result{OK: true, ID: id, Message: "Queued; transfer continues when the device is reachable"}, nil
}

func (d *Daemon) scheduler(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		d.mu.Lock()
		limit := d.cfg.Network.MaxConcurrent
		if limit < 1 {
			limit = 2
		}
		active := len(d.cancels)
		var ready []*job
		for _, j := range d.jobs {
			if active >= limit {
				break
			}
			t := j.Transfer
			if t.Direction != "send" || (t.Status != "queued" && t.Status != "interrupted") {
				continue
			}
			if _, running := d.cancels[t.ID]; running {
				continue
			}
			p, exists := d.peers[t.PeerID]
			if !exists || !p.Online || !p.Relay || d.trustPending[p.ID] || d.trust[p.ID] != j.Fingerprint || p.Fingerprint != j.Fingerprint {
				continue
			}
			delay := time.Duration(1<<min(t.Retry, 5)) * time.Second
			if t.Status == "interrupted" && time.Since(t.Updated) < delay {
				continue
			}
			runCtx, stop := context.WithCancel(ctx)
			d.cancels[t.ID] = stop
			j.Transfer.Status = "preparing"
			j.Transfer.Error = ""
			j.Transfer.Updated = time.Now()
			ready = append(ready, j)
			active++
			d.wg.Add(1)
			go func(id string, p model.Peer, runCtx context.Context) { defer d.wg.Done(); d.runJob(runCtx, id, p) }(t.ID, p, runCtx)
		}
		if len(ready) > 0 {
			d.notifyLocked()
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

func (d *Daemon) runJob(ctx context.Context, id string, p model.Peer) {
	d.mu.Lock()
	j := d.jobs[id]
	req := j.Request
	pin := j.Fingerprint
	d.mu.Unlock()
	prepared, err := transfer.Prepare(ctx, id, req.Paths, req.Kind, req.Text, req.Name)
	var receipt transfer.Receipt
	if err == nil {
		receipt, err = d.engine.Send(ctx, d.peerAddress(p), pin, prepared)
	}
	d.mu.Lock()
	j = d.jobs[id]
	delete(d.cancels, id)
	if j.Transfer.Status != "paused" && j.Transfer.Status != "cancelled" && j.Transfer.Status != "rejected" {
		if err != nil {
			if prepared == nil || permanentError(err) {
				j.Transfer.Status = "failed"
			} else if strings.Contains(err.Error(), "rejected") {
				j.Transfer.Status = "rejected"
			} else {
				j.Transfer.Status = "interrupted"
				j.Transfer.Retry++
			}
			j.Transfer.Error = err.Error()
		} else {
			j.Transfer.Status = "completed"
			j.Transfer.Verified = receipt.Verified
			j.Transfer.Bytes = receipt.Bytes
			j.Transfer.Total = receipt.Bytes
			j.Transfer.Destination = strings.Join(receipt.Paths, ", ")
		}
		j.Transfer.Updated = time.Now()
	}
	d.notifyLocked()
	done := j.Transfer.Status == "completed" || j.Transfer.Status == "cancelled" || j.Transfer.Status == "rejected"
	if done {
		j.Request = model.SendRequest{}
	}
	d.mu.Unlock()
	d.save()
	if done {
		d.cleanSpool(req)
	}
	signal(d.wake)
}

func (d *Daemon) decide(ctx context.Context, fp string, offer transfer.Offer) error {
	encoded, err := json.Marshal(offer)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
	d.mu.Lock()
	var peer model.Peer
	for id, pin := range d.trust {
		if pin == fp && !d.trustPending[id] {
			peer = d.peers[id]
			break
		}
	}
	if peer.ID == "" {
		d.mu.Unlock()
		return errors.New("sender is not trusted")
	}
	pending, receiving := 0, 0
	for id, existing := range d.jobs {
		if !existing.Transfer.Terminal() {
			pending++
		}
		if id != offer.ID && existing.Transfer.Direction == "receive" && (existing.Transfer.Status == "offered" || existing.Transfer.Status == "transferring" || existing.Transfer.Status == "verifying") {
			receiving++
		}
	}
	if pending >= 256 {
		d.mu.Unlock()
		return errors.New("receiver queue is full")
	}
	if receiving >= max(1, d.cfg.Network.MaxConcurrent) {
		d.mu.Unlock()
		return errors.New("receiver is busy; retry shortly")
	}
	j, exists := d.jobs[offer.ID]
	if exists && (j.Fingerprint != fp || j.Transfer.Direction != "receive") {
		d.mu.Unlock()
		return errors.New("transfer ID already belongs to another transfer")
	}
	if exists && (j.Transfer.Status == "cancelled" || j.Transfer.Status == "rejected" || j.Transfer.Status == "paused") {
		d.mu.Unlock()
		return fmt.Errorf("transfer %s; resume it on the receiver first", j.Transfer.Status)
	}
	if exists && j.OfferDigest != "" && j.OfferDigest != digest {
		d.mu.Unlock()
		return errors.New("transfer ID reused with a different manifest")
	}
	previouslyAccepted := exists && j.Accepted && j.OfferDigest == digest
	now := time.Now()
	if !exists {
		j = &job{Fingerprint: fp, Transfer: model.Transfer{ID: offer.ID, Name: offer.Name, Kind: offer.Kind, Peer: peer.Name, PeerID: peer.ID, Direction: "receive", Started: now}}
		d.jobs[offer.ID] = j
	}
	// Approval is bound to the exact offer before the engine creates a payload
	// journal. Legacy jobs lacking this binding require fresh manual approval.
	j.OfferDigest = digest
	j.Accepted = previouslyAccepted
	j.Transfer.Status = "offered"
	j.Transfer.Total = offer.Total
	j.Transfer.Updated = now
	ch := make(chan bool, 1)
	d.decisions[offer.ID] = ch
	d.notifyLocked()
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.decisions, offer.ID); d.mu.Unlock() }()
	if err = d.persist(); err != nil {
		return fmt.Errorf("persist incoming offer: %w", err)
	}
	if d.cfg.Receive.AutoAcceptTrusted || previouslyAccepted {
		d.mu.Lock()
		j.Accepted = true
		j.Transfer.Status = "transferring"
		d.notifyLocked()
		d.mu.Unlock()
		if err = d.persist(); err != nil {
			d.mu.Lock()
			j.Accepted = false
			j.Transfer.Status = "interrupted"
			j.Transfer.Error = "Could not persist acceptance: " + err.Error()
			d.notifyLocked()
			d.mu.Unlock()
			return fmt.Errorf("persist acceptance: %w", err)
		}
		return nil
	}
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	select {
	case accept := <-ch:
		if accept {
			return nil
		}
		return errors.New("transfer rejected by receiver")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("offer expired; accept promptly or enable auto_accept_trusted")
	}
}

func (d *Daemon) progress(e transfer.Event) {
	d.mu.Lock()
	current, found := d.jobs[e.ID]
	valid := found && current.Transfer.Direction == e.Direction && (e.Peer == "" || current.Fingerprint == e.Peer) && current.Transfer.Status != "paused" && current.Transfer.Status != "cancelled" && current.Transfer.Status != "rejected"
	d.mu.Unlock()
	if !valid {
		return
	}
	if e.Direction == "receive" && e.Status == "completed" && (e.Kind == "text" || e.Kind == "url") {
		if err := d.clipboard.WriteInternal(e.Text); err != nil {
			d.mu.Lock()
			d.notices = append(d.notices, "Could not save Relay clipboard: "+err.Error())
			d.notifyLocked()
			d.mu.Unlock()
		}
	}
	if e.Direction == "receive" && e.Status == "offered" {
		return
	}
	d.mu.Lock()
	j, exists := d.jobs[e.ID]
	if !exists {
		d.mu.Unlock()
		return
	}
	t := &j.Transfer
	if t.Status == "cancelled" || t.Status == "paused" || t.Status == "rejected" {
		d.mu.Unlock()
		return
	}
	now := time.Now()
	elapsed := now.Sub(t.Updated).Seconds()
	if elapsed > 0 && e.Bytes >= t.Bytes {
		instant := float64(e.Bytes-t.Bytes) / elapsed
		if t.Speed == 0 {
			t.Speed = instant
		} else {
			t.Speed = t.Speed*.7 + instant*.3
		}
	}
	t.Bytes = e.Bytes
	t.Total = e.Total
	t.Updated = now
	if seconds := now.Sub(t.Started).Seconds(); seconds > 0 {
		t.AverageSpeed = float64(t.Bytes) / seconds
	}
	if t.Speed > 0 {
		t.ETASeconds = float64(max(int64(0), t.Total-t.Bytes)) / t.Speed
	}
	if e.Status != "" {
		t.Status = e.Status
	}
	t.Error = e.Error
	t.Verified = e.Verified
	if len(e.Paths) > 0 {
		t.Destination = strings.Join(e.Paths, ", ")
	}
	d.notifyLocked()
	done := t.Terminal()
	d.mu.Unlock()

	if done {
		d.save()
	}
}

func (d *Daemon) cleanSpool(req model.SendRequest) {
	if !req.Spool {
		return
	}
	root := filepath.Join(d.cfg.Paths.StateDir, "stdin")
	for _, p := range req.Paths {
		parent := filepath.Dir(p)
		if filepath.Dir(parent) != root || filepath.Base(parent) == "." {
			continue
		}
		if info, err := os.Lstat(parent); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		_ = os.Remove(p)
		_ = os.Remove(parent)
	}
}

func permanentError(err error) bool {
	for _, part := range []string{"not trusted", "identity changed", "source changed", "different manifest", "resume prefix differs", "not supported", "exceeds configured", "invalid manifest", "unsafe path"} {
		if strings.Contains(err.Error(), part) {
			return true
		}
	}
	return false
}
