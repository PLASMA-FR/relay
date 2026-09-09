package main

import "testing"

func TestTailnetUnblockDoesNotRequestFingerprint(t *testing.T) {
	cliEnvironment(t)
	d := fakeDaemon(t)
	d.status.TrustMode = "tailnet"
	// Fake daemon expects its observed pin if provided; no pin is needed for a
	// Tailnet policy unblock. Treat action as successful for this transport test.
	d.status.Peers[0].Fingerprint = ""
	code, _, errOut := runCLI(t, "", "--no-start", "trust", "laptop")
	if code != 0 {
		t.Fatal(code, errOut)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.actions) != 1 || d.actions[0].Action != "trust" || d.actions[0].Fingerprint != "" {
		t.Fatal(d.actions)
	}
}
