// Package agents implements remote agent enrollment + the WebSocket
// control plane (concept §3.1, §15.3). The wire format is JSON frames
// over a single bidirectional WebSocket inside an mTLS connection.
//
// Slice 3.1.1 ships only the connection lifecycle: hello / welcome /
// heartbeat / ping. Subsequent slices add request/response frames for
// container ops, exec, logs and stats.
package agents

import (
	"encoding/json"
	"slices"
	"strings"
)

// Frame is the message envelope that flows in both directions.
type Frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	// Lifecycle (agent → server)
	FrameAgentHello     = "agent.hello"
	FrameAgentHeartbeat = "agent.heartbeat"
	FrameAgentPong      = "agent.pong"

	// Lifecycle (server → agent)
	FrameServerWelcome = "server.welcome"
	FrameServerPing    = "server.ping"

	// Container operations (server → agent: req.*, agent → server: res.*).
	// Each request carries a unique ID; the response echoes that ID so the
	// server can correlate it with the waiting goroutine.
	FrameReqContainerList    = "req.containers.list"
	FrameReqContainerInspect = "req.containers.inspect"
	FrameReqContainerStart   = "req.containers.start"
	FrameReqContainerStop    = "req.containers.stop"
	FrameReqContainerRestart = "req.containers.restart"
	FrameReqContainerRemove  = "req.containers.remove"
	// P.11.4 container lifecycle additions.
	FrameReqContainerPause   = "req.containers.pause"
	FrameReqContainerUnpause = "req.containers.unpause"
	FrameReqContainerKill    = "req.containers.kill"

	// Resource listings + mutations (server → agent)
	FrameReqImageList      = "req.images.list"
	FrameReqImageInspect   = "req.images.inspect"
	FrameReqImageHistory   = "req.images.history"
	FrameReqImageRemove    = "req.images.remove"
	FrameReqImagePrune     = "req.images.prune"
	FrameReqNetworkList    = "req.networks.list"
	FrameReqNetworkInspect = "req.networks.inspect"
	FrameReqNetworkCreate  = "req.networks.create"
	FrameReqNetworkRemove  = "req.networks.remove"
	FrameReqNetworkPrune   = "req.networks.prune"
	// FrameReqNetworkTopology asks for the host's topology.Graph; only
	// sent to agents advertising CapNetworkTopology.
	FrameReqNetworkTopology = "req.networks.topology"
	FrameReqVolumeList      = "req.volumes.list"
	FrameReqVolumeInspect   = "req.volumes.inspect"
	FrameReqVolumeCreate    = "req.volumes.create"
	FrameReqVolumeRemove    = "req.volumes.remove"
	FrameReqVolumePrune     = "req.volumes.prune"
	// Volume content browsing (P.11.8). Read-only: list one directory
	// level or read a single file capped at the caller's maxBytes.
	// The stream frame for large-file download is deferred — the UI
	// offers "too large, 5.2 MB" + a download button today.
	FrameReqVolumeBrowse     = "req.volume.browse"
	FrameReqVolumeBrowseFile = "req.volume.browse_file"
	FrameReqDaemonInfo       = "req.daemon.info"

	// Host-level system metrics (CPU / memory / disk / uptime) for the
	// dashboard's all-mode System Health panel. Payload is empty; the
	// agent reads its local /proc and /var/lib/docker via the system
	// package and returns the Metrics struct as JSON. Slice P.6.
	FrameReqSystemMetrics = "req.system.metrics"
	// Per-mount disk usage; only sent to agents advertising CapDiskMounts.
	FrameReqSystemDisks = "req.system.disks"

	// Stack operations (slice 3.1.3) — server ships compose YAML + .env
	// content with the deploy request, agent runs the same compose
	// executor as the server would for a local deploy.
	FrameReqStackDeploy = "req.stack.deploy"
	FrameReqStackStop   = "req.stack.stop"
	FrameReqStackStatus = "req.stack.status"

	// Agent self-upgrade (Polish). Server pushes a download URL and
	// expected version; agent downloads, replaces its own binary, and
	// restarts the systemd service.
	FrameReqAgentUpgrade = "req.agent.upgrade"

	// FrameReqStacksDirApply checks a new stacks directory on the agent
	// and, unless CheckOnly, asks the agent's root updater to switch to it.
	FrameReqStacksDirApply = "req.stacks_dir.apply"

	// Service scaling (P.8) — manual replica count adjustment.
	FrameReqStackScale      = "req.stack.scale"
	FrameReqStackCheckScale = "req.stack.check_scale"

	// Volume tar-stream (P.9 migration). The server opens a stream to
	// the source agent for tar-export (read), and another to the target
	// agent for tar-import (write). The server relays bytes between them.
	FrameReqVolumeTarExport = "req.volume.tar_export" // source: start tar czf - on volume
	FrameReqVolumeTarImport = "req.volume.tar_import" // target: start tar xzf - into volume

	// Synchronous exec — server blocks until the command exits. Used by
	// backup pre-hooks (pg_dump etc.) where request/response is the right
	// shape (no stdin, bounded stdout, we need the exit code).
	FrameReqContainerExecRun = "req.container.exec_run"

	// Container files (browse, preview, upload/edit, delete), answered by
	// agents advertising CapContainerFiles. Downloads use the
	// "container_file" stream kind after a stat.
	FrameReqContainerFilesList   = "req.container.files.list"
	FrameReqContainerFilesRead   = "req.container.files.read"
	FrameReqContainerFilesStat   = "req.container.files.stat"
	FrameReqContainerFilesWrite  = "req.container.files.write"
	FrameReqContainerFilesDelete = "req.container.files.delete"

	// Compose-file mirroring (P.7) — server pushes the canonical
	// compose+env content to the agent after a successful deploy so
	// each agent retains a local copy in case the main server is lost.
	// stack.delete removes the local copy when the stack is torn down.
	FrameReqStackSync   = "req.stack.sync"
	FrameReqStackDelete = "req.stack.delete"

	// FrameReqCancel tells the agent to abandon the in-flight request
	// carrying the same ID (the server stopped waiting for it).
	FrameReqCancel = "req.cancel"

	// FrameAgentCertRenew is the one agent → server request: the agent
	// sends a CSR over its mTLS session and gets a fresh client cert for
	// the same host ID back in a res frame.
	FrameAgentCertRenew = "agent.cert_renew"

	// FrameAgentMetrics carries a MetricsBatch of host and container
	// samples (agent → server, no response).
	FrameAgentMetrics = "agent.metrics"

	// Single response type. Errors set OK=false and put the message in Error.
	FrameRes = "res"

	// Stream multiplexing — one bidirectional logical stream per stream_id,
	// carried over the same agent connection. Used for logs, stats and exec
	// where request/response doesn't fit (long-lived, chunked, possibly
	// bidirectional).
	FrameStreamOpen    = "stream.open"    // server → agent: start a new stream
	FrameStreamData    = "stream.data"    // bidirectional: payload bytes
	FrameStreamClose   = "stream.close"   // bidirectional: end the stream
	FrameStreamControl = "stream.control" // bidirectional: out-of-band ops (resize, signals)
)

