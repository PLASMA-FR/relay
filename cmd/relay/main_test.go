package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/model"
)

func cliEnvironment(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "relay-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("RELAY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("RELAY_STATE_DIR", filepath.Join(dir, "relay-state"))
	t.Setenv("RELAY_SOCKET", filepath.Join(dir, "relay.sock"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	cfg := "name = 'cli-test'\n[receive]\ndirectory = '" + filepath.Join(dir, "inbox") + "'\n"
	if err = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runCLI(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	code := execute(ctx, args, strings.NewReader(input), &out, &errOut)
	return code, out.String(), errOut.String()
}

type scriptedDaemon struct {
	mu           sync.Mutex
	sends        []model.SendRequest
	actions      []model.Action
	status       model.Snapshot
	outcome      string
	trustFailure bool
	sendFailure  bool
}

func fakeDaemon(t *testing.T) *scriptedDaemon {
	t.Helper()
	server := &scriptedDaemon{status: model.Snapshot{Version: model.Version, Name: "test desktop", Tailscale: "Running", Address: "100.64.0.1", Fingerprint: strings.Repeat("a", 64), Peers: []model.Peer{{ID: "peer", Name: "laptop", Online: true, Relay: true, Trusted: true, Fingerprint: strings.Repeat("b", 64)}}}, outcome: "completed"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		server.mu.Lock()
		defer server.mu.Unlock()
		json.NewEncoder(w).Encode(server.status)
	})
	mux.HandleFunc("POST /v1/send", func(w http.ResponseWriter, r *http.Request) {
		var req model.SendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		server.mu.Lock()
		server.sends = append(server.sends, req)
		fail := server.sendFailure
		server.mu.Unlock()
		if fail {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "device is not trusted"})
			return
		}
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(model.Result{OK: true, ID: "queued-id", Message: "queued"})
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		server.mu.Lock()
		s := server.status
		outcome := server.outcome
		server.mu.Unlock()
		s.Transfers = []model.Transfer{{ID: "queued-id", Name: "notes.txt", Status: "transferring", Bytes: 12, Total: 24}}
		json.NewEncoder(w).Encode(s)
		w.(http.Flusher).Flush()
		s.Transfers = nil
		s.History = []model.Transfer{{ID: "queued-id", Name: "notes.txt", Status: outcome, Bytes: 24, Total: 24, Verified: outcome == "completed", Error: map[string]string{"rejected": "receiver declined", "failed": "checksum mismatch"}[outcome]}}
		json.NewEncoder(w).Encode(s)
		w.(http.Flusher).Flush()
	})
	mux.HandleFunc("POST /v1/action", func(w http.ResponseWriter, r *http.Request) {
		var a model.Action
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		server.mu.Lock()
		server.actions = append(server.actions, a)
		fail := server.trustFailure
		p := server.status.Peers[0]
		server.mu.Unlock()
		if a.Action == "trust" && (fail || a.Fingerprint != p.Fingerprint) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "fingerprints do not match; no trust was granted"})
			return
		}
		json.NewEncoder(w).Encode(model.Result{OK: true, Message: "done", Peer: &p})
	})
	ln, err := net.Listen("unix", os.Getenv("RELAY_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() { httpServer.Serve(ln); close(done) }()
	t.Cleanup(func() { httpServer.Close(); <-done })
	return server
}

func TestCLIHelpVersionAndCompletionWithoutConfig(t *testing.T) {
	cliEnvironment(t)
	if err := os.WriteFile(os.Getenv("RELAY_CONFIG"), []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args     []string
		contains string
	}{{[]string{"--help"}, "terminal-native"}, {[]string{"send", "--help"}, "--stdin"}, {[]string{"version", "--json"}, `"protocol": 1`}, {[]string{"completion", "bash"}, "__start_relay"}} {
		code, out, stderr := runCLI(t, "", tc.args...)
		if code != 0 || !strings.Contains(out, tc.contains) || stderr != "" {
			t.Errorf("%v: exit=%d out=%q err=%q", tc.args, code, out, stderr)
		}
	}
}

func TestCLIArgumentAndConfigErrors(t *testing.T) {
	cliEnvironment(t)
	fakeDaemon(t)
	for _, args := range [][]string{{"send"}, {"send", "laptop"}, {"send", "laptop", "file", "--stdin", "pipe.txt"}, {"open", "laptop", "file:///etc/passwd"}, {"--unknown-option"}, {"pause"}, {"clipboard", "show", "extra"}, {"service", "stop", "extra"}, {"version", "extra"}, {"completion"}} {
		code, _, stderr := runCLI(t, "", append([]string{"--no-start"}, args...)...)
		if code != 2 || stderr == "" {
			t.Errorf("%v: exit=%d err=%q", args, code, stderr)
		}
	}
	code, _, stderr := runCLI(t, "", "--no-start", "--json", "send")
	var problem struct {
		Error string `json:"error"`
		Code  int    `json:"exit_code"`
	}
	if json.Unmarshal([]byte(stderr), &problem) != nil || code != 2 || problem.Code != 2 || problem.Error == "" {
		t.Fatalf("JSON usage error: %d %q", code, stderr)
	}
	if err := os.WriteFile(os.Getenv("RELAY_CONFIG"), []byte("[receive]\nunknown_option = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI(t, "", "--no-start", "status")
	if code != 2 || !strings.Contains(stderr, "unknown_option") {
		t.Fatalf("invalid config: %d %q", code, stderr)
	}
}

func TestCLINoStartNeverCreatesDaemonState(t *testing.T) {
	dir := cliEnvironment(t)
	code, out, stderr := runCLI(t, "", "--no-start", "--json", "status")
	if code == 0 || out != "" || stderr == "" {
		t.Fatalf("missing daemon: %d %q %q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "relay-state")); !os.IsNotExist(err) {
		t.Fatalf("--no-start created daemon state: %v", err)
	}
	if _, err := os.Stat(os.Getenv("RELAY_SOCKET")); !os.IsNotExist(err) {
		t.Fatalf("--no-start created socket: %v", err)
	}
}

func TestCLIPipelineAndClipboardRoundTrip(t *testing.T) {
	cliEnvironment(t)
	text := "git diff context\nUnicode: λ 漢字\n"
	code, out, stderr := runCLI(t, text, "clipboard", "copy")
	if code != 0 || out != "" || !strings.Contains(stderr, "Relay Clipboard") {
		t.Fatalf("copy: %d %q %q", code, out, stderr)
	}
	code, out, stderr = runCLI(t, "", "clipboard", "paste")
	if code != 0 || out != text || stderr != "" {
		t.Fatalf("paste: %d %q %q", code, out, stderr)
	}
	code, out, stderr = runCLI(t, "", "--json", "clipboard", "show")
	var item map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &item) != nil || item["text"] != text || stderr != "" {
		t.Fatalf("JSON show: %d %q %q", code, out, stderr)
	}
	server := fakeDaemon(t)
	code, out, stderr = runCLI(t, text, "--no-start", "text", "laptop", "--detach", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("piped text: %d %q %q", code, out, stderr)
	}
	code, _, stderr = runCLI(t, "", "--no-start", "clip", "laptop", "--detach")
	if code != 0 {
		t.Fatalf("clip: %d %s", code, stderr)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.sends) != 2 || server.sends[0].Text != text || server.sends[0].Kind != "text" || server.sends[1].Text != text || server.sends[1].Kind != "clipboard" {
		t.Fatalf("sent %+v", server.sends)
	}
}

func TestCLISendWaitsForVerifiedCompletion(t *testing.T) {
	dir := cliEnvironment(t)
	server := fakeDaemon(t)
	source := filepath.Join(dir, "notes with spaces.txt")
	if err := os.WriteFile(source, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "", "--no-start", "send", "laptop", source)
	if code != 0 || !strings.Contains(out, "Verified") || !strings.Contains(stderr, "transferring") || !strings.Contains(stderr, "Ctrl-C leaves the daemon working") {
		t.Fatalf("send: %d %q %q", code, out, stderr)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.sends) != 1 || server.sends[0].Paths[0] != source {
		t.Fatalf("send paths: %+v", server.sends)
	}
}

func TestCLIJSONSendAndTransferFailure(t *testing.T) {
	cliEnvironment(t)
	server := fakeDaemon(t)
	code, out, stderr := runCLI(t, "hello", "--no-start", "--json", "text", "laptop")
	var tr model.Transfer
	if code != 0 || json.Unmarshal([]byte(out), &tr) != nil || tr.Status != "completed" || !tr.Verified || stderr != "" {
		t.Fatalf("completion: %d %q %q", code, out, stderr)
	}
	code, out, stderr = runCLI(t, "hello", "--no-start", "--json", "text", "laptop", "--detach")
	var result model.Result
	if code != 0 || json.Unmarshal([]byte(out), &result) != nil || result.ID != "queued-id" || !result.OK || stderr != "" {
		t.Fatalf("detach: %d %q %q", code, out, stderr)
	}
	server.mu.Lock()
	server.outcome = "rejected"
	server.mu.Unlock()
	code, out, stderr = runCLI(t, "hello", "--no-start", "--json", "text", "laptop")
	if code != 3 || json.Unmarshal([]byte(out), &tr) != nil || tr.Status != "rejected" || !strings.Contains(stderr, "receiver declined") {
		t.Fatalf("rejection: %d %q %q", code, out, stderr)
	}
}

func TestCLIStdinSpoolIsBinaryAndResumable(t *testing.T) {
	dir := cliEnvironment(t)
	server := fakeDaemon(t)
	binary := string([]byte{0, 1, 2, 255, 0}) + strings.Repeat("payload", 1000)
	code, _, stderr := runCLI(t, binary, "--no-start", "send", "laptop", "--stdin", "archive.tar", "--detach")
	if code != 0 {
		t.Fatalf("stream: %d %s", code, stderr)
	}
	server.mu.Lock()
	req := server.sends[0]
	server.mu.Unlock()
	if !req.Spool || len(req.Paths) != 1 || filepath.Base(req.Paths[0]) != "archive.tar" || !strings.HasPrefix(req.Paths[0], filepath.Join(dir, "relay-state", "stdin")+string(filepath.Separator)) {
		t.Fatalf("spool request %+v", req)
	}
	b, err := os.ReadFile(req.Paths[0])
	if err != nil || string(b) != binary {
		t.Fatalf("spool content: %v", err)
	}
	st, _ := os.Stat(req.Paths[0])
	if st.Mode().Perm() != 0600 {
		t.Fatalf("spool mode %o", st.Mode().Perm())
	}
	code, _, stderr = runCLI(t, "text", "--no-start", "send", "laptop", "--stdin", "../escape", "--detach")
	if code != 2 {
		t.Fatalf("unsafe name: %d %s", code, stderr)
	}
}

func TestCLIPairingDoesNotImplicitlyTrust(t *testing.T) {
	cliEnvironment(t)
	server := fakeDaemon(t)
	code, out, stderr := runCLI(t, "", "--no-start", "pair", "laptop")
	if code != 0 || !strings.Contains(out, "Compare this fingerprint") || stderr != "" {
		t.Fatalf("pair: %d %q %q", code, out, stderr)
	}
	code, _, stderr = runCLI(t, "", "--no-start", "trust", "laptop")
	if code != 2 || !strings.Contains(stderr, "--fingerprint") {
		t.Fatalf("noninteractive trust: %d %q", code, stderr)
	}
	code, _, stderr = runCLI(t, "", "--no-start", "trust", "laptop", "--fingerprint", strings.Repeat("c", 64))
	if code == 0 || !strings.Contains(stderr, "fingerprints do not match") {
		t.Fatalf("mismatch: %d %q", code, stderr)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.actions) != 2 || server.actions[0].Action != "pair" || server.actions[1].Action != "trust" {
		t.Fatalf("unexpected implicit trust %+v", server.actions)
	}
}

func TestCLIRejectedQueueCleansOnlyItsOwnStdinSpool(t *testing.T) {
	dir := cliEnvironment(t)
	server := fakeDaemon(t)
	server.mu.Lock()
	server.sendFailure = true
	server.mu.Unlock()
	root := filepath.Join(dir, "relay-state", "stdin")
	if err := os.MkdirAll(filepath.Join(root, "existing"), 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "existing", "keep.txt")
	if err := os.WriteFile(keep, []byte("existing transfer"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "payload", "--no-start", "send", "laptop", "--stdin", "new.txt", "--detach")
	if code == 0 || !strings.Contains(stderr, "not trusted") {
		t.Fatalf("queue failure: %d %q", code, stderr)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
		t.Fatalf("spool cleanup: %v %v", entries, err)
	}
	b, err := os.ReadFile(keep)
	if err != nil || string(b) != "existing transfer" {
		t.Fatalf("existing spool changed: %v", err)
	}
}

func TestCLITextLimitAndTerminalSafety(t *testing.T) {
	if _, err := readText(strings.NewReader(strings.Repeat("x", clipboard.MaxBytes+1))); err == nil {
		t.Fatal("oversized text accepted")
	}
	got := safe("normal\x1b]52;c;hidden\a\u202etxt")
	if strings.ContainsAny(got, "\x1b\a\u202e") {
		t.Fatal("terminal controls were preserved")
	}
	var out bytes.Buffer
	a := &app{out: &out}
	a.transfers([]model.Transfer{{ID: "id", Name: "a\x1b[2J", Peer: "p\rpeer", Status: "completed"}})
	if strings.ContainsAny(out.String(), "\x1b\r") {
		t.Fatal("history emitted terminal controls")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := io.ReadAll(&contextReader{ctx: canceled, r: strings.NewReader("data")})
	if err != context.Canceled {
		t.Fatalf("cancelled stream: %v", err)
	}
}
