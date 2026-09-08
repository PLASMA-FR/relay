package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/PLASMA-FR/relay/internal/clipboard"
	"github.com/PLASMA-FR/relay/internal/config"
	"github.com/PLASMA-FR/relay/internal/daemon"
	"github.com/PLASMA-FR/relay/internal/doctor"
	"github.com/PLASMA-FR/relay/internal/ipc"
	"github.com/PLASMA-FR/relay/internal/model"
	"github.com/PLASMA-FR/relay/internal/service"
	"github.com/PLASMA-FR/relay/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

type app struct {
	cfg           config.Config
	configPath    string
	json, noStart bool
	client        *ipc.Client
	in            io.Reader
	out, errOut   io.Writer
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
func execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if f, ok := in.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		stop := context.AfterFunc(ctx, func() { _ = f.Close() })
		defer stop()
	}
	a := &app{in: in, out: out, errOut: errOut}
	root := a.command()
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	err := root.ExecuteContext(ctx)
	if a.client != nil {
		a.client.Close()
	}
	if err == nil {
		return 0
	}
	code := 1
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	if errors.Is(err, context.Canceled) {
		code = 130
	}
	if a.json {
		_ = json.NewEncoder(errOut).Encode(map[string]any{"error": err.Error(), "exit_code": code})
	} else {
		fmt.Fprintf(errOut, "relay: %s\n", safe(err.Error()))
	}
	return code
}
func (a *app) command() *cobra.Command {
	root := &cobra.Command{Use: "relay", Short: "Files, clipboard and context — across your Tailnet", Long: "Relay is a private, terminal-native sharing desk for your Tailscale devices.\nRun without a command to open the live interface.", Version: model.Version, SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs}
	root.AddCommand(a.mobileCommand())
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "configuration file (default ~/.config/relay/config.toml)")
	root.PersistentFlags().BoolVar(&a.json, "json", false, "machine-readable JSON output")
	root.PersistentFlags().BoolVar(&a.noStart, "no-start", false, "do not automatically start the local daemon")
	root.SetFlagErrorFunc(func(_ *cobra.Command, e error) error { return &exitError{2, e} })
	root.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		var e error
		a.cfg, e = config.Load(a.configPath)
		if e != nil {
			return &exitError{2, e}
		}
		a.client = ipc.New(a.cfg.Paths.Socket)
		return nil
	}
	root.RunE = func(c *cobra.Command, _ []string) error {
		if a.json {
			s, e := a.status(c.Context())
			if e != nil {
				return e
			}
			return a.printJSON(s)
		}
		f, ok := a.in.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			return &exitError{2, errors.New("the TUI needs a terminal; use relay status, relay devices, or relay --help")}
		}
		if os.Getenv("TERM") == "dumb" || os.Getenv("TERM") == "" {
			return &exitError{2, errors.New("the TUI needs an xterm-compatible TERM; use relay status for plain output")}
		}
		if e := a.ensureDaemon(c.Context()); e != nil {
			return e
		}
		return tui.Run(c.Context(), a.client, a.cfg)
	}
	add := func(use, short string, args cobra.PositionalArgs, run func(*cobra.Command, []string) error) *cobra.Command {
		c := &cobra.Command{Use: use, Short: short, Args: func(c *cobra.Command, v []string) error {
			if e := args(c, v); e != nil {
				return &exitError{2, e}
			}
			return nil
		}, RunE: run}
		root.AddCommand(c)
		return c
	}
	add("daemon", "Run the transfer daemon in the foreground", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		d, e := daemon.New(a.cfg)
		if e != nil {
			return e
		}
		return d.Run(c.Context())
	})
	add("stop", "Gracefully stop the local daemon; preserve recoverable jobs", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		r, e := a.client.Action(c.Context(), model.Action{Action: "shutdown"})
		if e != nil {
			return e
		}
		return a.result(r)
	})
	add("status", "Show daemon, Tailnet and inbox status", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		s, e := a.status(c.Context())
		if e != nil {
			return e
		}
		if a.json {
			return a.printJSON(s)
		}
		fmt.Fprintf(a.out, "RELAY %s · %s\nTailscale  %s  %s\nInbox      %s\nClipboard  %s\nIdentity   %s\n", safe(s.Version), safe(s.Name), safe(s.Tailscale), safe(s.Address), safe(s.ReceiveDirectory), safe(s.ClipboardBackend), safe(s.Fingerprint))
		active := 0
		for _, t := range s.Transfers {
			if !t.Terminal() {
				active++
			}
		}
		fmt.Fprintf(a.out, "%d devices · %d active transfers\n", len(s.Peers), active)
		for _, n := range s.Notices {
			fmt.Fprintln(a.out, safe(n))
		}
		return nil
	})
	add("devices", "Discover devices and see which ones run Relay", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		s, e := a.status(c.Context())
		if e != nil {
			return e
		}
		if a.json {
			return a.printJSON(s.Peers)
		}
		w := tabwriter.NewWriter(a.out, 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "DEVICE\tADDRESS\tSTATE\tTRUST\tVERSION")
		for _, p := range s.Peers {
			state := "offline"
			if p.Online {
				state = "Tailscale online"
			}
			if p.Relay {
				state = "Relay ready"
			}
			trust := "unpaired"
			if p.Trusted {
				trust = "trusted"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", safe(p.Name), safe(p.Address), state, trust, safe(p.Version))
		}
		w.Flush()
		if len(s.Peers) == 0 {
			fmt.Fprintln(a.out, "No peers yet. Connect Tailscale and run Relay on another device.")
		}
		return nil
	})
	var detach bool
	var stdinName string
	send := add("send DEVICE [PATH ...]", "Send files or directories; waits for verified completion", cobra.MinimumNArgs(1), func(c *cobra.Command, args []string) error {
		if stdinName == "" && len(args) < 2 {
			return &exitError{2, errors.New("provide at least one file or directory, or --stdin NAME")}
		}
		if stdinName != "" && (len(args) != 1 || !validStdinName(stdinName)) {
			return &exitError{2, errors.New("use --stdin with a plain filename and no additional paths")}
		}
		for _, path := range args[1:] {
			if _, err := os.Lstat(config.ExpandPath(path)); err != nil {
				return &exitError{2, err}
			}
		}
		if e := a.ensureDaemon(c.Context()); e != nil {
			return e
		}
		req := model.SendRequest{Peer: args[0], Kind: "file"}
		var spool string
		if stdinName != "" {
			if len(args) != 1 {
				return &exitError{2, errors.New("use paths or --stdin NAME, not both")}
			}
			path, e := a.spool(c.Context(), stdinName)
			if e != nil {
				return e
			}
			spool = filepath.Dir(path)
			req.Paths = []string{path}
			req.Spool = true
		} else {
			if len(args) < 2 {
				return &exitError{2, errors.New("provide at least one file or directory, or --stdin NAME")}
			}
			for _, p := range args[1:] {
				abs, e := filepath.Abs(config.ExpandPath(p))
				if e != nil {
					return e
				}
				req.Paths = append(req.Paths, abs)
			}
		}
		res, e := a.client.Send(c.Context(), req)
		if e != nil {
			if spool != "" {
				os.RemoveAll(spool)
			}
			return e
		}
		return a.sent(c.Context(), res, detach)
	})
	send.Flags().BoolVar(&detach, "detach", false, "queue the transfer and return immediately")
	send.Flags().StringVar(&stdinName, "stdin", "", "stream stdin into a resumable spool with this filename")
	for _, kind := range []string{"text", "open", "clip"} {
		kind := kind
		use := kind + " DEVICE [TEXT]"
		short := "Send text or piped stdin"
		max := 2
		if kind == "open" {
			use = "open DEVICE URL"
			short = "Send an HTTP(S) URL to the remote Relay inbox"
		}
		if kind == "clip" {
			use = "clip DEVICE"
			short = "Send the local clipboard (headless fallback included)"
			max = 1
		}
		var detachText bool
		c := add(use, short, cobra.RangeArgs(1, max), func(c *cobra.Command, args []string) error {
			if kind == "open" && len(args) != 2 {
				return &exitError{2, errors.New("usage: relay open DEVICE URL")}
			}
			req := model.SendRequest{Peer: args[0], Kind: kind}
			var e error
			if kind == "clip" {
				req.Kind = "clipboard"
				m := clipboard.New(a.cfg.Paths.StateDir)
				m.FallbackInternal = a.cfg.Clipboard.FallbackInternal
				req.Text, e = m.Read(c.Context())
			} else if len(args) == 2 {
				req.Text = args[1]
			} else {
				req.Text, e = readText(a.in)
			}
			if e != nil {
				return e
			}
			if kind == "open" {
				req.Kind = "url"
				u, e := url.Parse(req.Text)
				if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
					return &exitError{2, errors.New("provide an http:// or https:// URL; Relay sends it without launching a browser")}
				}
			}
			if len(req.Text) > clipboard.MaxBytes {
				return &exitError{2, errors.New("text exceeds 1 MiB; use relay send --stdin NAME")}
			}
			if e := a.ensureDaemon(c.Context()); e != nil {
				return e
			}
			res, e := a.client.Send(c.Context(), req)
			if e != nil {
				return e
			}
			return a.sent(c.Context(), res, detachText)
		})
		c.Flags().BoolVar(&detachText, "detach", false, "queue and return immediately")
	}
	add("history", "Show recent completed and recoverable transfers", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		s, e := a.status(c.Context())
		if e != nil {
			return e
		}
		if a.json {
			return a.printJSON(s.History)
		}
		a.transfers(s.History)
		return nil
	})
	add("transfers", "Show queued, incoming and active transfers", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		s, e := a.status(c.Context())
		if e != nil {
			return e
		}
		if a.json {
			return a.printJSON(s.Transfers)
		}
		a.transfers(s.Transfers)
		return nil
	})
	for _, action := range []string{"accept", "reject", "pause", "resume", "cancel"} {
		action := action
		add(action+" ID", strings.ToUpper(action[:1])+action[1:]+" a transfer (IDs from relay transfers)", cobra.ExactArgs(1), func(c *cobra.Command, args []string) error {
			if e := a.ensureDaemon(c.Context()); e != nil {
				return e
			}
			r, e := a.client.Action(c.Context(), model.Action{Action: action, ID: args[0]})
			if e != nil {
				return e
			}
			return a.result(r)
		})
	}
	add("pair DEVICE", "Inspect a device identity before pairing", cobra.ExactArgs(1), func(c *cobra.Command, args []string) error {
		if e := a.ensureDaemon(c.Context()); e != nil {
			return e
		}
		r, e := a.client.Action(c.Context(), model.Action{Action: "pair", Peer: args[0]})
		if e != nil {
			return e
		}
		if a.json {
			return a.printJSON(r)
		}
		if r.Peer == nil {
			return errors.New("daemon did not return peer identity")
		}
		fmt.Fprintf(a.out, "%s\nSHA-256  %s\n\nCompare this fingerprint with 'relay status' on that device.\nThen run: relay trust %s --fingerprint %s\nRepeat in the other direction to enable sending.\n", safe(r.Peer.Name), safe(r.Peer.Fingerprint), safe(args[0]), safe(r.Peer.Fingerprint))
		return nil
	})
	var fingerprint string
	trust := add("trust [DEVICE]", "List trusted devices or trust a verified fingerprint", cobra.MaximumNArgs(1), func(c *cobra.Command, args []string) error {
		s, e := a.status(c.Context())
		if e != nil {
			return e
		}
		if len(args) == 0 {
			peers := []model.Peer{}
			for _, p := range s.Peers {
				if p.Trusted {
					peers = append(peers, p)
				}
			}
			if a.json {
				return a.printJSON(peers)
			}
			for _, p := range peers {
				fmt.Fprintf(a.out, "%s  %s\n", safe(p.Name), safe(p.Fingerprint))
			}
			if len(peers) == 0 {
				fmt.Fprintln(a.out, "No trusted devices. Run relay pair DEVICE to compare fingerprints.")
			}
			return nil
		}
		if fingerprint == "" {
			if a.json || !isTerminal(a.in) {
				return &exitError{2, errors.New("trust requires --fingerprint SHA256 from the remote device's relay status")}
			}
			r, e := a.client.Action(c.Context(), model.Action{Action: "pair", Peer: args[0]})
			if e != nil {
				return e
			}
			if r.Peer == nil {
				return errors.New("peer identity unavailable")
			}
			fmt.Fprintf(a.errOut, "Observed fingerprint: %s\nCompare 'relay status' on the remote device.\nPaste the full verified fingerprint to trust this device: ", safe(r.Peer.Fingerprint))
			line, e := bufio.NewReader(a.in).ReadString('\n')
			if e != nil {
				return e
			}
			fingerprint = strings.TrimSpace(line)
			if fingerprint != r.Peer.Fingerprint {
				return &exitError{3, errors.New("fingerprints do not match; no trust was granted")}
			}
		}
		r, e := a.client.Action(c.Context(), model.Action{Action: "trust", Peer: args[0], Fingerprint: fingerprint})
		if e != nil {
			return e
		}
		return a.result(r)
	})
	trust.Flags().StringVar(&fingerprint, "fingerprint", "", "full SHA-256 identity fingerprint verified on the remote device")
	add("untrust DEVICE", "Revoke trust and stop this peer's active transfers", cobra.ExactArgs(1), func(c *cobra.Command, args []string) error {
		if e := a.ensureDaemon(c.Context()); e != nil {
			return e
		}
		r, e := a.client.Action(c.Context(), model.Action{Action: "untrust", Peer: args[0]})
		if e != nil {
			return e
		}
		return a.result(r)
	})
	clip := &cobra.Command{Use: "clipboard", Short: "Use the clipboard without a desktop"}
	root.AddCommand(clip)
	clip.AddCommand(&cobra.Command{Use: "show", Aliases: []string{"paste"}, Short: "Print Relay Clipboard text (pipe-friendly)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		m := clipboard.New(a.cfg.Paths.StateDir)
		s, e := m.ReadInternal()
		if e != nil {
			return e
		}
		if a.json {
			return a.printJSON(map[string]string{"backend": "Relay Clipboard", "text": s})
		}
		if isTerminal(a.out) {
			s = safeMultiline(s)
		}
		_, e = io.WriteString(a.out, s)
		return e
	}})
	clip.AddCommand(&cobra.Command{Use: "copy [TEXT]", Short: "Copy text or stdin to the available clipboard", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		var s string
		var e error
		if len(args) == 1 {
			s = args[0]
		} else {
			s, e = readText(a.in)
		}
		if e != nil {
			return e
		}
		m := clipboard.New(a.cfg.Paths.StateDir)
		m.FallbackInternal = a.cfg.Clipboard.FallbackInternal
		if e = m.Write(c.Context(), s); e != nil {
			return e
		}
		if a.json {
			return a.printJSON(map[string]any{"ok": true, "backend": m.Backend(), "bytes": len(s)})
		}
		fmt.Fprintf(a.errOut, "Copied %s to %s.\n", humanBytes(int64(len(s))), m.Backend())
		return nil
	}})
	add("doctor", "Diagnose configuration, Tailscale, IPC, inbox and terminal", cobra.NoArgs, func(c *cobra.Command, _ []string) error {
		r := doctor.Run(c.Context(), a.cfg)
		if a.json {
			if e := a.printJSON(r); e != nil {
				return e
			}
		} else {
			fmt.Fprintln(a.out, "RELAY DOCTOR")
			for _, v := range r.Checks {
				symbol := "ok"
				if v.Status != "ok" {
					symbol = v.Status
				}
				fmt.Fprintf(a.out, "%-5s %-24s %s\n", symbol, safe(v.Name), safe(v.Detail))
				if v.Fix != "" {
					fmt.Fprintf(a.out, "      %s\n", safe(v.Fix))
				}
			}
			if r.Ready {
				fmt.Fprintln(a.out, "\nRelay is ready.")
			}
		}
		if !r.Ready {
			return &exitError{1, errors.New("doctor found issues; follow the remediation above")}
		}
		return nil
	})
	svc := &cobra.Command{Use: "service", Short: "Manage the Linux user systemd service"}
	root.AddCommand(svc)
	for _, action := range []string{"install", "start", "stop", "restart", "status", "uninstall"} {
		action := action
		svc.AddCommand(&cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			if action == "install" {
				if e := config.Ensure(a.cfg); e != nil {
					return e
				}
			}
			s, e := service.Run(c.Context(), action, a.cfg)
			if e != nil {
				return e
			}
			if a.json {
				return a.printJSON(map[string]any{"ok": true, "message": s})
			}
			fmt.Fprintln(a.out, s)
			return nil
		}})
	}
	completion := &cobra.Command{Use: "completion [bash|zsh|fish]", Short: "Generate shell completions", Args: cobra.ExactArgs(1), ValidArgs: []string{"bash", "zsh", "fish"}, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, RunE: func(c *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(a.out, true)
		case "zsh":
			return root.GenZshCompletion(a.out)
		case "fish":
			return root.GenFishCompletion(a.out, true)
		}
		return &exitError{2, errors.New("choose bash, zsh or fish")}
	}}
	root.AddCommand(completion)
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print Relay version", Args: cobra.NoArgs, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, RunE: func(*cobra.Command, []string) error {
		if a.json {
			return a.printJSON(map[string]any{"version": model.Version, "protocol": model.ProtocolVersion})
		}
		fmt.Fprintln(a.out, "relay", model.Version)
		return nil
	}})
	// Dynamic peer names at the first argument; ordinary filesystem completion
	// takes over for subsequent arguments of send.
	for _, c := range root.Commands() {
		switch c.Name() {
		case "send", "pair", "trust", "untrust", "text", "clip", "open":
			c.ValidArgsFunction = func(c *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
				if len(args) > 0 {
					return nil, cobra.ShellCompDirectiveDefault
				}
				cfg, e := config.Load(a.configPath)
				if e != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				ctx, cancel := context.WithTimeout(c.Context(), 300*time.Millisecond)
				defer cancel()
				client := ipc.New(cfg.Paths.Socket)
				defer client.Close()
				s, e := client.Status(ctx)
				if e != nil {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				var names []string
				for _, p := range s.Peers {
					if strings.HasPrefix(p.Name, prefix) {
						names = append(names, p.Name)
					}
				}
				return names, cobra.ShellCompDirectiveNoFileComp
			}
		}
	}
	var wrapArgs func(*cobra.Command)
	wrapArgs = func(c *cobra.Command) {
		if c.Args != nil {
			validate := c.Args
			c.Args = func(cmd *cobra.Command, args []string) error {
				if e := validate(cmd, args); e != nil {
					return &exitError{2, e}
				}
				return nil
			}
		}
		for _, sub := range c.Commands() {
			wrapArgs(sub)
		}
	}
	wrapArgs(root)
	return root
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func (a *app) result(r model.Result) error {
	if a.json {
		return a.printJSON(r)
	}
	fmt.Fprintln(a.out, safe(r.Message))
	return nil
}
func (a *app) status(ctx context.Context) (model.Snapshot, error) {
	if e := a.ensureDaemon(ctx); e != nil {
		return model.Snapshot{}, e
	}
	return a.client.Status(ctx)
}
func (a *app) ensureDaemon(ctx context.Context) error {
	test, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_, e := a.client.Status(test)
	cancel()
	if e == nil {
		return nil
	}
	if a.noStart {
		return e
	}
	if e = config.Ensure(a.cfg); e != nil {
		return e
	}
	if _, err := os.Stat(service.UnitPath()); err == nil {
		_, _ = service.Run(ctx, "start", a.cfg)
	}
	check, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	_, e = a.client.Status(check)
	stop()
	if e == nil {
		return nil
	}
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	log, e := os.OpenFile(filepath.Join(a.cfg.Paths.StateDir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	cmd := exec.Command(binary, "--config", a.cfg.Paths.ConfigFile, "daemon")
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		log.Close()
		return e
	}
	_ = cmd.Process.Release()
	log.Close()
	wait := time.NewTimer(6 * time.Second)
	defer wait.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait.C:
			return fmt.Errorf("daemon did not start; run 'relay daemon' for details or inspect %s", filepath.Join(a.cfg.Paths.StateDir, "daemon.log"))
		case <-tick.C:
			check, stop := context.WithTimeout(ctx, 300*time.Millisecond)
			_, e = a.client.Status(check)
			stop()
			if e == nil {
				return nil
			}
		}
	}
}
func (a *app) sent(ctx context.Context, r model.Result, detach bool) error {
	if detach {
		return a.result(r)
	}
	if !a.json {
		fmt.Fprintf(a.errOut, "Queued %s · Ctrl-C leaves the daemon working; relay cancel %s cancels it.\n", r.ID, r.ID)
	}
	var final model.Transfer
	var last string
	for {
		watchCtx, cancel := context.WithCancel(ctx)
		e := a.client.Watch(watchCtx, func(s model.Snapshot) {
			var found *model.Transfer
			for i := range s.Transfers {
				if s.Transfers[i].ID == r.ID {
					t := s.Transfers[i]
					found = &t
					break
				}
			}
			if found == nil {
				for i := range s.History {
					if s.History[i].ID == r.ID {
						t := s.History[i]
						found = &t
						break
					}
				}
			}
			if found == nil {
				return
			}
			t := *found
			if !a.json && t.Status != last {
				fmt.Fprintf(a.errOut, "%s · %s · %s / %s\n", safe(t.Name), safe(t.Status), humanBytes(t.Bytes), humanBytes(t.Total))
				last = t.Status
			}
			if t.Terminal() || t.Status == "paused" {
				final = t
				cancel()
			}
		})
		cancel()
		if final.ID != "" {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !a.json {
			fmt.Fprintln(a.errOut, "Daemon connection lost; waiting for it to return. The transfer remains recoverable.")
		}
		if e != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if a.json {
		if e := a.printJSON(final); e != nil {
			return e
		}
	} else if final.Status == "completed" && final.Verified {
		fmt.Fprintf(a.out, "Verified · %s · %s\n", safe(final.Name), humanBytes(final.Total))
	}
	if final.Status != "completed" {
		return &exitError{3, fmt.Errorf("transfer %s: %s %s; inspect with relay transfers", final.ID, final.Status, final.Error)}
	}
	if !final.Verified {
		return &exitError{3, errors.New("daemon reported completion without integrity verification; inspect relay transfers")}
	}
	return nil
}
func (a *app) transfers(ts []model.Transfer) {
	if len(ts) == 0 {
		fmt.Fprintln(a.out, "No transfers yet. Try relay send DEVICE PATH.")
		return
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tTIME\tPEER\tITEM\tSIZE\tSTATUS")
	for _, t := range ts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", safe(t.ID), t.Started.Local().Format("Jan 02 15:04"), safe(t.Peer), safe(t.Name), humanBytes(t.Total), safe(t.Status))
		if t.Error != "" {
			fmt.Fprintf(w, "\t\t\t%s\n", safe(t.Error))
		}
	}
	w.Flush()
}
func (a *app) spool(ctx context.Context, name string) (string, error) {
	if !validStdinName(name) {
		return "", &exitError{2, errors.New("--stdin needs a plain filename without directories or control characters")}
	}
	dir := filepath.Join(a.cfg.Paths.StateDir, "stdin")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	tmp, e := os.MkdirTemp(dir, "stream-*")
	if e != nil {
		return "", e
	}
	path := filepath.Join(tmp, name)
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		os.RemoveAll(tmp)
		return "", e
	}
	n, e := io.CopyBuffer(f, io.LimitReader(&contextReader{ctx, a.in}, a.cfg.Receive.MaxBytes+1), make([]byte, 256<<10))
	if e == nil && n > a.cfg.Receive.MaxBytes {
		e = errors.New("stdin exceeds receive.max_bytes; increase the limit explicitly")
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		os.RemoveAll(tmp)
		return "", e
	}
	return path, nil
}

func validStdinName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\") && safe(name) == name
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(b)
}
func readText(r io.Reader) (string, error) {
	b, e := io.ReadAll(io.LimitReader(r, clipboard.MaxBytes+1))
	if e != nil {
		return "", e
	}
	if len(b) > clipboard.MaxBytes {
		return "", &exitError{2, errors.New("text exceeds 1 MiB; use relay send --stdin NAME")}
	}
	return string(b), nil
}
func isTerminal(v any) bool { f, ok := v.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return '�'
		}
		return r
	}, s)
}
func safeMultiline(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return '�'
		}
		return r
	}, s)
}
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", v)
}