// ProtocolVersion is bumped whenever a frame or capability is added.
// Agents that report an older version (or none) still work; the server
// skips what they lack and recommends an update.
const ProtocolVersion = 7

// MaxFrameBytes bounds one inbound frame on either side; the largest
// legitimate ones are 1 MiB file reads and exec output, base64-encoded.
const MaxFrameBytes = 16 << 20

// Capabilities exchanged in hello / welcome.
const (
	CapRequestCancel = "req.cancel"            // agent: honours FrameReqCancel
	CapStreamCredit  = "stream.credit"         // both: credit-based stream flow control
	CapCertRenew     = "cert.renew"            // server: accepts FrameAgentCertRenew
	CapDiskMounts    = "disk_mounts"           // agent: answers FrameReqSystemDisks
	CapDockerEvents  = "docker_events"         // agent: serves the "events" stream kind
	CapVolumeReplace = "volume_import.replace" // agent: takes params.replace; still sent for older servers, which check it instead of CapVolumeStaged
	// CapMetricsHistory, agent: sends FrameAgentMetrics; server: stores them.
	CapMetricsHistory = "metrics.history"
	// CapVolumeStaged, agent: a replace restore extracts into a staging
	// directory and swaps only after tar succeeded (VolumeImportScript).
	CapVolumeStaged = "volume_import.staged"
	// CapContainerFiles, agent: answers the req.container.files.* frames
	// and serves the "container_file" stream kind.
	CapContainerFiles = "container_files"
	// CapVolumeManage, agent: answers req.volumes.create and req.volumes.remove.
	CapVolumeManage = "volumes.manage"
	// CapResourceManage, agent: answers req.volumes.prune and the
	// req.networks.create/remove/prune frames.
	CapResourceManage = "resources.manage"
	// CapVolumeFileDownload, agent: serves the "volume_file" stream kind.
	CapVolumeFileDownload = "volume_file.download"
	// CapReservedLabels, agent: refuses compose labels that would override
	// Dockmesh's tracking labels on deploy and scale.
	CapReservedLabels = "compose.reserved_labels"
	// CapImageTransfer, agent: serves the "image_pull" and "image_export"
	// stream kinds.
	CapImageTransfer = "images.transfer"
	// CapImageHistory, agent: answers req.images.history.
	CapImageHistory = "images.history"
	// CapNoEnvMirror, agent: keeps no .env copy, so req.stack.sync goes
	// without the plaintext env.
	CapNoEnvMirror = "stacks.no_env_mirror"
	// CapNetworkTopology, agent: answers req.networks.topology.
	CapNetworkTopology = "networks.topology"
)

