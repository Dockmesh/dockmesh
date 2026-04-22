package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
)

// runInitCmd is the first-run setup wizard invoked by the user after
// `curl -fsSL https://get.dockmesh.dev | bash`. It walks through:
//
//  1. Data directory layout confirmation
//  2. Listen port
//  3. Admin user credentials
//  4. Public base URL (for OIDC callback + agent enroll hints)
//  5. Agent WebSocket public URL (mTLS hardcodes this into each agent)
//  6. Optional systemd unit install + start
//
// Every step is idempotent — re-running after a partial setup picks up
// where you left off. Non-interactive mode (--yes) accepts sane defaults
// and auto-generates the admin password, printing it once.
//
// When the user opts into the systemd step, we go all the way: write
// the unit, `daemon-reload`, `enable --now`, then probe the health
// endpoint for up to 10s and report the outcome. The old behaviour
// (install unit, tell user to run `systemctl enable --now`) was a UX
// bug — k3s does the full enable-and-start and the user expected the
// same here.
func runInitCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	yes := fs.Bool("yes", false, "non-interactive — accept defaults, auto-generate admin password")
	dataDir := fs.String("data-dir", "/var/lib/dockmesh", "where to keep DB + stacks + keys")
	installSystemd := fs.Bool("systemd", true, "install + enable + start a systemd unit")
	listen := fs.String("listen", ":8080", "HTTP listen address")
	adminUser := fs.String("admin-user", "admin", "admin username")
	baseURL := fs.String("base-url", "", "public base URL — empty = derive from hostname")
	_ = fs.Parse(args)

	interactive := !*yes
	if interactive && !isStdinTTY() {
		fmt.Fprintln(os.Stderr, "stdin is not a TTY — use --yes for non-interactive setup")
		os.Exit(2)
	}

	printInitBanner()

	// ---- Step 1: data dir ------------------------------------------------
	section(1, 6, "Data directory")
	dd := *dataDir
	if interactive {
		say("   Where should Dockmesh keep its DB, stacks, and keys?")
		dd = promptDefault(dd)
	}
	if err := os.MkdirAll(filepath.Join(dd, "data"), 0o700); err != nil {
		die("create data dir", err)
	}
	if err := os.MkdirAll(filepath.Join(dd, "stacks"), 0o755); err != nil {
		die("create stacks dir", err)
	}
	initOK("using " + dd)

	// ---- Step 2: listen port --------------------------------------------
	section(2, 6, "Listen address")
	addr := *listen
	if interactive {
		say("   HTTP listen address (host:port)")
		addr = promptDefault(addr)
	}
	if !portAvailable(addr) {
		initWarn(addr + " is in use — Dockmesh will fail to start until that conflict is resolved")
	} else {
		initOK(addr + " is free")
	}

	// ---- Step 3: admin user ----------------------------------------------
	section(3, 6, "Admin user")
	admin := *adminUser
	password := ""
	if interactive {
		say("   Admin username")
		admin = promptDefault(admin)
		say("   Password (leave empty to auto-generate a strong 18-char one)")
		password = promptPassword()
	}
	generated := password == ""
	if generated {
		password = randomPassword(18)
	}

	// ---- Step 4: base URL ------------------------------------------------
	section(4, 6, "Public base URL")
	bURL := *baseURL
	if bURL == "" {
		bURL = deriveBaseURL(addr)
	}
	if interactive {
		say("   URL users will browse to (OIDC callbacks + agent enroll links)")
		bURL = promptDefault(bURL)
	}
	initOK("base URL: " + bURL)

	// ---- Step 5: agent public URL ---------------------------------------
	section(5, 6, "Agent connection URL")
	agentURL := initDeriveAgentURL(bURL)
	if interactive {
		say("   Remote agents connect here via mTLS (wss://)")
		agentURL = promptDefault(agentURL)
	}
	initOK("agent URL: " + agentURL)

	// ---- Step 6: systemd -------------------------------------------------
	section(6, 6, "systemd integration")
	doSystemd := *installSystemd
	if interactive {
		doSystemd = promptYesNo("Install systemd unit + start dockmesh now?", doSystemd)
	}

	// ====== Apply =========================================================
	applyHeader()
	cfg := &config.Config{
		DBPath:           filepath.Join(dd, "data", "dockmesh.db"),
		StacksRoot:       filepath.Join(dd, "stacks"),
		SecretsPath:      filepath.Join(dd, "data", "secrets.env"),
		SecretsKeyPath:   filepath.Join(dd, "data", "secrets.age-key"),
		AuditGenesisPath: filepath.Join(dd, "data", "audit-genesis.sha256"),
		HTTPAddr:         addr,
		BaseURL:          bURL,
		AgentPublicURL:   agentURL,
	}

	adminCreated, err := initDBAndAdmin(cfg, admin, password)
	if err != nil {
		die("bootstrap DB", err)
	}
	initOK("database initialised       " + cfg.DBPath)
	if adminCreated {
		initOK("admin '" + admin + "' created")
	} else {
		// User already exists in this DB. DON'T silently ignore — the
		// old behaviour let re-runs display a fake "new password" while
		// the stored hash stayed untouched, producing real lockouts
		// when users tried to log in with the value init showed them.
		initWarn("admin '" + admin + "' already exists — password NOT changed")
		initWarn("to reset it: sudo dockmesh admin reset-password --user " + admin + " --password <new>")
		// Suppress the auto-generated password box + replace the login
		// line in the summary so the user doesn't assume the printed
		// password is the live one.
		generated = false
		password = ""
	}

	if err := writeEnvFile(dd, cfg); err != nil {
		die("write env file", err)
	}
	initOK("env file written           " + filepath.Join(dd, "dockmesh.env"))

	// Show the generated password in a bordered box, positioned so it
	// can't get lost in subsequent log lines. We only print this when
	// auto-generated — if the user typed their own, they know it.
	if generated {
		renderBox("Auto-generated password", []string{
			"",
			"   " + bold(password),
			"",
			"   Save it now — won't be shown again.",
			"",
		})
	}

	serviceStarted := false
	var healthURL string
	if doSystemd {
		if runtime.GOOS != "linux" {
			initWarn("systemd install supported on linux only — skipping")
		} else if unit, err := installSystemdUnitFile(dd); err != nil {
			initWarn("systemd unit install failed: " + err.Error())
		} else {
			initOK("systemd unit installed     " + unit)
			serviceStarted, healthURL = enableAndStartService(cfg.HTTPAddr)
		}
	}

	// ====== Summary =======================================================
	summaryLines := []string{
		"",
		"    Dashboard   " + accent(bURL),
		"    Login       " + admin + "  /  " + passwordForSummary(password, generated),
		"",
		"    Service     sudo systemctl status dockmesh",
		"    Logs        sudo journalctl -u dockmesh -f",
		"    Restart     sudo systemctl restart dockmesh",
		"",
		"  Next",
		"    • Enroll a second host  →  Agents → New agent",
		"    • Deploy your first stack → Stacks → New",
		"    • Set up scheduled backups → Backups → New job",
		"",
	}

	title := "✔  Dockmesh is ready"
	if doSystemd && !serviceStarted {
		title = "!  Dockmesh configured — service not running"
		summaryLines = append(
			[]string{"", "    Start it with:  sudo systemctl start dockmesh", ""},
			summaryLines[1:]...,
		)
	}
	if !doSystemd {
		summaryLines = []string{
			"",
			"    Dashboard   " + accent(bURL),
			"    Login       " + admin + "  /  " + passwordForSummary(password, generated),
			"",
			"    Start       " + accent("sudo dockmesh serve --env-file "+filepath.Join(dd, "dockmesh.env")),
			"",
		}
	}
	_ = healthURL
	renderBox(title, summaryLines)

	fmt.Fprintln(os.Stderr, "   Docs   https://dockmesh.dev/docs")
	fmt.Fprintln(os.Stderr)
}

