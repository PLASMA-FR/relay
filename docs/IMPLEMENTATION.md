# Implementation contracts

Module: `github.com/PLASMA-FR/relay`, Go 1.25+ (os.Root available). Root owns
model, config, ipc client, clipboard, cmd/relay, packaging and documentation.

Daemon owns internal/daemon and internal/tailscale. Public surface:
`daemon.New(cfg config.Config) (*Daemon, error)` and `Run(ctx context.Context) error`.
Config is passed by value. Unix HTTP IPC endpoints:
`GET /v1/status` → model.Snapshot; `GET /v1/events` → newline-delimited snapshots,
initial snapshot then changes, keepalive permitted (blank line);
`POST /v1/send` model.SendRequest → model.Result;
`POST /v1/action` model.Action → model.Result. Errors JSON `{error: string}`.
Actions: refresh, accept, reject, pause, resume, cancel, pair, trust, untrust,
clipboard-copy, clipboard-show. Pair returns observed Peer/fingerprint without
trusting. Trust requires the full observed fingerprint. Trust is directional:
both devices must explicitly trust each other. No remote commands/URL launch.
Snapshot includes history and pending incoming offers (`status=offered`).
Transferring statuses: queued, preparing, offered, transferring, verifying,
paused, interrupted, completed, cancelled, rejected, failed.

Root IPC client: `ipc.New(socket string) *Client`, `Status(ctx) (model.Snapshot,error)`,
`Send(ctx, model.SendRequest) (model.Result,error)`, `Action(ctx,model.Action)
(model.Result,error)`, `Watch(ctx, func(model.Snapshot)) error`.

TUI owns internal/tui. `tui.Run(ctx context.Context, client *ipc.Client,
cfg config.Config) error`. Use tview/tcell: event-driven incremental screen diff,
first-class mouse. Integrate initial status and Watch with reconnect. Plain
helper methods/model tests and tcell SimulationScreen for keyboard/mouse/resizing.
Never call peer protocol directly. All user/peer filenames terminal-safe escaped.

Transfer owner owns internal/identity, internal/protocol, internal/transfer;
coordinate engine API directly with daemon owner. TLS 1.3 Ed25519 self-signed
certificates and SHA256 SPKI pinning. Discovery only exposes bounded hello;
application transfers require pinned identity and current trust. Versioned
length-bounded binary frames; bounded streaming and verified resume. Store
metadata atomically, partial payload on disk, safe extraction (reject symlinks
for V1 and explain explicitly), no overwritten destinations. Parallelism bounded.

Config root-owned fields (all public): Name string, Paths config.Paths
(ConfigFile, StateDir, CacheDir, Socket string), Receive (Directory string,
AutoAcceptTrusted bool, Conflict string, MaxBytes int64), UI (Mouse,VimKeys,ASCII
bool), Clipboard (FallbackInternal bool), Network (TailscaleOnly bool, Port int,
Listen string, MaxConcurrent int, DiscoverySeconds int). Listen permits explicit
loopback only in non-tailnet integration tests; production never public binds.
`config.Load(path string) (Config,error)`, `config.Default() Config`,
`config.Ensure(Config) error`, `config.ExpandPath(string) string`.

Clipboard root-owned: `clipboard.New(stateDir string) *Manager`,
`Backend() string`, `Read(ctx) (string,error)`, `Write(ctx,string) error`,
`ReadInternal() (string,error)`, `WriteInternal(string) error`.
Incoming text updates only the Relay clipboard; no unsolicited system paste.

Do not commit other agents' work. Root handles git commits after staged review.