// Stream control ops beyond the exec "resize".
const (
	// StreamOpCredit grants the peer Params["n"] more data frames.
	StreamOpCredit = "credit"
	// StreamOpEOF ends the server → agent data direction; the agent
	// finishes and reports the result in its stream.close.
	StreamOpEOF = "eof"
)

// StreamWindow is the number of data frames either side may have in
// flight per stream before it has to wait for credit.
const StreamWindow = 64

// StreamOpen is the server → agent payload that requests a new stream.
// Kind picks the data source on the agent side. Params are kind-specific
// (e.g. {"tail":"100"} for logs).
type StreamOpen struct {
	StreamID  string         `json:"stream_id"`
	Kind      string         `json:"kind"` // "logs" | "stats" | "exec"
	Container string         `json:"container"`
	Params    map[string]any `json:"params,omitempty"`
}

// StreamData carries an opaque chunk of stream payload. Bytes are
// base64-encoded by default JSON marshalling so we can ship raw binary
// (docker multiplexed log frames, etc.) without escaping issues.
type StreamData struct {
	StreamID string `json:"stream_id"`
	Data     []byte `json:"data"`
}

// StreamClose terminates a stream. Error is non-empty if the stream ended
// because of a failure (otherwise it's a clean EOF).
type StreamClose struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

// StreamControl carries out-of-band events that don't fit the byte-oriented
// data channel. Used by exec for tty resize: Op="resize", Params={cols,rows}.
type StreamControl struct {
	StreamID string         `json:"stream_id"`
	Op       string         `json:"op"`
	Params   map[string]any `json:"params,omitempty"`
}

// ResponseEnvelope is the wire format every response uses. Data is the
// JSON-encoded result of the operation; both sides use the same docker
// SDK so types match without translation.
type ResponseEnvelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Request payloads — kept tiny, most operations only need an ID + maybe
// a flag.

type ContainerListReq struct {
	All bool `json:"all"`
}

type ContainerIDReq struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

// ContainerKillReq carries the target id plus the optional signal name.
// Empty signal is treated by Docker as SIGKILL (its default for Kill).
type ContainerKillReq struct {
	ID     string `json:"id"`
	Signal string `json:"signal,omitempty"`
}

// AgentUpgradeReq tells the agent to download a new binary and restart.
// Legacy agents fetch the binary from the agent listener over mTLS and
// verify SHA256; BinaryURL is only used by agents that predate that.
// Managed agents get only Version: they fetch the signed release of that
// version and verify it with their own keys.
type AgentUpgradeReq struct {
	BinaryURL string `json:"binary_url"` // https://server/install/dockmesh-agent-linux-amd64
	Version   string `json:"version"`    // expected version after upgrade
	SHA256    string `json:"sha256"`     // hex SHA-256 of the binary at BinaryURL
}

type ResourceIDReq struct {
	ID string `json:"id"`
}

type ImageRemoveReq struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

type VolumeCreateReq struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type VolumeRemoveReq struct {
	Name  string `json:"name"`
	Force bool   `json:"force,omitempty"`
}

type NetworkCreateReq struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// StackDeployReq carries the full compose project payload to the agent.
// The agent writes Compose + Env to a temp directory, parses with
// compose-go and runs the same DeployProject executor the server uses.
//
// Auth maps image references to X-Registry-Auth blobs for the pulls of
// this request; ForcePull pulls every image even when it is cached.
type StackDeployReq struct {
	Name      string            `json:"name"`
	Compose   string            `json:"compose"`
	Env       string            `json:"env,omitempty"`
	Auth      map[string]string `json:"auth,omitempty"`
	ForcePull bool              `json:"force_pull,omitempty"`
}

type StackNameReq struct {
	Name string `json:"name"`
}

