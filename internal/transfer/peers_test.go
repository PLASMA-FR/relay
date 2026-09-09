package transfer

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PLASMA-FR/relay/internal/protocol"
)

func TestPeerExchangeRequiresConnectionAuthorization(t *testing.T) {
	h := setup(t, nil)
	var calls atomic.Int32
	opts := h.receiver.opts
	opts.AuthorizePeer = func(_ context.Context, addr, fp string) error {
		host, _, e := net.SplitHostPort(addr)
		if e != nil || host != "127.0.0.1" || fp != h.sender.opts.Identity.Fingerprint {
			return errors.New("wrong actual connection identity")
		}
		calls.Add(1)
		return nil
	}
	opts.PeerDirectory = func(_ context.Context, _, _ string, hints []protocol.PeerHint) ([]protocol.PeerHint, error) {
		return hints, nil
	}
	receiver, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	h.mu.Lock()
	h.receiver = receiver
	h.mu.Unlock()
	hello, e := h.sender.Probe(context.Background(), h.addr)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, cap := range hello.Capabilities {
		if cap == protocol.PeerDirectoryCapability {
			found = true
		}
	}
	if !found || calls.Load() != 0 {
		t.Fatal("Hello must advertise capability without invoking authorization/reverse probes")
	}
	hints := []protocol.PeerHint{{Name: "phone", Address: "100.64.0.2:7331"}}
	got, e := h.sender.ExchangePeers(context.Background(), h.addr, h.receiverID.Fingerprint, hints)
	if e != nil || len(got) != 1 || got[0] != hints[0] || calls.Load() != 1 {
		t.Fatal(got, e, calls.Load())
	}
	if _, e = h.sender.ExchangePeers(context.Background(), h.addr, strings.Repeat("0", 64), hints); e == nil {
		t.Fatal("accepted wrong TLS pin")
	}
}

func TestPeerAuthorizationRejectsBeforeDirectoryHandler(t *testing.T) {
	h := setup(t, nil)
	opts := h.receiver.opts
	var called atomic.Bool
	opts.AuthorizePeer = func(context.Context, string, string) error { return errors.New("not a Tailnet member") }
	opts.PeerDirectory = func(context.Context, string, string, []protocol.PeerHint) ([]protocol.PeerHint, error) {
		called.Store(true)
		return nil, nil
	}
	receiver, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	h.mu.Lock()
	h.receiver = receiver
	h.mu.Unlock()
	if _, e = h.sender.ExchangePeers(context.Background(), h.addr, h.receiverID.Fingerprint, nil); e == nil {
		t.Fatal("allowed unverified member")
	}
	if called.Load() {
		t.Fatal("directory disclosed before authorization")
	}
}
