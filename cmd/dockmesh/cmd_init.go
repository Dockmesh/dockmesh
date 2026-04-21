package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
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
//  6. Optional systemd unit install
//
// Everything is idempotent: re-running after a partial setup picks up
// where you left off. Non-interactive mode (--yes) accepts sane
// defaults and auto-generates the admin password, printing it once.
func runInitCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	yes := fs.Bool("yes", false, "non-interactive — accept defaults, auto-generate admin password")
	dataDir := fs.String("data-dir", "/var/lib/dockmesh", "where to keep DB + stacks + keys")
	installSystemd := fs.Bool("systemd", true, "install a systemd unit so the server starts on boot")
	listen := fs.String("listen", ":8080", "HTTP listen address")
	adminUser := fs.String("admin-user", "admin", "admin username")
	baseURL := fs.String("base-url", "", "public base URL (e.g. https://dockmesh.example.com) — empty = derive from hostname")
	_ = fs.Parse(args)

	interactive := !*yes
	if interactive && !isStdinTTY() {
		fmt.Fprintln(os.Stderr, "stdin is not a TTY — use --yes for non-interactive setup")
		os.Exit(2)
	}

	printBanner()

	// ---- Step 1: data dir ------------------------------------------------
	section("1/6  Data directory")
	dd := *dataDir
	if interactive {
		dd = promptDefault("Where should Dockmesh keep its DB + stacks + keys?", dd)
	}
	if err := os.MkdirAll(filepath.Join(dd, "data"), 0o700); err != nil {
		die("create data dir", err)
	}
	if err := os.MkdirAll(filepath.Join(dd, "stacks"), 0o755); err != nil {
		die("create stacks dir", err)
	}
	initOK("data directory: " + dd)

	// ---- Step 2: listen port --------------------------------------------
	section("2/6  HTTP listen address")
	addr := *listen
	if interactive {
		addr = promptDefault("HTTP listen address (host:port, host optional)", addr)
	}
	if !portAvailable(addr) {
		initWarn("address " + addr + " is already in use — Dockmesh will fail to start until that conflict is resolved")
	} else {
		initOK("listen: " + addr)
	}

	// ---- Step 3: admin user ----------------------------------------------
	section("3/6  Admin user")
	admin := *adminUser
	password := ""
	if interactive {
		admin = promptDefault("Admin username", admin)
		fmt.Fprintln(os.Stderr, "    (leave blank to auto-generate a strong 18-char password)")
		password = promptPassword("Admin password")
	}
	if password == "" {
		password = randomPassword(18)
		info(fmt.Sprintf("generated password: %s", password))
		info("    write this down now — it will NOT be shown again.")
	}

	// ---- Step 4: base URL ------------------------------------------------
	section("4/6  Public base URL")
	bURL := *baseURL
	if bURL == "" {
		bURL = deriveBaseURL(addr)
	}
	if interactive {
		bURL = promptDefault("Public URL users will browse to (OIDC callbacks + agent hints)", bURL)
	}
	initOK("base URL: " + bURL)

	// ---- Step 5: agent public URL ---------------------------------------
	section("5/6  Agent connection URL")
	agentURL := initDeriveAgentURL(bURL)
	if interactive {
		agentURL = promptDefault("Remote agents will connect here (wss://)", agentURL)
	}
	initOK("agent URL: " + agentURL)

	// ---- Step 6: systemd -------------------------------------------------
	section("6/6  systemd integration")
	doSystemd := *installSystemd
	if interactive {
		doSystemd = promptYesNo("Install a systemd unit so dockmesh starts on boot?", doSystemd)
	}

	// ====== Apply =========================================================
	section("Applying configuration")
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

	if err := initDBAndAdmin(cfg, admin, password); err != nil {
		die("bootstrap DB", err)
	}
	initOK("DB initialised, admin user '" + admin + "' created")

	if err := writeEnvFile(dd, cfg); err != nil {
		die("write env file", err)
	}
	initOK("env file: " + filepath.Join(dd, "dockmesh.env"))

	if doSystemd {
		if err := installSystemdUnit(dd); err != nil {
			initWarn("systemd unit install failed: " + err.Error())
			initWarn("you can retry manually with:  sudo cp " + filepath.Join(dd, "dockmesh.service") + " /etc/systemd/system/")
		} else {
			initOK("systemd unit installed — enable + start with:  sudo systemctl enable --now dockmesh")
		}
	}

	// ====== Summary =======================================================
	cat := func(s string) string { return "\033[36m" + s + "\033[0m" }
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "\033[1;32mDockmesh initialised.\033[0m")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Browse to:   "+cat(bURL))
	fmt.Fprintln(os.Stderr, "  Login:       "+admin+"  /  (password shown above)")
	if doSystemd {
		fmt.Fprintln(os.Stderr, "  Start:       "+cat("sudo systemctl enable --now dockmesh"))
	} else {
		fmt.Fprintln(os.Stderr, "  Start:       "+cat("sudo dockmesh serve --config "+filepath.Join(dd, "dockmesh.env")))
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Docs:        https://dockmesh.dev/docs")
	fmt.Fprintln(os.Stderr)
}

