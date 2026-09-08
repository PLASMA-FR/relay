package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PLASMA-FR/relay/internal/invite"
)

func TestMobileInvitationUsesDaemonIdentity(t *testing.T) {
	cliEnvironment(t)
	d := fakeDaemon(t)
	code, out, errOut := runCLI(t, "", "--no-start", "--json", "mobile", "invite")
	if code != 0 {
		t.Fatal(code, errOut)
	}
	var r struct {
		Invitation string `json:"invitation"`
	}
	if e := json.Unmarshal([]byte(out), &r); e != nil {
		t.Fatal(e)
	}
	i, e := invite.Parse(r.Invitation)
	if e != nil {
		t.Fatal(e)
	}
	if i.Fingerprint != d.status.Fingerprint || i.Address != "100.64.0.1:7331" {
		t.Fatal(i)
	}
	d.mu.Lock()
	n := len(d.actions)
	d.mu.Unlock()
	if n != 0 {
		t.Fatal("invitation changed trust")
	}
	code, out, errOut = runCLI(t, "", "--no-start", "mobile", "invite", "--qr")
	if code != 0 || !strings.Contains(out, "relay://pair") {
		t.Fatal(code, errOut)
	}
}
func TestMobilePairVerifiesBeforeTrust(t *testing.T) {
	cliEnvironment(t)
	d := fakeDaemon(t)
	d.status.Peers[0].Address = "100.64.0.2"
	bad, _ := invite.New("phone", "100.64.0.2:7331", strings.Repeat("c", 64))
	code, _, _ := runCLI(t, "", "--no-start", "mobile", "pair", bad.URL())
	if code != 3 {
		t.Fatal(code)
	}
	d.mu.Lock()
	for _, a := range d.actions {
		if a.Action == "trust" {
			t.Fatal("trusted mismatched invitation")
		}
	}
	d.actions = nil
	d.mu.Unlock()
	good, _ := invite.New("phone", "100.64.0.2:7331", strings.Repeat("b", 64))
	code, _, errOut := runCLI(t, "", "--no-start", "mobile", "pair", good.URL())
	if code != 0 {
		t.Fatal(code, errOut)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.actions) != 2 || d.actions[0].Action != "pair" || d.actions[1].Action != "trust" {
		t.Fatal(d.actions)
	}
}
