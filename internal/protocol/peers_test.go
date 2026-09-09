package protocol

import (
	"strings"
	"testing"
)

func TestPeerDirectoryBoundsAndShape(t *testing.T) {
	good := PeerHint{Name: "laptop", Address: "100.64.0.2:7331", OS: "linux"}
	if e := ValidatePeerHints([]PeerHint{good, {Name: "phone", Address: "[fd7a:115c:a1e0::2]:7331"}}); e != nil {
		t.Fatal(e)
	}
	for _, peers := range [][]PeerHint{
		make([]PeerHint, MaxPeerHints+1), {good, good}, {{Address: "example.com:7331"}}, {{Address: "100.64.0.2:0"}}, {{Address: "100.64.0.2:7331", Name: "hello\x1b"}}, {{Address: "100.64.0.2:7331", OS: strings.Repeat("x", 65)}}, {{Address: "[fd7a:115c:a1e0::1%eth0]:7331"}},
	} {
		if e := ValidatePeerHints(peers); e == nil {
			t.Fatal("accepted invalid hints", peers)
		}
	}
}