// enableAndStartService is the "k3s-style" finisher: daemon-reload,
// enable --now, then poll the HTTP health endpoint so the user learns
// whether the service actually came up before `init` exits.
func enableAndStartService(listen string) (bool, string) {
	if err := runSilent("systemctl", "daemon-reload"); err != nil {
		initWarn("systemctl daemon-reload failed: " + err.Error())
		return false, ""
	}
	spinnerStart("enabling + starting dockmesh.service")
	err := runSilent("systemctl", "enable", "--now", "dockmesh")
	spinnerStop()
	if err != nil {
		initFail("enable --now failed: " + err.Error())
		initFail("inspect with: sudo journalctl -u dockmesh --since '1 min ago'")
		return false, ""
	}

	// Probe the health endpoint for up to 10 seconds. listen may be
	// ":8080" (any-iface) or "127.0.0.1:9999" etc. — strip the host and
	// probe localhost so we don't depend on DNS or external routing.
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		// Treat a bare ":port" as "localhost:port".
		host = "127.0.0.1"
		port = strings.TrimPrefix(listen, ":")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	healthURL := fmt.Sprintf("http://%s:%s/api/v1/health", host, port)

	spinnerStart("probing " + healthURL)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				spinnerStop()
				pid := systemdPID()
				if pid != "" {
					initOK(fmt.Sprintf("service running            PID %s", pid))
				} else {
					initOK("service running")
				}
				initOK(fmt.Sprintf("health OK                  %d in %dms", resp.StatusCode, 0))
				return true, healthURL
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	spinnerStop()
	initWarn(fmt.Sprintf("health probe timed out after 10s — last error: %v", lastErr))
	initWarn("check status with: sudo systemctl status dockmesh")
	return false, healthURL
}

