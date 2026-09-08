// Package invite encodes public device identity invitations. An invitation is
// an address and expected public-key fingerprint, never an authorization token.
package invite

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Invitation struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

// New validates all fields before an invitation can be displayed or dialed.
func New(name, address, fingerprint string) (Invitation, error) {
	i := Invitation{Version: 1, Name: name, Address: address, Fingerprint: strings.ToLower(fingerprint)}
	if name == "" || len(name) > 128 || !utf8.ValidString(name) {
		return i, errors.New("device name must contain 1–128 printable UTF-8 bytes")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return i, errors.New("device name contains control characters")
		}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return i, errors.New("invitation address must include a numeric Tailscale IP and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !IsTailscaleAddress(ip.String()) {
		return i, errors.New("invitation must use a Tailscale address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return i, errors.New("invalid invitation port")
	}
	i.Address = net.JoinHostPort(ip.String(), port)
	b, err := hex.DecodeString(i.Fingerprint)
	if err != nil || len(b) != 32 {
		return i, errors.New("invitation requires a complete SHA-256 fingerprint")
	}
	return i, nil
}

func IsTailscaleAddress(s string) bool {
	ip, e := netip.ParseAddr(s)
	if e != nil || ip.Zone() != "" {
		return false
	}
	return netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)
}

func (i Invitation) URL() string {
	q := url.Values{"v": {strconv.Itoa(i.Version)}, "name": {i.Name}, "address": {i.Address}, "fingerprint": {i.Fingerprint}}
	return (&url.URL{Scheme: "relay", Host: "pair", RawQuery: q.Encode()}).String()
}

func Parse(raw string) (Invitation, error) {
	if len(raw) > 2048 {
		return Invitation{}, errors.New("invitation is too long")
	}
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil {
		return Invitation{}, errors.New("invalid invitation link")
	}
	if !strings.EqualFold(u.Scheme, "relay") || u.Host != "pair" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Path != "" || u.RawPath != "" {
		return Invitation{}, errors.New("expected a relay://pair invitation link")
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q) != 4 {
		return Invitation{}, errors.New("invalid invitation fields")
	}
	for _, key := range []string{"v", "name", "address", "fingerprint"} {
		if len(q[key]) != 1 {
			return Invitation{}, fmt.Errorf("invitation requires exactly one %s", key)
		}
	}
	if q.Get("v") != "1" {
		return Invitation{}, errors.New("unsupported invitation version; update Relay on both devices")
	}
	return New(q.Get("name"), q.Get("address"), q.Get("fingerprint"))
}
