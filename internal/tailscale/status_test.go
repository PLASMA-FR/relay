package tailscale

import "testing"

func TestParse(t *testing.T) {
	s, err := Parse([]byte(`{"BackendState":"Running","Self":{"ID":"self","HostName":"desktop","TailscaleIPs":["100.64.0.1","192.168.0.1"]},"Peer":{"key":{"ID":"p1","HostName":"laptop","DNSName":"laptop.tail.ts.net.","Online":true,"TailscaleIPs":["100.70.0.3"]},"evil":{"HostName":"public","TailscaleIPs":["8.8.8.8"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "Running" || len(s.Self.Addresses) != 1 || len(s.Peers) != 1 || s.Peers[0].DNSName != "laptop.tail.ts.net" {
		t.Fatalf("unexpected %+v", s)
	}
}
func TestAddresses(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "100.63.255.255", "100.128.0.0", "8.8.8.8", "fd7b::1", "invalid"} {
		if IsAddress(s) {
			t.Errorf("accepted %s", s)
		}
	}
	for _, s := range []string{"100.64.0.1", "100.127.255.255", "fd7a:115c:a1e0::1"} {
		if !IsAddress(s) {
			t.Errorf("rejected %s", s)
		}
	}
}
func TestMalformed(t *testing.T) {
	for _, s := range []string{"{}", "null", "{broken"} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