// StackScaleReq carries the compose content alongside the scale
// request so the agent can parse the project locally. P.8.
type StackScaleReq struct {
	Name     string            `json:"name"`
	Compose  string            `json:"compose"`
	Env      string            `json:"env,omitempty"`
	Service  string            `json:"service"`
	Replicas int               `json:"replicas"`
	Auth     map[string]string `json:"auth,omitempty"`
}

// StackCheckScaleReq is the pre-flight check variant. P.8.
type StackCheckScaleReq struct {
	Name    string            `json:"name"`
	Compose string            `json:"compose"`
	Env     string            `json:"env,omitempty"`
	Service string            `json:"service"`
	Auth    map[string]string `json:"auth,omitempty"`
}

// VolumeTarReq identifies the volume for tar export/import.
type VolumeTarReq struct {
	Volume string `json:"volume"`
}

// VolumeBrowseReq is the payload for directory-listing / file-read
// requests (P.11.8). SubPath is relative to the volume root; empty or
// "/" means the root itself. MaxBytes caps the read-file response —
// callers pass 0 for the 1 MiB default.
type VolumeBrowseReq struct {
	Volume   string `json:"volume"`
	SubPath  string `json:"sub_path,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// ContainerExecRunReq is the payload for FrameReqContainerExecRun.
// Used by backup pre-hooks to run (e.g.) pg_dump inside a container
// running on a remote agent. Bounded output — 1 MiB default cap.
type ContainerExecRunReq struct {
	Container      string   `json:"container"`
	Cmd            []string `json:"cmd"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty"`
}

// ContainerFileReq is the payload of the req.container.files.* frames.
// Path is absolute inside the container; Data and Mode are only used by
// writes, MaxBytes only by reads.
type ContainerFileReq struct {
	Container string `json:"container"`
	Path      string `json:"path"`
	MaxBytes  int64  `json:"max_bytes,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Mode      int64  `json:"mode,omitempty"`
}

// ContainerExecRunRes is the response.
type ContainerExecRunRes struct {
	Stdout   []byte `json:"stdout"`
	ExitCode int    `json:"exit_code"`
}

// StackSyncReq carries the compose file and optional meta for local
// storage on the agent.
type StackSyncReq struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	// Env goes only to agents without CapNoEnvMirror: those remove their
	// .env copy when it is empty, even one a container bind-mounts.
	Env  string `json:"env,omitempty"`
	Meta string `json:"meta,omitempty"` // .dockmesh.meta.json content
}

// HelloPayload is what the agent sends as soon as the WS opens. It tells
// the server which version, OS and docker daemon are on the other end.
//
// `Version` is the human-readable build label ("dev", "1.4.2", …) and
// `Commit` is the short git hash. The auto-upgrade controller compares
// Commit, not Version, because version-string matches fall apart once
// ldflags start embedding a +<hash> suffix — that was the cause of an
// infinite upgrade loop observed during Phase-2 testing (server said
// "dev", agent said "dev+abc1234", strings mismatched every round).
type HelloPayload struct {
	Version       string `json:"version"`
	Commit        string `json:"commit,omitempty"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	DockerVersion string `json:"docker_version,omitempty"`
	// ProtocolVersion and Capabilities are zero for agents that predate
	// negotiation.
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	// MirroredEnv is set by agents that no longer mirror stack .env files
	// while plaintext copies from older versions are still on the host.
	MirroredEnv bool `json:"mirrored_env,omitempty"`
	// Daemon is nil from agents that predate it, and when the daemon
	// could not be asked.
	Daemon *DaemonInfo `json:"daemon,omitempty"`
	// Layout is LayoutLegacy or LayoutManaged; agents that predate it
	// are legacy.
	Layout string `json:"layout,omitempty"`
	// UpdateResult is the root updater's result of the last managed
	// update, sent until a server welcomed it.
	UpdateResult *AgentUpdateResult `json:"update_result,omitempty"`
	// StacksDirs lists the agent's stacks directories, the current one
	// first; agents that predate it send none.
	StacksDirs []StacksDirInfo `json:"stacks_dirs,omitempty"`
}

// StacksDirApplyReq asks for Path as the agent's stacks directory.
type StacksDirApplyReq struct {
	Path      string `json:"path"`
	CheckOnly bool   `json:"check_only,omitempty"`
}

