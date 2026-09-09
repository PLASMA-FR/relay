package daemon

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PLASMA-FR/relay/internal/model"
)

func (d *Daemon) openSocket() (net.Listener, error) {
	parent := filepath.Dir(d.cfg.Paths.Socket)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("IPC socket directory must be owned by you with mode 0700; set RELAY_SOCKET inside a private directory")
	}
	if info, err = os.Lstat(d.cfg.Paths.Socket); err == nil {
		stat, ok = info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || !ok || int(stat.Uid) != os.Getuid() {
			return nil, errors.New("refusing to replace non-socket or foreign IPC path")
		}
		conn, e := net.DialTimeout("unix", d.cfg.Paths.Socket, 200*time.Millisecond)
		if e == nil {
			conn.Close()
			return nil, errors.New("Relay daemon already listening on IPC socket")
		}
		if err = os.Remove(d.cfg.Paths.Socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", d.cfg.Paths.Socket)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(d.cfg.Paths.Socket, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (d *Daemon) httpServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, d.snapshot()) })
	mux.HandleFunc("GET /v1/events", d.events)
	mux.HandleFunc("POST /v1/send", func(w http.ResponseWriter, r *http.Request) {
		var req model.SendRequest
		if err := decode(w, r, &req); err != nil {
			respondError(w, 400, err)
			return
		}
		result, err := d.send(req)
		if err != nil {
			respondError(w, 400, err)
			return
		}
		respond(w, 202, result)
	})
	mux.HandleFunc("POST /v1/action", func(w http.ResponseWriter, r *http.Request) {
		var action model.Action
		if err := decode(w, r, &action); err != nil {
			respondError(w, 400, err)
			return
		}
		result, err := d.action(r, action)
		if err != nil {
			respondError(w, 400, err)
			return
		}
		respond(w, 200, result)
		if action.Action == "shutdown" {
			// Commit the local response before cancellation closes HTTP clients.
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			d.mu.Lock()
			stop := d.stop
			d.mu.Unlock()
			if stop != nil {
				go stop()
			}
		}
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10}
}
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func respondError(w http.ResponseWriter, status int, err error) {
	respond(w, status, map[string]string{"error": err.Error()})
}

func (d *Daemon) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, 500, errors.New("streaming unavailable"))
		return
	}
	ch := make(chan struct{}, 1)
	d.mu.Lock()
	if len(d.subscribers) >= 32 {
		d.mu.Unlock()
		respondError(w, 429, errors.New("too many event subscribers"))
		return
	}
	d.subscribers[ch] = struct{}{}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.subscribers, ch); d.mu.Unlock() }()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	controller := http.NewResponseController(w)
	emit := func() error {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := enc.Encode(d.snapshot())
		flusher.Flush()
		return err
	}
	if err := emit(); err != nil {
		return
	}
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			// Coalesce bursty frame progress into at most ten IPC updates per second.
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			select {
			case <-ch:
			default:
			}
			if err := emit(); err != nil {
				return
			}
		case <-keepalive.C:
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(w, "\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (d *Daemon) action(r *http.Request, a model.Action) (model.Result, error) {
	result := model.Result{OK: true}
	switch a.Action {
	case "shutdown":
		d.mu.Lock()
		running := d.stop != nil
		d.mu.Unlock()
		if !running {
			return result, errors.New("daemon is not running")
		}
		result.Message = "Daemon is shutting down; active transfers remain resumable"
		return result, nil
	case "refresh":
		signal(d.refresh)
		result.Message = "Refreshing devices"
		return result, nil
	case "clipboard-show":
		text, err := d.clipboard.ReadInternal()
		result.Text = text
		return result, err
	case "clipboard-copy":
		if len(a.Text) > 1<<20 {
			return result, errors.New("clipboard exceeds 1 MiB")
		}
		return result, d.clipboard.Write(r.Context(), a.Text)
	case "pair", "trust", "untrust":
		if d.cfg.Network.TrustTailnet {
			return d.tailnetTrustAction(r, a)
		}
		if a.Action != "pair" {
			d.trustMu.Lock()
			defer d.trustMu.Unlock()
		}
		d.mu.Lock()
		p, err := d.findPeerLocked(a.Peer)
		if err != nil {
			d.mu.Unlock()
			return result, err
		}
		if a.Action == "pair" {
			d.mu.Unlock()
			d.probe(r.Context(), p.ID)
			d.mu.Lock()
			p = d.peers[p.ID]
			d.mu.Unlock()
			if !p.Relay && p.Fingerprint == "" {
				return result, errors.New(p.Error)
			}
			result.Peer = &p
			result.Message = "Verify this full fingerprint on the other device before trusting"
			return result, nil
		}
		previousPin := d.trust[p.ID]
		if a.Action == "trust" {
			fingerprint := strings.ToLower(strings.ReplaceAll(a.Fingerprint, ":", ""))
			decoded, e := hex.DecodeString(fingerprint)
			if e != nil || len(decoded) != 32 || p.Fingerprint == "" || fingerprint != p.Fingerprint {
				d.mu.Unlock()
				return result, errors.New("trust requires the complete currently observed fingerprint; use relay pair first")
			}
			d.trustPending[p.ID] = true
			d.trustBefore[p.ID] = previousPin
			d.trust[p.ID] = fingerprint
			d.blockPendingID = p.ID
			d.blockBefore = make(map[string]bool, len(d.blocked))
			for key, value := range d.blocked {
				d.blockBefore[key] = value
			}
			for _, key := range d.blockKeysLocked(p) {
				delete(d.blocked, key)
			}
			p.Blocked = false
			p.Trusted = true
			d.peers[p.ID] = p
			result.Message = "Trusted; repeat pairing on the other device to enable mutual transfer"
		} else {
			delete(d.trust, p.ID)
			p.Trusted = false
			d.peers[p.ID] = p
			for id, j := range d.jobs {
				if j.Transfer.PeerID == p.ID && !j.Transfer.Terminal() {
					j.Transfer.Status = "paused"
					j.Transfer.Error = "Device trust revoked"
					j.Transfer.Updated = time.Now()
					if stop := d.cancels[id]; stop != nil {
						stop()
					}
					d.engine.Cancel(id)
					if ch := d.decisions[id]; ch != nil {
						select {
						case ch <- false:
						default:
						}
					}
				}
			}
			result.Message = "Trust revoked; active transfers stopped"
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
				delete(d.trustPending, p.ID)
				delete(d.trustBefore, p.ID)
				d.blocked = d.blockBefore
				d.blockBefore = nil
				d.blockPendingID = ""
				if previousPin == "" {
					delete(d.trust, p.ID)
				} else {
					d.trust[p.ID] = previousPin
				}
				current := d.peers[p.ID]
				current.Blocked = d.blockedLocked(p.ID)
				current.Trusted = previousPin != "" && previousPin == current.Fingerprint && !current.Blocked
				d.peers[p.ID] = current
				d.durabilityNotice = "Trust change was not enabled because it could not be saved. Fix state-directory permissions or free disk space, then retry."
			} else {
				// Never restore revoked trust in memory merely because storage
				// failed. Explicitly tell the caller that restart is unsafe.
				d.durabilityNotice = "Trust is revoked for this running daemon, but revocation could not be saved. Do not restart Relay until storage is repaired and relay untrust succeeds; the previous saved trust may otherwise return."
			}
			d.notifyLocked()
			d.mu.Unlock()
			if a.Action == "untrust" {
				return result, fmt.Errorf("trust revoked in memory but NOT durable: %w; repair storage and repeat relay untrust before restarting", err)
			}
			return result, fmt.Errorf("trust change not enabled: %w; repair storage and retry", err)
		}
		signal(d.wake)
		signal(d.refresh)
		result.Peer = &p
		return result, nil
	case "accept", "reject", "pause", "resume", "cancel":
		d.mu.Lock()
		j, exists := d.jobs[a.ID]
		if !exists {
			d.mu.Unlock()
			return result, errors.New("transfer not found")
		}
		switch a.Action {
		case "accept", "reject":
			ch := d.decisions[a.ID]
			if ch == nil {
				d.mu.Unlock()
				return result, errors.New("offer is no longer waiting; resume or retry from the sender")
			}
			accepted := a.Action == "accept"
			if accepted {
				j.Accepted = true
				j.Transfer.Status = "transferring"
			} else {
				j.Transfer.Status = "rejected"
			}
			j.Accepted = accepted
			j.Transfer.Updated = time.Now()
			d.notifyLocked()
			d.mu.Unlock()
			if err := d.persist(); err != nil {
				d.mu.Lock()
				j.Accepted = false
				j.Transfer.Status = "offered"
				j.Transfer.Error = "Could not persist decision: " + err.Error()
				d.notifyLocked()
				d.mu.Unlock()
				return result, fmt.Errorf("persist decision: %w", err)
			}
			// No payload can start until its local approval is durable.
			select {
			case ch <- accepted:
			default:
			}
			result.ID = a.ID
			return result, nil
		case "pause", "cancel":
			if j.Transfer.Terminal() {
				d.mu.Unlock()
				return result, errors.New("transfer has already finished")
			}
			if a.Action == "pause" {
				j.Transfer.Status = "paused"
			} else {
				j.Transfer.Status = "cancelled"
			}
			if stop := d.cancels[a.ID]; stop != nil {
				stop()
			}
			d.engine.Cancel(a.ID)
			if ch := d.decisions[a.ID]; ch != nil {
				select {
				case ch <- false:
				default:
				}
			}
		case "resume":
			if j.Transfer.Status == "completed" || j.Transfer.Status == "cancelled" || j.Transfer.Status == "rejected" {
				d.mu.Unlock()
				return result, errors.New("transfer has ended; start a new send")
			}
			if j.Transfer.Direction == "receive" {
				j.Transfer.Status = "interrupted"
				result.Message = "Ready to receive; resume the transfer on the sender"
			} else {
				j.Transfer.Status = "queued"
				j.Transfer.Retry = 0
			}
			j.Transfer.Error = ""
		}
		j.Transfer.Updated = time.Now()
		req := j.Request
		cancelled := j.Transfer.Status == "cancelled"
		if cancelled {
			j.Request = model.SendRequest{}
		}
		d.notifyLocked()
		d.mu.Unlock()
		d.save()
		if cancelled {
			d.cleanSpool(req)
		}
		signal(d.wake)
		result.ID = a.ID
		return result, nil
	default:
		return result, fmt.Errorf("unknown action %q", a.Action)
	}
}
