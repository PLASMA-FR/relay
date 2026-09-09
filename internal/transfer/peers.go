package transfer

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/PLASMA-FR/relay/internal/protocol"
)

// ExchangePeers shares discovery hints over pinned mutual TLS. Hints themselves
// cannot authorize transfers: every consumer verifies each candidate endpoint.
func (e *Engine) ExchangePeers(ctx context.Context, address, expectedFingerprint string, peers []protocol.PeerHint) ([]protocol.PeerHint, error) {
	pin, err := hex.DecodeString(expectedFingerprint)
	if err != nil || len(pin) != 32 {
		return nil, errors.New("complete peer fingerprint is required")
	}
	if err = protocol.ValidatePeerHints(peers); err != nil {
		return nil, err
	}
	c, err := dial(ctx, address, e.opts.Identity)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	if peerFP(c) != expectedFingerprint {
		return nil, errors.New("peer identity changed")
	}
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if err = protocol.WriteJSON(c, protocol.PeersFrame, protocol.PeerDirectory{Peers: peers}); err != nil {
		return nil, err
	}
	var response protocol.PeerDirectory
	if err = protocol.ReadJSON(c, protocol.PeersFrame, &response); err != nil {
		return nil, err
	}
	if err = protocol.ValidatePeerHints(response.Peers); err != nil {
		return nil, err
	}
	return response.Peers, nil
}
