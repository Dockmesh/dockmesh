package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// runSetupCmd handles `dockmesh setup <subcommand>`.
func runSetupCmd(args []string) {
	if len(args) == 0 || args[0] != "new-code" {
		fmt.Fprintln(os.Stderr, "usage: dockmesh setup new-code")
		os.Exit(2)
	}
	if err := setupNewCode(time.Now()); err != nil {
		if errors.Is(err, errSetUpAlready) {
			fmt.Fprintln(os.Stderr, "This server is already set up. To add an admin, run: "+setupAdminCommand)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

var errSetUpAlready = errors.New("server already set up")

const setupAdminCommand = "sudo dockmesh admin create --username <name> --role admin"

func setupNewCode(now time.Time) error {
	if runtime.GOOS == "windows" {
		return errors.New("setup codes are only supported on Linux and macOS")
	}
	if os.Geteuid() != 0 {
		return errors.New("dockmesh setup new-code must run as root (" + setup.CodeCommand + ")")
	}
	paths := config.ResolvePaths(os.Getenv)
	facts := setup.Probe{
		ConfigFiles: []string{loadedEnvFile, unitEnvFile()},
		DBPathSet:   os.Getenv("DOCKMESH_DB_PATH") != "",
		Force:       os.Getenv("DOCKMESH_SETUP_FORCE") == "true",
		Paths:       paths,
	}.Scan()
	facts.Users = countUsersReadOnly(paths.DBPath, facts.DB)
	httpAddr := os.Getenv("DOCKMESH_HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	if !setup.CodeAllowed(facts, func() bool { return serverInSetup(httpAddr) }) {
		return errSetUpAlready
	}

	code, err := setup.NewCode()
	if err != nil {
		return err
	}
	rec, err := setup.NewCodeRecord(code, now)
	if err != nil {
		return err
	}
	_, statErr := os.Lstat(setup.CodeFile)
	if err := setup.WriteCodeRecord(setup.CodeFile, rec, codeGroup(paths.DataDir)); err != nil {
		return fmt.Errorf("write %s: %w", setup.CodeFile, err)
	}
	shown := setup.FormatCode(code)
	fmt.Printf("Setup code   %s\n", shown)
	fmt.Printf("Valid until  %s (2 hours)\n", rec.ExpiresAt.Local().Format("15:04"))
	if statErr == nil {
		fmt.Println("The previous code no longer works.")
	}
	fmt.Printf("Open %s/setup#%s\n", setupBaseURL(httpAddr), shown)
	return nil
}

// countUsersReadOnly counts accounts without writing to the database: it
// belongs to the service user and root must not leave files behind. -1
// when it cannot tell.
func countUsersReadOnly(path string, exists bool) int {
	if !exists {
		return 0
	}
	conn, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return -1
	}
	defer conn.Close()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return -1
	}
	return n
}

// serverInSetup asks the server on this host whether it runs in setup mode.
func serverInSetup(httpAddr string) bool {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/setup/status") // #nosec G704 -- this host's own listen address from the server config
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Active bool `json:"active"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&st) == nil && st.Active
}

// setupBaseURL is where the operator opens the wizard: the configured
// base URL, else this host's first LAN address on the web port.
func setupBaseURL(httpAddr string) string {
	if u := os.Getenv("DOCKMESH_BASE_URL"); u != "" && u != config.DefaultBaseURL {
		return strings.TrimSuffix(u, "/")
	}
	_, port, err := net.SplitHostPort(httpAddr)
	if err != nil || port == "" {
		port = "8080"
	}
	return "http://" + net.JoinHostPort(lanAddress(), port)
}

func lanAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				return n.IP.String()
			}
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}
