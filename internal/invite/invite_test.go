package invite

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, a := range []string{"100.64.0.5:7331", "[fd7a:115c:a1e0::5]:7331"} {
		want, e := New("My laptop 世界", a, strings.Repeat("ab", 32))
		if e != nil {
			t.Fatal(e)
		}
		got, e := Parse(want.URL())
		if e != nil || got != want {
			t.Fatalf("%+v %v", got, e)
		}
	}
}
func TestRejectMalformedInvites(t *testing.T) {
	good, _ := New("laptop", "100.64.0.5:7331", strings.Repeat("ab", 32))
	raw := good.URL()
	tests := []string{raw + "&v=1", raw + "&extra=x", raw + "#fragment", strings.Replace(raw, "v=1", "v=2", 1), strings.Replace(raw, "relay://pair", "https://pair", 1), strings.Replace(raw, "relay://pair", "relay://user@pair", 1), strings.Replace(raw, "relay://pair", "relay://pair/path", 1), strings.Replace(raw, "100.64.0.5", "127.0.0.1", 1), strings.Replace(raw, "100.64.0.5", "192.168.1.1", 1), strings.Replace(raw, "100.64.0.5", "8.8.8.8", 1), strings.Replace(raw, "name=laptop", "name=%1b%5b2J", 1), strings.Replace(raw, "fingerprint=", "fingerprint=x", 1), strings.Repeat("x", 2049)}
	for _, s := range tests {
		if _, e := Parse(s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestRejectUnsafeNewValues(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7331", "[::]:7331", "100.64.0.5:0", "100.64.0.5:65536", "100.64.0.5:+7331", "desktop:7331", "100.64.0.5", "[fd7a:115c:a1e0::5%eth0]:7331"} {
		if _, e := New("peer", address, strings.Repeat("a", 64)); e == nil {
			t.Fatal(address)
		}
	}
}