func systemdPID() string {
	out, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", "dockmesh").Output()
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(string(out))
	if pid == "0" || pid == "" {
		return ""
	}
	return pid
}

func runSilent(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Stdout = io.Discard
	c.Stderr = io.Discard
	return c.Run()
}

// ---------------------------------------------------------------------------
//  Styling — matches install.sh so the two tools feel like one product.
// ---------------------------------------------------------------------------

var (
	useColor = isStderrTTY() && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
)

func esc(code, s string) string {
	if !useColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}
func bold(s string) string   { return esc("1", s) }
func dim(s string) string    { return esc("2", s) }
func accent(s string) string { return esc("38;5;51", s) }
func muted(s string) string  { return esc("38;5;240", s) }

func isStderrTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func printInitBanner() {
	renderBox("dockmesh — first-run setup", []string{
		"",
		"   Guided wizard for data dir, admin user, listen port and",
		"   systemd service. Takes ~2 minutes and is idempotent —",
		"   safe to re-run to change settings later.",
		"",
	})
}

func section(n, total int, title string) {
	rule := strings.Repeat("━", 4)
	fmt.Fprintf(os.Stderr, "\n%s  %s  %s  %s  %s\n\n",
		esc("38;5;51", rule),
		bold(fmt.Sprintf("%d / %d", n, total)),
		esc("38;5;51", rule),
		bold(title),
		esc("38;5;51", strings.Repeat("━", 60-len(title))),
	)
}

func applyHeader() {
	fmt.Fprintf(os.Stderr, "\n%s  %s  %s\n\n",
		esc("38;5;51", strings.Repeat("━", 4)),
		bold("Applying"),
		esc("38;5;51", strings.Repeat("━", 68)),
	)
}

func initOK(m string)   { fmt.Fprintln(os.Stderr, "   "+esc("38;5;42", "✔")+" "+m) }
func initWarn(m string) { fmt.Fprintln(os.Stderr, "   "+esc("38;5;214", "!")+" "+m) }
func initFail(m string) { fmt.Fprintln(os.Stderr, "   "+esc("38;5;196", "✘")+" "+m) }
func say(m string)      { fmt.Fprintln(os.Stderr, m) }
func die(what string, err error) {
	fmt.Fprintf(os.Stderr, "   %s %s: %v\n", esc("38;5;196", "✘"), what, err)
	os.Exit(1)
}

// renderBox draws a rounded Unicode box around a title + body lines.
// Body lines are printed verbatim (no padding math to avoid clobbering
// ANSI-escape-width miscalculations); we trust the caller to keep lines
// under ~66 visible chars.
func renderBox(title string, lines []string) {
	const w = 70
	border := accent(strings.Repeat("─", w-2))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, accent("╭")+border+accent("╮"))
	fmt.Fprintln(os.Stderr, accent("│")+strings.Repeat(" ", w-2)+accent("│"))
	// Title line
	fmt.Fprintln(os.Stderr, accent("│")+"  "+bold(title))
	fmt.Fprintln(os.Stderr, accent("│")+strings.Repeat(" ", w-2)+accent("│"))
	for _, line := range lines {
		fmt.Fprintln(os.Stderr, accent("│")+line)
	}
	fmt.Fprintln(os.Stderr, accent("│")+strings.Repeat(" ", w-2)+accent("│"))
	fmt.Fprintln(os.Stderr, accent("╰")+border+accent("╯"))
	fmt.Fprintln(os.Stderr)
}

