// Package transfer streams authenticated, resumable transfers over Relay TLS.
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/PLASMA-FR/relay/internal/identity"
	"github.com/PLASMA-FR/relay/internal/protocol"
)

const MaxEntries = 10000
const MaxText = 1024 * 1024

type Entry struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	ModTime   int64  `json:"mod_time"`
	Directory bool   `json:"directory"`
}
type Offer struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Text    string  `json:"text,omitempty"`
	Entries []Entry `json:"entries"`
	Total   int64   `json:"total"`
}
type Prepared struct {
	Offer   Offer
	Sources map[string]string
}
type Event struct {
	ID, Peer, Direction, Status, Name, Kind, CurrentFile, Error string
	Bytes, Total                                                int64
	Verified                                                    bool
	Paths                                                       []string
	Text                                                        string
}
type Receipt struct {
	Paths    []string `json:"paths"`
	Bytes    int64    `json:"bytes"`
	Verified bool     `json:"verified"`
	Text     string   `json:"text,omitempty"`
	Kind     string   `json:"kind"`
}
type Options struct {
	Identity                       *identity.Identity
	StateDir, ReceiveDir, Conflict string
	MaxBytes                       int64
	Hello                          protocol.Hello
	Trusted                        func(string) bool
	Decide                         func(context.Context, string, Offer) error
	Progress                       func(Event)
}

func NewID() string {
	var b [16]byte
	_, e := rand.Read(b[:])
	if e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

func Prepare(ctx context.Context, id string, paths []string, kind, text, name string) (*Prepared, error) {
	if id == "" {
		id = NewID()
	}
	if kind == "" {
		kind = "file"
	}
	p := &Prepared{Offer: Offer{ID: id, Kind: kind, Name: name, Text: text}, Sources: map[string]string{}}
	if kind == "text" || kind == "url" {
		p.Offer.Total = int64(len(text))
		if p.Offer.Name == "" {
			p.Offer.Name = kind
		}
		return p, Validate(p.Offer, 1<<40)
	}
	if len(paths) == 0 {
		return nil, errors.New("select at least one file or directory")
	}
	seen := map[string]bool{}
	for _, source := range paths {
		abs, e := filepath.Abs(source)
		if e != nil {
			return nil, e
		}
		base := filepath.Base(abs)
		if base == "." || base == string(filepath.Separator) {
			return nil, errors.New("cannot send filesystem root")
		}
		if seen[base] {
			return nil, fmt.Errorf("two selected paths have the same name %q", base)
		}
		seen[base] = true
		e = filepath.WalkDir(abs, func(local string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e := ctx.Err(); e != nil {
				return e
			}
			if len(p.Offer.Entries) >= MaxEntries {
				return errors.New("transfer has too many entries (maximum 10000)")
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlinks are not supported in V1: %s", local)
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() && !info.IsDir() {
				return fmt.Errorf("special files are not supported: %s", local)
			}
			rel, e := filepath.Rel(abs, local)
			if e != nil {
				return e
			}
			remote := base
			if rel != "." {
				remote = path.Join(base, filepath.ToSlash(rel))
			}
			entry := Entry{Path: remote, Mode: uint32(info.Mode().Perm() & 0777), ModTime: info.ModTime().UnixNano(), Directory: info.IsDir()}
			if !entry.Directory {
				entry.Size = info.Size()
				p.Offer.Total += entry.Size
				p.Sources[remote] = local
			}
			p.Offer.Entries = append(p.Offer.Entries, entry)
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	if p.Offer.Name == "" {
		p.Offer.Name = p.Offer.Entries[0].Path
		if len(paths) > 1 {
			p.Offer.Name = fmt.Sprintf("%s + %d more", p.Offer.Name, len(paths)-1)
		}
	}
	return p, Validate(p.Offer, 1<<50)
}

func safePath(s string) bool {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) || path.IsAbs(s) || path.Clean(s) != s || s == "." || strings.ContainsAny(s, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." || part == "." || part == "" || len(part) > 255 || strings.HasPrefix(part, ".relay-") {
			return false
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return false
			}
		}
	}
	return true
}
func Validate(o Offer, max int64) error {
	if len(o.ID) != 32 {
		return errors.New("invalid transfer ID")
	}
	if _, e := hex.DecodeString(o.ID); e != nil {
		return errors.New("invalid transfer ID")
	}
	if o.Kind != "file" && o.Kind != "directory" && o.Kind != "text" && o.Kind != "url" {
		return errors.New("unsupported content kind")
	}
	if len(o.Name) > 4096 || !utf8.ValidString(o.Name) {
		return errors.New("invalid display name")
	}
	if o.Total < 0 || o.Total > max {
		return errors.New("transfer exceeds configured receive limit")
	}
	if o.Kind == "text" || o.Kind == "url" {
		if len(o.Text) > MaxText || len(o.Entries) != 0 || o.Total != int64(len(o.Text)) {
			return errors.New("invalid text offer")
		}
		return nil
	}
	if len(o.Text) != 0 || len(o.Entries) == 0 || len(o.Entries) > MaxEntries {
		return errors.New("invalid manifest entry count")
	}
	known := map[string]bool{}
	var total int64
	for _, e := range o.Entries {
		if !safePath(e.Path) {
			return fmt.Errorf("unsafe path %q", e.Path)
		}
		if _, exists := known[e.Path]; exists {
			return errors.New("duplicate manifest path")
		}
		if e.Mode > 0777 || e.Size < 0 || e.Size > max-total {
			return errors.New("invalid manifest size or mode")
		}
		if e.Directory && e.Size != 0 {
			return errors.New("directory with content size")
		}
		parent := path.Dir(e.Path)
		if parent != "." && !known[parent] {
			return errors.New("manifest parent directory missing or not a directory")
		}
		known[e.Path] = e.Directory
		total += e.Size
	}
	if total != o.Total {
		return errors.New("manifest total does not match sizes")
	}
	return nil
}
