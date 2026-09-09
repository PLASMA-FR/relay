package tailscale

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestWhoIsValidatesDirectNodeIdentity(t *testing.T) {
	now := time.Now()
	node := map[string]any{"StableID": "node-1", "Name": "laptop.example.ts.net.", "Addresses": []string{"100.64.0.2/32", "fd7a:115c:a1e0::2/128"}, "KeyExpiry": now.Add(time.Hour), "Hostinfo": map[string]string{"Hostname": "laptop", "OS": "linux"}}
	raw := func(n map[string]any) []byte { b, _ := json.Marshal(map[string]any{"Node": n}); return b }
	got, e := parseWhoIs(raw(node), "100.64.0.2", now)
	if e != nil || got.ID != "node-1" || got.Hostname != "laptop" {
		t.Fatal(got, e)
	}
	for _, mutate := range []func(map[string]any){
		func(n map[string]any) { n["Expired"] = true }, func(n map[string]any) { n["MachineAuthorized"] = false }, func(n map[string]any) { n["KeyExpiry"] = now.Add(-time.Second) }, func(n map[string]any) { n["StableID"] = "" }, func(n map[string]any) { n["Addresses"] = []string{"100.64.0.0/10"} }, func(n map[string]any) { n["Addresses"] = []string{"100.64.0.3/32"} },
	} {
		copy := map[string]any{}
		for k, v := range node {
			copy[k] = v
		}
		mutate(copy)
		if _, e := parseWhoIs(raw(copy), "100.64.0.2", now); e == nil {
			t.Fatal(copy)
		}
	}
}

func TestWhoIsUsesActualEndpointOverPrivateLocalAPI(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "whois.sock")
	ln, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/whois" || r.URL.Query().Get("addr") != "100.64.0.2:32123" || r.URL.Query().Get("proto") != "tcp" {
			t.Error(r.URL)
			http.Error(w, "wrong endpoint", 400)
			return
		}
		_, _ = w.Write([]byte(`{"Node":{"StableID":"node-2","Addresses":["100.64.0.2/32"]}}`))
	})}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	t.Setenv("RELAY_TAILSCALE_SOCKET", socket)
	got, e := WhoIs(context.Background(), "100.64.0.2:32123")
	if e != nil || got.ID != "node-2" {
		t.Fatal(got, e)
	}
	if _, e = WhoIs(context.Background(), "192.168.1.2:123"); e == nil {
		t.Fatal("accepted non-Tailnet source")
	}
}