// StacksDirCheck is the agent's check of a stacks directory, with the same
// rules its root updater applies. Status is ok, warn or fail; Issue names
// the first problem.
type StacksDirCheck struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Issue      string `json:"issue,omitempty"`
	Message    string `json:"message"`
	WillCreate bool   `json:"will_create,omitempty"`
	FSType     string `json:"fs_type,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	FreeBytes  int64  `json:"free_bytes,omitempty"`
}

// Outcomes of a StacksDirApplyRes.
const (
	StacksDirChecked = "checked"
	StacksDirRefused = "refused"
	StacksDirDone    = "done"
	StacksDirFailed  = "failed"
	// StacksDirPending: the root updater took the request but had not
	// answered when the agent stopped waiting.
	StacksDirPending = "pending"
)

// StacksDirApplyRes answers a StacksDirApplyReq.
type StacksDirApplyRes struct {
	Status string         `json:"status"`
	Check  StacksDirCheck `json:"check"`
	Reason string         `json:"reason,omitempty"`
}

// StacksDirInfo is one stacks directory of an agent and how many stacks
// it holds.
type StacksDirInfo struct {
	Path    string `json:"path"`
	Current bool   `json:"current"`
	Stacks  int    `json:"stacks"`
}

// Install layouts an agent reports in its hello.
const (
	// LayoutLegacy: the binary lives in the data dir, owned by the
	// service account, and the agent replaces it itself.
	LayoutLegacy = "legacy"
	// LayoutManaged: the binary is root-owned and a root oneshot
	// (dockmesh-agent update apply) installs what the agent staged.
	LayoutManaged = "managed"
)

// AgentUpdateResult mirrors rootupdate.Result without importing it into
// the protocol.
type AgentUpdateResult struct {
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to,omitempty"`
	Step   string `json:"step,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// HelloLayout returns the agent's layout, legacy when it sent none.
func (h HelloPayload) HelloLayout() string {
	if h.Layout == LayoutManaged {
		return LayoutManaged
	}
	return LayoutLegacy
}

// maxStacksDirs bounds what an agent's hello may list: the unit makes at
// most three stacks dirs writable, the data dir takes the fourth slot.
const maxStacksDirs = 3

// maxStacksDirPath cuts a reported stacks dir path.
const maxStacksDirPath = 4096

// ReportedStacksDirs returns the stacks dirs of the hello, at most
// maxStacksDirs, with long paths cut, each path once, no negative counts
// and only the first entry as the current one.
func (h HelloPayload) ReportedStacksDirs() []StacksDirInfo {
	out := make([]StacksDirInfo, 0, min(len(h.StacksDirs), maxStacksDirs))
	for _, d := range h.StacksDirs {
		if len(out) == maxStacksDirs {
			break
		}
		if len(d.Path) > maxStacksDirPath {
			d.Path = strings.ToValidUTF8(d.Path[:maxStacksDirPath], "")
		}
		if slices.ContainsFunc(out, func(o StacksDirInfo) bool { return o.Path == d.Path }) {
			continue
		}
		d.Stacks = max(d.Stacks, 0)
		d.Current = d.Current && len(out) == 0
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Has reports whether the agent advertised capability c.
func (h HelloPayload) Has(c string) bool {
	for _, x := range h.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// WelcomePayload is the server's reply confirming the agent is registered.
type WelcomePayload struct {
	AgentID         string   `json:"agent_id"`
	AgentName       string   `json:"agent_name"`
	ServerVersion   string   `json:"server_version"`
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
}

// CertRenewReq carries a PEM CSR for a new client key generated on the
// agent. The server ignores the CSR subject and keeps the host ID.
type CertRenewReq struct {
	CSR string `json:"csr"`
}

// CertRenewRes is the renewed client cert.
type CertRenewRes struct {
	ClientCert string `json:"client_cert"` // PEM
}

// HeartbeatPayload is sent from the agent on a fixed interval (15s).
type HeartbeatPayload struct {
	TS             int64 `json:"ts"`
	ContainerCount int32 `json:"container_count"`
}

// PingPayload is the server's reverse healthcheck.
type PingPayload struct {
	TS int64 `json:"ts"`
}

// EnrollRequest is POSTed to /enroll on the agent listener by an
// unauthenticated agent that wants to swap a one-time token for a client cert.
type EnrollRequest struct {
	Token string `json:"token"`
	// Hostinfo so the server can populate the row on first enrol.
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Version       string `json:"version"`
	DockerVersion string `json:"docker_version,omitempty"`
}

// EnrollResponse contains the freshly-signed client cert + the CA cert
// the agent must trust to validate the server.
type EnrollResponse struct {
	AgentID    string `json:"agent_id"`
	AgentName  string `json:"agent_name"`
	ClientCert string `json:"client_cert"` // PEM
	ClientKey  string `json:"client_key"`  // PEM
	CACert     string `json:"ca_cert"`     // PEM
	AgentURL   string `json:"agent_url"`   // wss URL the agent should dial
}
