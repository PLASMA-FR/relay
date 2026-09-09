package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/PLASMA-FR/relay/internal/invite"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

func (a *app) mobileCommand() *cobra.Command {
	root := &cobra.Command{Use: "mobile", Short: "Pair an Android companion with this Relay device"}
	var qr bool
	c := &cobra.Command{Use: "invite", Short: "Print a public pairing link for the Android app", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, e := a.status(cmd.Context())
		if e != nil {
			return e
		}
		address := s.Address
		if net.ParseIP(address) != nil {
			address = net.JoinHostPort(address, strconv.Itoa(a.cfg.Network.Port))
		}
		i, e := invite.New(s.Name, address, s.Fingerprint)
		if e != nil {
			return fmt.Errorf("cannot create an invitation: %w; connect Tailscale and run relay doctor", e)
		}
		if a.json {
			return a.printJSON(map[string]any{"invitation": i.URL(), "device": i, "protocol": model.ProtocolVersion})
		}
		fmt.Fprintln(a.out, i.URL())
		if qr {
			q, e := qrcode.New(i.URL(), qrcode.Medium)
			if e != nil {
				return e
			}
			fmt.Fprintln(a.out, q.ToSmallString(false))
		}
		if s.TrustMode == "tailnet" {
			fmt.Fprintln(a.out, "\nPairing is not required. Relay devices appear automatically through Tailscale.")
			fmt.Fprintln(a.out, "This optional connection link can help a phone find its first device.")
			return nil
		}
		fmt.Fprintln(a.out, "\nIn Relay on Android: Add device → scan or paste this link, then confirm trust.")
		fmt.Fprintln(a.out, "Enable receiving on the phone. Pair its invitation here with: relay mobile pair 'LINK'")
		return nil
	}}
	c.Flags().BoolVar(&qr, "qr", false, "also render a terminal QR code to scan with the phone")
	root.AddCommand(c)
	root.AddCommand(&cobra.Command{Use: "pair INVITATION", Short: "Verify and trust a phone invitation you obtained from your phone", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		i, e := invite.Parse(args[0])
		if e != nil {
			return &exitError{2, e}
		}
		s, e := a.status(cmd.Context())
		if e != nil {
			return e
		}
		host, _, _ := net.SplitHostPort(i.Address)
		var peer model.Peer
		found := false
		for _, p := range s.Peers {
			address := p.Address
			if h, _, err := net.SplitHostPort(address); err == nil {
				address = h
			}
			if address == host {
				peer = p
				found = true
				break
			}
		}
		if !found {
			return errors.New("phone is not in the current Tailnet peer list; connect Tailscale on both devices, enable receiving on the phone, and refresh Relay")
		}
		// Existing daemon pinning remains the only trust authority. The invitation
		// supplies the independently obtained expected public-key fingerprint.
		r, e := a.client.Action(cmd.Context(), model.Action{Action: "pair", Peer: peer.ID})
		if e != nil {
			return e
		}
		if r.Peer == nil || r.Peer.Fingerprint != i.Fingerprint {
			return &exitError{3, errors.New("phone fingerprint does not match the invitation; no trust was granted")}
		}
		r, e = a.client.Action(cmd.Context(), model.Action{Action: "trust", Peer: peer.ID, Fingerprint: i.Fingerprint})
		if e != nil {
			return e
		}
		return a.result(r)
	}})
	return root
}
