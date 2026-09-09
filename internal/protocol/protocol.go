// Package protocol implements Relay's bounded binary framing protocol.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const Version = 1
const MaxMetadata = 2 * 1024 * 1024
const ChunkSize = 256 * 1024
const (
	HelloFrame byte = iota + 1
	OfferFrame
	DecisionFrame
	ResumeFrame
	DataFrame
	FileEndFrame
	FileAckFrame
	CompleteFrame
	ErrorFrame
	HeartbeatFrame
	PeersFrame
)

const PeerDirectoryCapability = "peer-directory-v1"
const MaxPeerHints = 256

type PeerHint struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	OS      string `json:"os"`
}

type PeerDirectory struct {
	Peers []PeerHint `json:"peers"`
}

type Hello struct {
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Version      string   `json:"version"`
	Protocol     int      `json:"protocol"`
	Capabilities []string `json:"capabilities"`
	Fingerprint  string   `json:"fingerprint"`
}

type Frame struct {
	Type byte
	Data []byte
}

// Each frame is magic 'RL', protocol version, type, uint32 big-endian length,
// followed by exactly length bytes. Payload data is never JSON or base64 encoded.
func Write(w io.Writer, kind byte, data []byte) error {
	limit := MaxMetadata
	if kind == DataFrame {
		limit = ChunkSize
	}
	if len(data) > limit {
		return errors.New("frame too large")
	}
	header := [8]byte{'R', 'L', Version, kind}
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	if err := writeFull(w, header[:]); err != nil {
		return err
	}
	return writeFull(w, data)
}
func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func Read(r io.Reader) (Frame, error) {
	var h [8]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return Frame{}, e
	}
	if h[0] != 'R' || h[1] != 'L' || h[2] != Version {
		return Frame{}, errors.New("unsupported Relay protocol")
	}
	if h[3] < HelloFrame || h[3] > PeersFrame {
		return Frame{}, errors.New("unknown frame type")
	}
	n := binary.BigEndian.Uint32(h[4:])
	limit := uint32(MaxMetadata)
	if h[3] == DataFrame {
		limit = ChunkSize
	}
	if n > limit {
		return Frame{}, errors.New("frame exceeds size limit")
	}
	b := make([]byte, int(n))
	_, e := io.ReadFull(r, b)
	return Frame{h[3], b}, e
}
func WriteJSON(w io.Writer, kind byte, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return Write(w, kind, b)
}
func ReadJSON(r io.Reader, kind byte, value any) error {
	f, e := Read(r)
	if e != nil {
		return e
	}
	if f.Type == ErrorFrame {
		return fmt.Errorf("peer: %s", string(f.Data))
	}
	if f.Type != kind {
		return fmt.Errorf("unexpected frame %d (wanted %d)", f.Type, kind)
	}
	if e = json.Unmarshal(f.Data, value); e != nil {
		return fmt.Errorf("invalid metadata: %w", e)
	}
	return nil
}
