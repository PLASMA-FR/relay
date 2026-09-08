package transfer

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/protocol"
)

const idleTimeout = 2 * time.Minute

type Engine struct {
	opts   Options
	mu     sync.Mutex
	active map[string]net.Conn
}
type resume struct {
	Offset int64  `json:"offset"`
	SHA256 string `json:"sha256"`
	Skip   bool   `json:"skip,omitempty"`
}
type checksum struct {
	SHA256 string `json:"sha256"`
}
type record struct {
	Offer       Offer             `json:"offer"`
	OfferDigest string            `json:"offer_digest"`
	Checksums   map[string]string `json:"checksums"`
	Names       map[string]string `json:"names"`
	Done        map[string]string `json:"done"`
	Skipped     map[string]bool   `json:"skipped"`
	Receipt     *Receipt          `json:"receipt,omitempty"`
}

func New(o Options) (*Engine, error) {
	if o.Identity == nil {
		return nil, errors.New("identity required")
	}
	if o.ReceiveDir == "" || o.StateDir == "" {
		return nil, errors.New("receive and state directories required")
	}
	if o.Conflict == "" {
		o.Conflict = "rename"
	}
	if o.Conflict != "rename" && o.Conflict != "skip" && o.Conflict != "cancel" && o.Conflict != "replace" {
		return nil, errors.New("unknown collision policy")
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1 << 40
	}
	if e := os.MkdirAll(o.ReceiveDir, 0700); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(filepath.Join(o.StateDir, "incoming"), 0700); e != nil {
		return nil, e
	}
	o.Hello.Protocol = protocol.Version
	o.Hello.Fingerprint = o.Identity.Fingerprint
	return &Engine{opts: o, active: map[string]net.Conn{}}, nil
}
func (e *Engine) Cancel(id string) bool {
	e.mu.Lock()
	c, ok := e.active[id]
	e.mu.Unlock()
	if ok {
		abort(c)
	}
	return ok
}