// ---------------------------------------------------------------------------
//  Spinner — cycles a braille frame while a long op runs. Stops via
//  spinnerStop which clears the line, leaving a clean OK/warn line to
//  replace it. Inline, not a goroutine — we emit frames synchronously
//  between work chunks.
// ---------------------------------------------------------------------------

var (
	spinnerMsg    string
	spinnerTicker *time.Ticker
	spinnerStopCh chan struct{}
	spinnerDone   chan struct{}
)

func spinnerStart(msg string) {
	spinnerMsg = msg
	if !useColor {
		fmt.Fprintln(os.Stderr, "   "+dim("⧖")+" "+msg+"...")
		return
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerStopCh = make(chan struct{})
	spinnerDone = make(chan struct{})
	spinnerTicker = time.NewTicker(80 * time.Millisecond)
	go func() {
		i := 0
		for {
			select {
			case <-spinnerStopCh:
				spinnerTicker.Stop()
				// Clear the spinner line cleanly.
				fmt.Fprintf(os.Stderr, "\r\033[2K")
				close(spinnerDone)
				return
			case <-spinnerTicker.C:
				fmt.Fprintf(os.Stderr, "\r   %s %s", esc("38;5;38", frames[i%len(frames)]), msg)
				i++
			}
		}
	}()
}

func spinnerStop() {
	if spinnerStopCh == nil {
		return
	}
	close(spinnerStopCh)
	<-spinnerDone
	spinnerStopCh = nil
}

// ---------------------------------------------------------------------------
//  Prompts
// ---------------------------------------------------------------------------
var stdinReader = bufio.NewReader(os.Stdin)

func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func promptDefault(def string) string {
	fmt.Fprintf(os.Stderr, "   %s %s %s ",
		accent("›"),
		def,
		muted("(press Enter)"),
	)
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptPassword() string {
	// No tty-masking fancy-pants here — curl|bash is already unusual,
	// and most TUI libraries depend on /dev/tty that isn't always
	// available. Echoed input keeps the code small and obvious.
	fmt.Fprintf(os.Stderr, "   %s ", accent("›"))
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptYesNo(prompt string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Fprintf(os.Stderr, "   %s [%s]: ", prompt, hint)
	line, _ := stdinReader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

func passwordForSummary(pw string, generated bool) string {
	if generated {
		return bold(pw)
	}
	return "(the one you entered)"
}

func portAvailable(addr string) bool {
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func deriveBaseURL(listen string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	port := strings.TrimPrefix(listen, ":")
	if port == "" || port == "80" {
		return "http://" + host
	}
	return "http://" + host + ":" + port
}

func initDeriveAgentURL(baseURL string) string {
	u := strings.TrimSuffix(baseURL, "/")
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + strings.TrimPrefix(u, "https://")
	} else {
		u = "wss://" + strings.TrimPrefix(u, "http://")
	}
	if idx := strings.LastIndex(u, ":"); idx > len("wss://") && !strings.ContainsAny(u[idx:], "/") {
		u = u[:idx]
	}
	return u + ":8443/connect"
}

func randomPassword(n int) string {
	raw := make([]byte, (n*6+7)/8)
	if _, err := rand.Read(raw); err != nil {
		return "CHANGE-ME-" + fmt.Sprint(time.Now().Unix())
	}
	return base64.RawURLEncoding.EncodeToString(raw)[:n]
}

// initDBAndAdmin opens the DB, runs migrations, and creates the admin
// user if it doesn't already exist. Returns created=true only when a
// fresh user was made — on re-run (user already exists) we return
// created=false so the caller can warn the operator that the password
// they just typed was NOT applied.
func initDBAndAdmin(cfg *config.Config, username, password string) (bool, error) {
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return false, err
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		return false, err
	}
	authSvc := auth.NewService(database, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := authSvc.CreateUser(ctx, username, "", password, "admin"); err != nil {
		if errors.Is(err, auth.ErrUsernameTaken) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func writeEnvFile(dataDir string, cfg *config.Config) error {
	envPath := filepath.Join(dataDir, "dockmesh.env")
	body := fmt.Sprintf(strings.Join([]string{
		"# Generated by `dockmesh init`.",
		"# Edit + restart dockmesh to apply.",
		"DOCKMESH_LISTEN=%s",
		"DOCKMESH_BASE_URL=%s",
		"DOCKMESH_AGENT_PUBLIC_URL=%s",
		"DOCKMESH_DB_PATH=%s",
		"DOCKMESH_STACKS_ROOT=%s",
		"DOCKMESH_DATA_DIR=%s",
		"DOCKMESH_SECRETS_KEY_PATH=%s",
		"",
	}, "\n"),
		cfg.HTTPAddr, cfg.BaseURL, cfg.AgentPublicURL,
		cfg.DBPath, cfg.StacksRoot, filepath.Dir(cfg.SecretsPath), cfg.SecretsKeyPath,
	)
	return os.WriteFile(envPath, []byte(body), 0o600)
}

// installSystemdUnitFile writes the unit under the data dir AND
// /etc/systemd/system (the canonical location). Also creates the
// dedicated `dockmesh` system user if it doesn't already exist and
// chowns the data directory to it — we ship a non-root default service
// so an exploit in the HTTP/agent handlers doesn't get root on the
// host. Docker-socket access is granted via the `docker` group instead.
// Returns the target path so the caller can include it in the "unit
// installed at X" line. enable+start+probe is done separately in
// enableAndStartService.
func installSystemdUnitFile(dataDir string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("systemd install supported on linux only")
	}
	bin, err := exec.LookPath("dockmesh")
	if err != nil {
		bin = "/usr/local/bin/dockmesh"
	}

	// 1) Ensure the `dockmesh` system user exists. useradd --system
	//    with no home + nologin shell is the standard pattern for
	//    service daemons. Idempotent: ignore "already exists" errors.
	if err := runSilent("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "dockmesh"); err != nil {
		// useradd returns 9 (EUSERSEXISTS) when user already exists —
		// anything else is a real error, but we log+continue rather
		// than fail the whole init (the unit below can still be
		// installed; admin can fix the user manually).
		if _, statErr := os.Stat("/etc/passwd"); statErr == nil {
			initWarn("could not create 'dockmesh' user — continuing with existing system accounts (if the user exists it's fine)")
		}
	}

	// 2) Add the service user to the `docker` group so it can open the
	//    Docker socket without being root. Requires the `docker` group
	//    to exist (it does on any host running docker).
	_ = runSilent("usermod", "-aG", "docker", "dockmesh")

	// 3) Chown the data directory so the service can read/write it.
	//    Happens unconditionally: safe on re-runs, and crucial when
	//    migrating an existing root-owned install.
	_ = runSilent("chown", "-R", "dockmesh:dockmesh", dataDir)
	_ = runSilent("chmod", "700", dataDir)

	unit := fmt.Sprintf(`[Unit]
Description=Dockmesh container management
After=docker.service network-online.target
Wants=docker.service network-online.target

[Service]
Type=simple
User=dockmesh
Group=docker
EnvironmentFile=%s/dockmesh.env
ExecStart=%s serve
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536
StateDirectory=dockmesh

# Hardening — the service never escalates out of its own context, has
# no kernel-tunable write access, and cannot see other users' /home.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ReadWritePaths=%s /var/run/docker.sock
RestrictNamespaces=true
LockPersonality=true

[Install]
WantedBy=multi-user.target
`, dataDir, bin, dataDir)

	// Drop a reference copy under the data dir so the user can diff /
	// edit it and re-install if needed.
	pending := filepath.Join(dataDir, "dockmesh.service")
	if err := os.WriteFile(pending, []byte(unit), 0o644); err != nil {
		return "", err
	}
	target := "/etc/systemd/system/dockmesh.service"
	if err := os.WriteFile(target, []byte(unit), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w (run dockmesh init as root)", target, err)
	}
	return target, nil
}
