// Package tailscale discovers devices through the locally authenticated CLI.
// It never needs an API key or makes calls to the Tailscale control plane.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Device struct {
	ID        string
	Hostname  string
	DNSName   string
	OS        string
	Addresses []string
	Online    bool
	LastSeen  time.Time
}

type Status struct {
	State string
	Self  Device
	Peers []Device
}

type wirePeer struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	TailscaleIPs []string
	Online       bool
	LastSeen     *time.Time
}

// IsAddress excludes exit-node and subnet routes, loopback, and public IPs.
func IsAddress(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	return netip.MustParsePrefix("100.64.0.0/10").Contains(a) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(a)
}

func Parse(data []byte) (Status, error) {
	var raw struct {
		BackendState string
		TailscaleIPs []string
		Self         *wirePeer
		Peer         map[string]wirePeer
	}
	if len(data) > 8<<20 {
		return Status{}, errors.New("Tailscale status exceeds 8 MiB")
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Status{}, fmt.Errorf("decode Tailscale status: %w", err)
	}
	if raw.BackendState == "" {
		return Status{}, errors.New("Tailscale status has no backend state")
	}
	convert := func(p wirePeer) Device {
		d := Device{ID: p.ID, Hostname: p.HostName, DNSName: strings.TrimSuffix(p.DNSName, "."), OS: p.OS, Online: p.Online}
		for _, ip := range p.TailscaleIPs {
			if IsAddress(ip) {
				d.Addresses = append(d.Addresses, ip)
			}
		}
		if p.LastSeen != nil {
			d.LastSeen = *p.LastSeen
		}
		if d.ID == "" && len(d.Addresses) > 0 {
			d.ID = d.Addresses[0]
		}
		return d
	}
	s := Status{State: raw.BackendState, Peers: []Device{}}
	if raw.Self != nil {
		s.Self = convert(*raw.Self)
	}
	if len(s.Self.Addresses) == 0 {
		for _, ip := range raw.TailscaleIPs {
			if IsAddress(ip) {
				s.Self.Addresses = append(s.Self.Addresses, ip)
			}
		}
	}
	for key, p := range raw.Peer {
		d := convert(p)
		if d.ID == "" {
			d.ID = key
		}
		if len(d.Addresses) > 0 {
			s.Peers = append(s.Peers, d)
		}
	}
	sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i].Hostname < s.Peers[j].Hostname })
	return s, nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("command output too large")
	}
	return b.Buffer.Write(p)
}

func Read(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tailscale", "status", "--json")
	out := &cappedBuffer{limit: 8 << 20}
	stderr := &cappedBuffer{limit: 16 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Status{}, errors.New("Tailscale is not installed; install Tailscale and run tailscale up")
		}
		return Status{}, fmt.Errorf("tailscale status failed: %w; check tailscaled and run tailscale status", err)
	}
	return Parse(out.Bytes())
}
