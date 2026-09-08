// Package model defines the stable local IPC data model. Remote protocol types
// live separately in protocol; the UI never opens peer connections.
package model

import "time"

const ProtocolVersion = 1

var Version = "0.2.0"

type Peer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Hostname     string    `json:"hostname"`
	Address      string    `json:"address"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch,omitempty"`
	Online       bool      `json:"online"`
	Relay        bool      `json:"relay"`
	Version      string    `json:"version,omitempty"`
	Protocol     int       `json:"protocol"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
	Trusted      bool      `json:"trusted"`
	LastSeen     time.Time `json:"last_seen"`
	LatencyMS    int64     `json:"latency_ms,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type Transfer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Peer         string    `json:"peer"`
	PeerID       string    `json:"peer_id"`
	Direction    string    `json:"direction"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Bytes        int64     `json:"bytes"`
	Total        int64     `json:"total"`
	Speed        float64   `json:"speed"`
	AverageSpeed float64   `json:"average_speed"`
	ETASeconds   float64   `json:"eta_seconds"`
	Started      time.Time `json:"started"`
	Updated      time.Time `json:"updated"`
	Verified     bool      `json:"verified"`
	Retry        int       `json:"retry"`
	Error        string    `json:"error,omitempty"`
	Paths        []string  `json:"paths,omitempty"`
	Destination  string    `json:"destination,omitempty"`
}

func (t Transfer) Terminal() bool {
	return t.Status == "completed" || t.Status == "cancelled" || t.Status == "rejected" || t.Status == "failed"
}

type Snapshot struct {
	Version          string     `json:"version"`
	Name             string     `json:"name"`
	Fingerprint      string     `json:"fingerprint"`
	Tailscale        string     `json:"tailscale"`
	Address          string     `json:"address"`
	ReceiveDirectory string     `json:"receive_directory"`
	ClipboardBackend string     `json:"clipboard_backend"`
	Peers            []Peer     `json:"peers"`
	Transfers        []Transfer `json:"transfers"`
	History          []Transfer `json:"history"`
	Notices          []string   `json:"notices,omitempty"`
}

type SendRequest struct {
	Peer  string   `json:"peer"`
	Paths []string `json:"paths,omitempty"`
	Kind  string   `json:"kind,omitempty"`
	Text  string   `json:"text,omitempty"`
	Name  string   `json:"name,omitempty"`
	Spool bool     `json:"spool,omitempty"`
}

type Action struct {
	Action      string `json:"action"`
	ID          string `json:"id,omitempty"`
	Peer        string `json:"peer,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Text        string `json:"text,omitempty"`
}

type Result struct {
	OK      bool   `json:"ok"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
	Text    string `json:"text,omitempty"`
	Peer    *Peer  `json:"peer,omitempty"`
}