// Cancellation must not wait for a TLS close-notify write on a stalled peer.
func abort(c net.Conn) {
	if tlsConn, ok := c.(*tls.Conn); ok {
		_ = tlsConn.NetConn().Close()
	} else {
		_ = c.Close()
	}
}
func (e *Engine) register(id string, c net.Conn) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.active[id]; ok {
		return errors.New("transfer is already active")
	}
	e.active[id] = c
	return nil
}
func (e *Engine) unregister(id string)   { e.mu.Lock(); delete(e.active, id); e.mu.Unlock() }
func (e *Engine) trusted(fp string) bool { return e.opts.Trusted != nil && e.opts.Trusted(fp) }
func (e *Engine) event(o Offer, peer, dir, status string, n int64, err error, current ...string) {
	if e.opts.Progress != nil {
		v := Event{ID: o.ID, Peer: peer, Direction: dir, Status: status, Name: o.Name, Kind: o.Kind, Bytes: n, Total: o.Total}
		if len(current) > 0 {
			v.CurrentFile = current[0]
		}
		if err != nil {
			v.Error = err.Error()
		}
		e.opts.Progress(v)
	}
}
func stopOnCancel(ctx context.Context, c net.Conn) func() {
	stop := context.AfterFunc(ctx, func() { abort(c) })
	return func() { stop() }
}
func deadline(c net.Conn) { _ = c.SetDeadline(time.Now().Add(idleTimeout)) }
func dial(ctx context.Context, address string, id *identity.Identity) (*tls.Conn, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, Config: id.TLSConfig()}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	return c.(*tls.Conn), nil
}
func peerFP(c *tls.Conn) string { return identity.Fingerprint(c.ConnectionState().PeerCertificates[0]) }
func (e *Engine) Probe(ctx context.Context, address string) (protocol.Hello, error) {
	c, err := dial(ctx, address, e.opts.Identity)
	if err != nil {
		return protocol.Hello{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = protocol.WriteJSON(c, protocol.HelloFrame, struct{}{}); err != nil {
		return protocol.Hello{}, err
	}
	var h protocol.Hello
	err = protocol.ReadJSON(c, protocol.HelloFrame, &h)
	h.Fingerprint = peerFP(c)
	if err == nil && h.Protocol != protocol.Version {
		err = errors.New("incompatible Relay protocol")
	}
	return h, err
}
func (e *Engine) Handle(ctx context.Context, c *tls.Conn) (err error) {
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if err = c.HandshakeContext(ctx); err != nil {
		return err
	}
	fp := peerFP(c)
	first, err := protocol.Read(c)
	if err != nil {
		return err
	}
	if first.Type == protocol.HelloFrame {
		return protocol.WriteJSON(c, protocol.HelloFrame, e.opts.Hello)
	}
	defer func() {
		if err != nil {
			_ = protocol.Write(c, protocol.ErrorFrame, []byte(err.Error()))
		}
	}()
	if !e.trusted(fp) {
		return errors.New("peer is not trusted; verify fingerprints and trust on both devices")
	}
	if first.Type != protocol.OfferFrame {
		return errors.New("expected transfer offer")
	}
	var o Offer
	if err = json.Unmarshal(first.Data, &o); err != nil {
		return errors.New("invalid offer")
	}
	if err = Validate(o, e.opts.MaxBytes); err != nil {
		return err
	}
	if err = e.register(o.ID, c); err != nil {
		return err
	}
	defer e.unregister(o.ID)
	var transferred int64
	defer func() {
		if err != nil {
			e.event(o, fp, "receive", "interrupted", transferred, err)
		}
	}()
	e.event(o, fp, "receive", "offered", 0, nil)
	// Approval may take longer than the transfer idle timeout. Cancellation still closes c.
	_ = c.SetDeadline(time.Time{})
	if e.opts.Decide == nil {
		return errors.New("receiver has no acceptance policy")
	}
	if err = e.opts.Decide(ctx, fp, o); err != nil {
		return err
	}
	if !e.trusted(fp) {
		return errors.New("trust revoked")
	}
	deadline(c)
	if err = protocol.WriteJSON(c, protocol.DecisionFrame, struct {
		Accepted bool `json:"accepted"`
	}{true}); err != nil {
		return err
	}
	receipt, err := e.receive(ctx, c, fp, o, &transferred)
	if err != nil {
		return err
	}
	if err = protocol.WriteJSON(c, protocol.CompleteFrame, receipt); err != nil {
		return err
	}
	if e.opts.Progress != nil {
		e.opts.Progress(Event{ID: o.ID, Peer: fp, Direction: "receive", Status: "completed", Name: o.Name, Kind: o.Kind, Bytes: o.Total, Total: o.Total, Verified: receipt.Verified, Paths: receipt.Paths, Text: receipt.Text})
	}
	return nil
}

func (e *Engine) Send(ctx context.Context, address, expectedFingerprint string, p *Prepared) (receipt Receipt, err error) {
	if p == nil {
		return receipt, errors.New("missing prepared transfer")
	}
	o := p.Offer
	if err = Validate(o, 1<<50); err != nil {
		return receipt, err
	}
	c, err := dial(ctx, address, e.opts.Identity)
	if err != nil {
		return receipt, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	fp := peerFP(c)
	if expectedFingerprint == "" || fp != expectedFingerprint {
		return receipt, errors.New("peer identity changed: verify its full fingerprint before trusting")
	}
	if !e.trusted(fp) {
		return receipt, errors.New("peer is not trusted")
	}
	if err = e.register(o.ID, c); err != nil {
		return receipt, err
	}
	defer e.unregister(o.ID)
	var sent int64
	defer func() {
		if err != nil {
			e.event(o, fp, "send", "interrupted", sent, err)
		}
	}()
	e.event(o, fp, "send", "offered", 0, nil)
	deadline(c)
	if err = protocol.WriteJSON(c, protocol.OfferFrame, o); err != nil {
		return receipt, err
	}
	// Human approval is allowed up to 24 hours, while context remains cancellable.
	_ = c.SetDeadline(time.Now().Add(24 * time.Hour))
	var accepted struct {
		Accepted bool `json:"accepted"`
	}
	if err = protocol.ReadJSON(c, protocol.DecisionFrame, &accepted); err != nil {
		return receipt, err
	}
	if !accepted.Accepted {
		return receipt, errors.New("transfer rejected")
	}
	deadline(c)
	first, err := readFrame(c)
	if err != nil {
		return receipt, err
	}
	// Completed receipts survive crashes and acknowledgement loss: retries are idempotent.
	if first.Type == protocol.CompleteFrame {
		err = json.Unmarshal(first.Data, &receipt)
		if err == nil && !receipt.Verified {
			err = errors.New("receiver did not verify transfer")
		}
		if err == nil && e.opts.Progress != nil {
			e.opts.Progress(Event{ID: o.ID, Peer: fp, Direction: "send", Status: "completed", Name: o.Name, Kind: o.Kind, Bytes: o.Total, Total: o.Total, Verified: true, Paths: receipt.Paths, Text: receipt.Text})
		}
		return receipt, err
	}
	if first.Type == protocol.ErrorFrame {
		return receipt, fmt.Errorf("peer: %s", first.Data)
	}
	buf := make([]byte, protocol.ChunkSize)
	lastProgress := time.Time{}
	for _, entry := range o.Entries {
		if entry.Directory {
			continue
		}
		var r resume
		if first.Type != protocol.ResumeFrame {
			return receipt, errors.New("expected resume state")
		}
		if err = json.Unmarshal(first.Data, &r); err != nil {
			return receipt, err
		}
		if r.Skip {
			sent += entry.Size
			deadline(c)
			first, err = readFrame(c)
			if err != nil {
				return receipt, err
			}
			continue
		}
		if r.Offset < 0 || r.Offset > entry.Size {
			return receipt, errors.New("invalid resume offset")
		}
		source, ok := p.Sources[entry.Path]
		if !ok {
			return receipt, errors.New("source missing from manifest")
		}
		f, openErr := openSource(source)
		if openErr != nil {
			return receipt, openErr
		}
		info, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return receipt, statErr
		}
		if info.Size() != entry.Size || info.ModTime().UnixNano() != entry.ModTime {
			f.Close()
			return receipt, errors.New("source changed since offer; start a new transfer")
		}
		h := sha256.New()
		if _, err = hashProgress(ctx, c, h, io.LimitReader(f, r.Offset), buf); err != nil {
			f.Close()
			return receipt, err
		}
		if hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
			f.Close()
			return receipt, errors.New("resume prefix differs from source; cancel and start a new transfer")
		}
		sent += r.Offset
		remaining := entry.Size - r.Offset
		for remaining > 0 {
			if err = ctx.Err(); err != nil {
				break
			}
			if !e.trusted(fp) {
				err = errors.New("trust revoked")
				break
			}
			n := int64(len(buf))
			if remaining < n {
				n = remaining
			}
			var count int
			count, err = io.ReadFull(f, buf[:n])
			if err != nil {
				break
			}
			h.Write(buf[:count])
			deadline(c)
			if err = protocol.Write(c, protocol.DataFrame, buf[:count]); err != nil {
				break
			}
			remaining -= int64(count)
			sent += int64(count)
			if time.Since(lastProgress) > 100*time.Millisecond {
				e.event(o, fp, "send", "transferring", sent, nil, entry.Path)
				lastProgress = time.Now()
			}
		}
		if err == nil {
			finalInfo, statErr := f.Stat()
			if statErr != nil {
				err = statErr
			} else if finalInfo.Size() != entry.Size || finalInfo.ModTime().UnixNano() != entry.ModTime {
				err = errors.New("source changed during transfer; start a new transfer")
			}
		}
		f.Close()
		if err != nil {
			return receipt, err
		}
		if !e.trusted(fp) {
			return receipt, errors.New("trust revoked")
		}
		deadline(c)
		if err = protocol.WriteJSON(c, protocol.FileEndFrame, checksum{hex.EncodeToString(h.Sum(nil))}); err != nil {
			return receipt, err
		}
		var ack checksum
		if err = readJSON(c, protocol.FileAckFrame, &ack); err != nil {
			return receipt, err
		}
		if ack.SHA256 != hex.EncodeToString(h.Sum(nil)) {
			return receipt, errors.New("receiver checksum acknowledgement differs")
		}
		deadline(c)
		first, err = readFrame(c)
		if err != nil {
			return receipt, err
		}
	}
	if first.Type == protocol.ErrorFrame {
		return receipt, fmt.Errorf("peer: %s", first.Data)
	}
	if first.Type != protocol.CompleteFrame {
		return receipt, errors.New("expected completion receipt")
	}
	err = json.Unmarshal(first.Data, &receipt)
	if err == nil && !receipt.Verified {
		err = errors.New("receiver did not verify transfer")
	}
	if err == nil && e.opts.Progress != nil {
		e.opts.Progress(Event{ID: o.ID, Peer: fp, Direction: "send", Status: "completed", Name: o.Name, Kind: o.Kind, Bytes: o.Total, Total: o.Total, Verified: receipt.Verified, Paths: receipt.Paths, Text: receipt.Text})
	}
	return receipt, err
}

// Source opens are confined to their parent directory, and the final entry must
// remain regular. Prepare rejects symlinks; before/after stat checks catch normal
// concurrent edits. Relay does not snapshot a concurrently modified source tree.
func openSource(source string) (*os.File, error) {
	r, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	i, err := r.Lstat(filepath.Base(source))
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() {
		return nil, errors.New("source is no longer a regular file")
	}
	return r.Open(filepath.Base(source))
}

func recordKey(fp, id string) string {
	v := sha256.Sum256([]byte(fp + ":" + id))
	return hex.EncodeToString(v[:16])
}
func (e *Engine) loadRecord(key string, o Offer) (*record, error) {
	b, err := os.ReadFile(filepath.Join(e.opts.StateDir, "incoming", key+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return &record{Offer: o, Names: map[string]string{}, Done: map[string]string{}, Skipped: map[string]bool{}, Checksums: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > protocol.MaxMetadata*8 {
		return nil, errors.New("stored manifest exceeds size limit")
	}
	var r record
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.OfferDigest != offerDigest(o) {
		return nil, errors.New("transfer ID reused with a different manifest")
	}
	r.Offer = o
	if r.Checksums == nil {
		r.Checksums = map[string]string{}
	}
	if r.Receipt != nil {
		r.Receipt.Text = o.Text
	}
	return &r, nil
}
func offerDigest(o Offer) string {
	b, _ := json.Marshal(o)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (e *Engine) saveRecord(key string, r *record) error {
	stored := *r
	stored.OfferDigest = offerDigest(r.Offer)
	stored.Offer.Text = "" // History retains no clipboard content.
	if r.Receipt != nil {
		receipt := *r.Receipt
		receipt.Text = ""
		stored.Receipt = &receipt
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	dir := filepath.Join(e.opts.StateDir, "incoming")
	f, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
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
	if err = os.Rename(name, filepath.Join(dir, key+".json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (e *Engine) receive(ctx context.Context, c *tls.Conn, fp string, o Offer, transferred *int64) (Receipt, error) {
	key := recordKey(fp, o.ID)
	rec, err := e.loadRecord(key, o)
	if err != nil {
		return Receipt{}, err
	}
	if rec.Receipt != nil {
		return *rec.Receipt, nil
	}
	if o.Kind == "text" || o.Kind == "url" {
		r := Receipt{Bytes: o.Total, Verified: true, Text: o.Text, Kind: o.Kind}
		rec.Receipt = &r
		return r, e.saveRecord(key, rec)
	}
	root, err := os.OpenRoot(e.opts.ReceiveDir)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	stage := ".relay-part-" + key
	if err = root.Mkdir(stage, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return Receipt{}, err
	}
	info, err := root.Lstat(stage)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Receipt{}, errors.New("unsafe partial transfer directory")
	}
	staging, err := root.OpenRoot(stage)
	if err != nil {
		return Receipt{}, err
	}
	defer staging.Close()
	if err = e.chooseNames(root, rec); err != nil {
		return Receipt{}, err
	}
	if err = e.saveRecord(key, rec); err != nil {
		return Receipt{}, err
	}
	result := Receipt{Kind: o.Kind, Verified: true}
	lastProgress := time.Time{}
	for index, entry := range o.Entries {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if !e.trusted(fp) {
			return result, errors.New("trust revoked")
		}
		top := strings.SplitN(entry.Path, "/", 2)[0]
		dest := rec.Names[top] + strings.TrimPrefix(entry.Path, top)
		if rec.Skipped[top] {
			if !entry.Directory {
				deadline(c)
				if err = protocol.WriteJSON(c, protocol.ResumeFrame, resume{Skip: true}); err != nil {
					return result, err
				}
				*transferred += entry.Size
			}
			continue
		}
		if entry.Directory {
			if err = mkdirSafe(root, dest); err != nil {
				return result, err
			}
			continue
		}
		partial := fmt.Sprintf("%06d.part", index)
		done := rec.Done[entry.Path] != ""
		// A crash after atomic publication but before the completion journal is
		// repaired using the checksum committed before publication.
		if !done && rec.Checksums[entry.Path] != "" {
			if noSymlinks(root, dest, false) == nil {
				candidate, ce := root.Open(dest)
				if ce == nil {
					ch := sha256.New()
					n, he := hashProgress(ctx, c, ch, candidate, make([]byte, protocol.ChunkSize))
					candidate.Close()
					if he == nil && n == entry.Size && hex.EncodeToString(ch.Sum(nil)) == rec.Checksums[entry.Path] {
						done = true
						rec.Done[entry.Path] = dest
						if err = e.saveRecord(key, rec); err != nil {
							return result, err
						}
					}
				}
			}
		}
		var f *os.File
		if done {
			dest = rec.Done[entry.Path]
			if err = noSymlinks(root, dest, false); err != nil {
				return result, err
			}
			f, err = root.Open(dest)
		} else {
			if i, se := staging.Lstat(partial); se == nil && !i.Mode().IsRegular() {
				return result, errors.New("unsafe partial file")
			}
			f, err = staging.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0600)
		}
		if err != nil {
			return result, err
		}
		sizeInfo, se := f.Stat()
		if se != nil {
			f.Close()
			return result, se
		}
		offset := sizeInfo.Size()
		if offset > entry.Size || done && offset != entry.Size {
			f.Close()
			return result, errors.New("partial or finalized size is invalid")
		}
		h := sha256.New()
		buf := make([]byte, protocol.ChunkSize)
		if _, err = hashProgress(ctx, c, h, f, buf); err != nil {
			f.Close()
			return result, err
		}
		deadline(c)
		if err = protocol.WriteJSON(c, protocol.ResumeFrame, resume{Offset: offset, SHA256: hex.EncodeToString(h.Sum(nil))}); err != nil {
			f.Close()
			return result, err
		}
		*transferred += offset
		remaining := entry.Size - offset
		for remaining > 0 {
			if err = ctx.Err(); err != nil {
				break
			}
			if !e.trusted(fp) {
				err = errors.New("trust revoked")
				break
			}
			deadline(c)
			var frame protocol.Frame
			frame, err = readFrame(c)
			if err != nil {
				break
			}
			if frame.Type != protocol.DataFrame || len(frame.Data) == 0 || int64(len(frame.Data)) > remaining {
				err = errors.New("invalid payload size or frame")
				break
			}
			if _, err = f.Write(frame.Data); err != nil {
				break
			}
			h.Write(frame.Data)
			remaining -= int64(len(frame.Data))
			*transferred += int64(len(frame.Data))
			if time.Since(lastProgress) > 100*time.Millisecond {
				e.event(o, fp, "receive", "transferring", *transferred, nil, entry.Path)
				lastProgress = time.Now()
			}
		}
		if err != nil {
			_ = f.Sync()
			f.Close()
			return result, err
		}
		e.event(o, fp, "receive", "verifying", *transferred, nil, entry.Path)
		deadline(c)
		var end checksum
		if err = readJSON(c, protocol.FileEndFrame, &end); err != nil {
			f.Close()
			return result, err
		}
		if !e.trusted(fp) {
			f.Close()
			return result, errors.New("trust revoked")
		}
		actual := hex.EncodeToString(h.Sum(nil))
		if end.SHA256 != actual {
			if !done {
				_ = f.Truncate(0)
				_ = f.Sync()
			}
			f.Close()
			return result, errors.New("SHA-256 integrity verification failed; partial reset")
		}
		if !done {
			if err = f.Sync(); err != nil {
				f.Close()
				return result, err
			}
			rec.Checksums[entry.Path] = actual
			if err = e.saveRecord(key, rec); err != nil {
				f.Close()
				return result, err
			}
			if err = noSymlinks(root, path.Dir(dest), true); err != nil {
				f.Close()
				return result, err
			}
			// Publication is atomic per file. Link never overwrites. Replace
			// atomically swaps a regular file; directories/symlinks are refused.
			if e.opts.Conflict == "replace" {
				if i, se := root.Lstat(dest); se == nil && !i.Mode().IsRegular() {
					f.Close()
					return result, errors.New("replace target must be a regular file")
				}
				err = root.Rename(path.Join(stage, partial), dest)
			} else {
				err = root.Link(path.Join(stage, partial), dest)
			}
			if err != nil {
				f.Close()
				return result, fmt.Errorf("finalize %q: %w (destination changed; resolve collision before retrying)", dest, err)
			}
			// Chmod the held descriptor, never an attacker-replaceable path.
			if err = f.Chmod(os.FileMode(entry.Mode) & 0777); err == nil {
				err = f.Sync()
			}
			f.Close()
			if err != nil {
				return result, err
			}
			if err = syncDirectory(root, path.Dir(dest)); err != nil {
				return result, err
			}
			rec.Done[entry.Path] = dest
			if err = e.saveRecord(key, rec); err != nil {
				return result, err
			}
			_ = staging.Remove(partial)
			_ = root.Chtimes(dest, time.Now(), time.Unix(0, entry.ModTime))
		} else {
			f.Close()
			_ = staging.Remove(partial)
		}

		result.Paths = append(result.Paths, filepath.Join(e.opts.ReceiveDir, filepath.FromSlash(dest)))
		result.Bytes += entry.Size
		deadline(c)
		if err = protocol.WriteJSON(c, protocol.FileAckFrame, checksum{actual}); err != nil {
			return result, err
		}
	}
	// Directory times are restored after writing their children.
	for i := len(o.Entries) - 1; i >= 0; i-- {
		entry := o.Entries[i]
		if entry.Directory {
			top := strings.SplitN(entry.Path, "/", 2)[0]
			if rec.Skipped[top] {
				continue
			}
			dest := rec.Names[top] + strings.TrimPrefix(entry.Path, top)
			_ = root.Chtimes(dest, time.Now(), time.Unix(0, entry.ModTime))
			if dir, de := root.Open(dest); de == nil {
				_ = dir.Chmod(os.FileMode(entry.Mode) & 0777)
				_ = dir.Sync()
				_ = dir.Close()
			}
		}
	}
	rec.Receipt = &result
	if err = e.saveRecord(key, rec); err != nil {
		return result, err
	}
	_ = root.Remove(stage)
	return result, nil
}

func (e *Engine) chooseNames(root *os.Root, r *record) error {
	for _, entry := range r.Offer.Entries {
		if strings.Contains(entry.Path, "/") {
			continue
		}
		if _, ok := r.Names[entry.Path]; ok {
			continue
		}
		name := entry.Path
		_, err := root.Lstat(name)
		if err == nil {
			switch e.opts.Conflict {
			case "cancel":
				return fmt.Errorf("destination %q already exists", name)
			case "skip":
				r.Names[name] = name
				r.Skipped[name] = true
				continue
			case "replace":
				if entry.Directory {
					return errors.New("directory replacement is not supported; choose rename")
				}
			case "rename":
				ext := path.Ext(name)
				stem := strings.TrimSuffix(name, ext)
				found := false
				for n := 1; n <= 10000; n++ {
					candidate := fmt.Sprintf("%s (%d)%s", stem, n, ext)
					if _, ce := root.Lstat(candidate); errors.Is(ce, os.ErrNotExist) {
						name = candidate
						found = true
						break
					}
				}
				if !found {
					return errors.New("too many destination name collisions")
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if entry.Directory {
			if err = root.Mkdir(name, 0700); err != nil {
				return fmt.Errorf("reserve directory: %w", err)
			}
		}
		r.Names[entry.Path] = name
	}
	return nil
}
func noSymlinks(root *os.Root, name string, directory bool) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("destination contains a symlink")
		}
		if i < len(parts)-1 || directory {
			if !info.IsDir() {
				return errors.New("destination parent is not a directory")
			}
		} else if !info.Mode().IsRegular() {
			return errors.New("destination is not a regular file")
		}
	}
	return nil
}
func mkdirSafe(root *os.Root, name string) error {
	if err := noSymlinks(root, path.Dir(name), true); err != nil {
		return err
	}
	err := root.Mkdir(name, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return noSymlinks(root, name, true)
}

func syncDirectory(root *os.Root, name string) error {
	d, e := root.Open(name)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Hashing a multi-gigabyte resume prefix can outlast the network idle timeout.
// Empty heartbeat frames keep both ends live without weakening idle limits;
// cancellation is checked between each bounded disk read.
func hashProgress(ctx context.Context, c net.Conn, w io.Writer, r io.Reader, buf []byte) (int64, error) {
	var total int64
	last := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, re := r.Read(buf)
		if n > 0 {
			written, we := w.Write(buf[:n])
			total += int64(written)
			if we != nil {
				return total, we
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if time.Since(last) > time.Second {
			deadline(c)
			if err := protocol.Write(c, protocol.HeartbeatFrame, nil); err != nil {
				return total, err
			}
			last = time.Now()
		}
		if re == io.EOF {
			return total, nil
		}
		if re != nil {
			return total, re
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}
func readFrame(c net.Conn) (protocol.Frame, error) {
	for {
		deadline(c)
		f, e := protocol.Read(c)
		if e != nil {
			return f, e
		}
		if f.Type != protocol.HeartbeatFrame {
			return f, nil
		}
		if len(f.Data) != 0 {
			return f, errors.New("invalid heartbeat")
		}
	}
}
func readJSON(c net.Conn, kind byte, value any) error {
	f, e := readFrame(c)
	if e != nil {
		return e
	}
	if f.Type == protocol.ErrorFrame {
		return fmt.Errorf("peer: %s", string(f.Data))
	}
	if f.Type != kind {
		return fmt.Errorf("unexpected frame %d (wanted %d)", f.Type, kind)
	}
	return json.Unmarshal(f.Data, value)
}
