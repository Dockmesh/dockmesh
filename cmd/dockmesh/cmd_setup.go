package main

import (
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// runSetupCmd handles `dockmesh setup <subcommand>`.
func runSetupCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh setup <new-code|status>")
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "materialize":
		err = runSetupMaterialize(args[1:])
	case "new-code":
		err = runSetupNewCode(args[1:])
	case "status":
		runSetupStatus(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: dockmesh setup <new-code|status>")
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, errSetUpAlready) {
			fmt.Fprintln(os.Stderr, "This server is already set up. To add an admin, run: "+setupAdminCommand)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func runSetupNewCode(args []string) error {
	fset := flag.NewFlagSet("setup new-code", flag.ExitOnError)
	porcelain := fset.Bool("porcelain", false, "print key=value lines for install.sh")
	_ = fset.Parse(args)
	c, err := setupNewCode(time.Now())
	if err != nil {
		return err
	}
	if *porcelain {
		c.writePorcelain(os.Stdout)
	} else {
		c.writeText(os.Stdout)
	}
	return nil
}

var errSetUpAlready = errors.New("server already set up")

const setupAdminCommand = "sudo dockmesh admin create --username <name> --role admin"

// newCode is what `setup new-code` prints.
type newCode struct {
	Code        string
	ValidUntil  time.Time
	URL         string
	Fingerprint string
	Replaced    bool
}

func (c newCode) writeText(w io.Writer) {
	fmt.Fprintf(w, "Setup code   %s\n", c.Code)
	fmt.Fprintf(w, "Valid until  %s (2 hours)\n", c.ValidUntil.Local().Format("15:04"))
	if c.Replaced {
		fmt.Fprintln(w, "The previous code no longer works.")
	}
	fmt.Fprintf(w, "Open %s\n", c.URL)
	if c.Fingerprint != "" {
		fmt.Fprintf(w, "Certificate  SHA-256 %s\n", c.Fingerprint)
		fmt.Fprintln(w, "Your browser warns about this certificate; continue only if it shows this fingerprint.")
	}
}

// writePorcelain prints one key=value per line, the form install.sh reads.
func (c newCode) writePorcelain(w io.Writer) {
	fmt.Fprintf(w, "code=%s\nvalid_until=%s\nurl=%s\nfingerprint=%s\nreplaced=%t\n",
		c.Code, c.ValidUntil.Local().Format("15:04"), c.URL, c.Fingerprint, c.Replaced)
}

// setupProbe gathers the setup decision's facts for this host.
func setupProbe() (setup.Facts, config.Paths) {
	paths := config.ResolvePaths(os.Getenv)
	facts := setup.Probe{
		ConfigFiles: []string{loadedEnvFile, unitEnvFile()},
		DBPathSet:   os.Getenv("DOCKMESH_DB_PATH") != "",
		Force:       os.Getenv("DOCKMESH_SETUP_FORCE") == "true",
		Paths:       paths,
	}.Scan()
	facts.Users = countUsersReadOnly(paths.DBPath, facts.DB)
	return facts, paths
}

func serverHTTPAddr() string {
	if a := os.Getenv("DOCKMESH_HTTP_ADDR"); a != "" {
		return a
	}
	return ":8080"
}

func setupNewCode(now time.Time) (newCode, error) {
	if runtime.GOOS == "windows" {
		return newCode{}, errors.New("setup codes are only supported on Linux and macOS")
	}
	if os.Geteuid() != 0 {
		return newCode{}, errors.New("dockmesh setup new-code must run as root (" + setup.CodeCommand + ")")
	}
	facts, paths := setupProbe()
	httpAddr := serverHTTPAddr()
	webCert, err := pki.ReadUICert(setup.PKIDir(paths.DataDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return newCode{}, fmt.Errorf("setup certificate: %w", err)
	}
	if !setup.CodeAllowed(facts, func() bool { return serverInSetup(httpAddr, webCert) }) {
		return newCode{}, errSetUpAlready
	}

	code, err := setup.NewCode()
	if err != nil {
		return newCode{}, err
	}
	rec, err := setup.NewCodeRecord(code, now)
	if err != nil {
		return newCode{}, err
	}
	_, statErr := os.Lstat(setup.CodeFile)
	if err := setup.WriteCodeRecord(setup.CodeFile, rec, codeGroup(paths.DataDir)); err != nil {
		return newCode{}, fmt.Errorf("write %s: %w", setup.CodeFile, err)
	}
	c := newCode{
		Code:       setup.FormatCode(code),
		ValidUntil: rec.ExpiresAt,
		Replaced:   statErr == nil,
	}
	c.URL = setupBaseURL(httpAddr, webCert != nil) + "/setup#" + c.Code
	if webCert != nil {
		c.Fingerprint = pki.Fingerprint(webCert.Raw)
	}
	return c, nil
}

// runSetupStatus is `dockmesh setup status`: whether this server still
// waits for its setup, and where the wizard is. It writes nothing; the
// installer asks it after an upgrade, so it never has to print a code.
func runSetupStatus(args []string) {
	fset := flag.NewFlagSet("setup status", flag.ExitOnError)
	porcelain := fset.Bool("porcelain", false, "print key=value lines for install.sh")
	_ = fset.Parse(args)
	facts, paths := setupProbe()
	httpAddr := serverHTTPAddr()
	webCert, _ := pki.ReadUICert(setup.PKIDir(paths.DataDir))
	unfinished := setup.CodeAllowed(facts, func() bool { return serverInSetup(httpAddr, webCert) })
	url := setupBaseURL(httpAddr, webCert != nil) + "/setup"
	switch {
	case *porcelain && unfinished:
		fmt.Printf("setup=unfinished\nurl=%s\n", url)
	case *porcelain:
		fmt.Println("setup=done")
	case unfinished:
		fmt.Printf("Setup is not finished. Get a setup code with: %s\nThen open %s\n", setup.CodeCommand, url)
	default:
		fmt.Println("This server is set up.")
	}
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

// serverInSetup asks the server on this host whether it runs in setup
// mode: over HTTPS pinned to webCert when setup mode has a certificate,
// else over plain HTTP.
func serverInSetup(httpAddr string, webCert *x509.Certificate) bool {
	if _, _, err := net.SplitHostPort(httpAddr); err != nil {
		return false
	}
	base, client := selfProbe(httpAddr, webCert)
	resp, err := client.Get(base + "/api/v1/setup/status") // #nosec G704 -- this host's own listen address from the server config
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
// base URL, else this host's first LAN address on the web port, with the
// scheme setup mode serves.
func setupBaseURL(httpAddr string, https bool) string {
	scheme := "http://"
	if https {
		scheme = "https://"
	}
	if u := os.Getenv("DOCKMESH_BASE_URL"); u != "" && u != config.DefaultBaseURL {
		return scheme + strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(u, "/"), "https://"), "http://")
	}
	_, port, err := net.SplitHostPort(httpAddr)
	if err != nil || port == "" {
		port = "8080"
	}
	return scheme + net.JoinHostPort(lanAddress(), port)
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
