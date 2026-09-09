package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// WhoIs asks the locally authenticated tailscaled instance to identify the
// actual remote TCP endpoint. A Tailscale-shaped IP alone is not authorization.
func WhoIs(ctx context.Context, remoteAddress string) (Device, error) {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil || !IsAddress(host) {
		return Device{}, errors.New("peer endpoint is not a direct Tailscale address")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	socket := os.Getenv("RELAY_TAILSCALE_SOCKET")
	if socket == "" {
		socket = "/var/run/tailscale/tailscaled.sock"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	endpoint := "http://local-tailscaled.sock/localapi/v0/whois?" + url.Values{"addr": {remoteAddress}, "proto": {"tcp"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Device{}, err
	}
	response, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return Device{}, fmt.Errorf("cannot verify peer with local Tailscale: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Device{}, fmt.Errorf("Tailscale did not authorize peer identity (HTTP %d)", response.StatusCode)
	}
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return Device{}, err
	}
	if len(data) > limit {
		return Device{}, errors.New("Tailscale identity response exceeds 2 MiB")
	}
	return parseWhoIs(data, host, time.Now())
}

func parseWhoIs(data []byte, host string, now time.Time) (Device, error) {
	var reply struct {
		Node *struct {
			StableID          string
			Name              string
			Addresses         []string
			MachineAuthorized *bool
			Expired           bool
			KeyExpiry         time.Time
			Hostinfo          struct {
				Hostname string
				OS       string
			}
		}
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return Device{}, fmt.Errorf("invalid Tailscale identity response: %w", err)
	}
	n := reply.Node
	if n == nil || n.StableID == "" || len(n.StableID) > 128 {
		return Device{}, errors.New("Tailscale identity is missing a stable node ID")
	}
	// Newer tailscaled versions omit the deprecated MachineAuthorized field.
	// An explicit denial is still authoritative when it is supplied.
	if n.Expired || (n.MachineAuthorized != nil && !*n.MachineAuthorized) || (!n.KeyExpiry.IsZero() && !now.Before(n.KeyExpiry)) {
		return Device{}, errors.New("Tailscale device authorization is expired or disabled")
	}
	remote, err := netip.ParseAddr(host)
	if err != nil || !IsAddress(host) {
		return Device{}, errors.New("invalid Tailscale source address")
	}
	d := Device{ID: n.StableID, Hostname: n.Hostinfo.Hostname, DNSName: strings.TrimSuffix(n.Name, "."), OS: n.Hostinfo.OS, Online: true}
	matched := false
	for _, raw := range n.Addresses {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() != prefix.Addr().BitLen() || !IsAddress(prefix.Addr().String()) {
			continue
		}
		d.Addresses = append(d.Addresses, prefix.Addr().String())
		if prefix.Addr() == remote {
			matched = true
		}
	}
	if !matched {
		return Device{}, errors.New("peer address is not assigned directly to this Tailscale node")
	}
	if d.Hostname == "" {
		d.Hostname = strings.Split(d.DNSName, ".")[0]
	}
	return d, nil
}