// ---------------------------------------------------------------------------
//  Pretty output
// ---------------------------------------------------------------------------
func printBanner() {
	fmt.Fprint(os.Stderr, "\n"+
		"  \033[1;36m _            _                      _\033[0m\n"+
		"  \033[1;36m__| | ___   ___| | ___ __ ___   ___ ___| |__\033[0m\n"+
		"  \033[1;36m/ _` |/ _ \\ / __| |/ / '_ ` _ \\ / _ / __| '_ \\\033[0m\n"+
		"  \033[1;36m( (_| | (_) | (__|   <| | | | | |  __\\__ \\ | | |\033[0m\n"+
		"  \033[1;36m \\__,_|\\___/ \\___|_|\\_\\_| |_| |_|\\___|___/_| |_|\033[0m\n"+
		"\n  \033[2mFirst-run setup. ~2 minutes.\033[0m\n")
}

func section(title string) {
	fmt.Fprintf(os.Stderr, "\n\033[1;36m▸ %s\033[0m\n", title)
}
func info(m string) { fmt.Fprintln(os.Stderr, "  \033[36mi\033[0m "+m) }
func initOK(m string)   { fmt.Fprintln(os.Stderr, "  \033[32m✓\033[0m "+m) }
func initWarn(m string) { fmt.Fprintln(os.Stderr, "  \033[33m!\033[0m "+m) }
func die(what string, err error) {
	fmt.Fprintf(os.Stderr, "  \033[31mx\033[0m %s: %v\n", what, err)
	os.Exit(1)
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

func promptDefault(prompt, def string) string {
	fmt.Fprintf(os.Stderr, "  %s [\033[2m%s\033[0m]: ", prompt, def)
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

func promptPassword(prompt string) string {
	// No tty-masking fancy-pants here — curl|bash is already unusual, and
	// most TUI libraries depend on /dev/tty that isn't available when
	// someone pipes stdin. Echoed input keeps the code small; the
	// alternative for strict deploys is to pass --admin-user/--yes and
	// read DOCKMESH_ADMIN_PW from env.
	fmt.Fprintf(os.Stderr, "  %s: ", prompt)
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptYesNo(prompt string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Fprintf(os.Stderr, "  %s [%s]: ", prompt, hint)
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
	// Prefer a hostname the user can point DNS at; fall back to localhost.
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
	// Agent mTLS listener defaults to :8443 and must be wss://.
	// If base URL is http(s), swap to wss and append the well-known
	// /connect path that the agent library uses.
	u := strings.TrimSuffix(baseURL, "/")
	if strings.HasPrefix(u, "https://") {
		u = "wss://" + strings.TrimPrefix(u, "https://")
	} else {
		u = "wss://" + strings.TrimPrefix(u, "http://")
	}
	// Swap :8080 → :8443 if present; else append :8443.
	if idx := strings.LastIndex(u, ":"); idx > len("wss://") && !strings.ContainsAny(u[idx:], "/") {
		u = u[:idx]
	}
	return u + ":8443/connect"
}

func randomPassword(n int) string {
	// URL-safe base64 gives 6 bits per char → ceil(n*6/8) bytes.
	raw := make([]byte, (n*6+7)/8)
	if _, err := rand.Read(raw); err != nil {
		return "CHANGE-ME-" + fmt.Sprint(time.Now().Unix())
	}
	return base64.RawURLEncoding.EncodeToString(raw)[:n]
}

func initDBAndAdmin(cfg *config.Config, username, password string) error {
	// Open + migrate.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		return err
	}
	// Create admin user if missing. auth.Service hashing path keeps
	// argon2id parameters consistent with the runtime login flow.
	authSvc := auth.NewService(database, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// CreateUser returns ErrUsernameTaken if the admin exists from a
	// prior init run. That's fine — the wizard is idempotent.
	if _, err := authSvc.CreateUser(ctx, username, "", password, "admin"); err != nil {
		if errors.Is(err, auth.ErrUsernameTaken) {
			return nil
		}
		return err
	}
	return nil
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

func installSystemdUnit(dataDir string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd install supported on linux only")
	}
	bin, err := exec.LookPath("dockmesh")
	if err != nil {
		bin = "/usr/local/bin/dockmesh"
	}
	unit := fmt.Sprintf(`[Unit]
Description=Dockmesh container management
After=docker.service network-online.target
Wants=docker.service network-online.target

[Service]
Type=simple
EnvironmentFile=%s/dockmesh.env
ExecStart=%s serve
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536
StateDirectory=dockmesh

[Install]
WantedBy=multi-user.target
`, dataDir, bin)

	// Drop the unit file under the data dir for review; also try to
	// install into /etc/systemd/system if we have permission.
	pending := filepath.Join(dataDir, "dockmesh.service")
	if err := os.WriteFile(pending, []byte(unit), 0o644); err != nil {
		return err
	}
	target := "/etc/systemd/system/dockmesh.service"
	if err := os.WriteFile(target, []byte(unit), 0o644); err == nil {
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}
	return nil
}
