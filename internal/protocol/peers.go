package protocol

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"unicode"
	"unicode/utf8"
)

// ValidatePeerHints validates structure only. Consumers separately authorize
// transport and enforce their own Tailnet membership policy before probing.
func ValidatePeerHints(peers []PeerHint) error {
	if len(peers) > MaxPeerHints {
		return errors.New("peer directory exceeds 256 devices")
	}
	seen := make(map[string]bool, len(peers))
	for _, p := range peers {
		if len(p.Name) > 128 || len(p.OS) > 64 || len(p.Address) > 80 || !utf8.ValidString(p.Name) || !utf8.ValidString(p.OS) {
			return errors.New("invalid peer metadata")
		}
		for _, s := range []string{p.Name, p.OS} {
			for _, r := range s {
				if !unicode.IsPrint(r) {
					return errors.New("peer metadata contains control characters")
				}
			}
		}
		host, port, e := net.SplitHostPort(p.Address)
		if e != nil {
			return errors.New("peer endpoint must contain a numeric IP and port")
		}
		a, e := netip.ParseAddr(host)
		if e != nil || a.Zone() != "" {
			return errors.New("peer endpoint requires a numeric IP without a zone")
		}
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return errors.New("invalid peer port")
		}
		canonical := net.JoinHostPort(a.String(), port)
		if seen[canonical] {
			return errors.New("duplicate peer endpoint")
		}
		seen[canonical] = true
	}
	return nil
}
