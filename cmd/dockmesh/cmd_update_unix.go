//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/pkg/version"
)

const serverUnitName = "dockmesh.service"

// dbStepTimeout bounds the database snapshot and its restore, so a whole
// run including a rollback stays inside the unit's TimeoutStartSec.
const dbStepTimeout = 10 * time.Minute

func updateApply(args []string) error {
	fs := flag.NewFlagSet("update apply", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "data dir of the server (set by dockmesh-update.service)")
	_ = fs.Parse(args)
	if runtime.GOOS != "linux" {
		return errors.New("updates are applied by systemd on Linux only")
	}
	if os.Geteuid() != 0 {
		return errors.New("must run as root")
	}
	if !filepath.IsAbs(*dataDir) {
		return errors.New("--data-dir needs an absolute path")
	}
	uid, _, err := serviceAccount()
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return err
	}
	if err := checkUnitBinary(binary); err != nil {
		return err
	}
	envFile, err := unitConfigFile("")
	if err != nil {
		return err
	}
	env, err := layout.ReadEnvFile(envFile)
	if err != nil {
		return err
	}
	paths := config.ResolvePaths(func(k string) string { return env[k] })
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// A stop or timeout from systemd ends the update early; Apply still
	// rolls back before it returns.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	res, err := rootupdate.Apply(ctx, rootupdate.Config{
		Installed:  version.Version,
		RequestDir: layout.UpdateRequestDir(filepath.Clean(*dataDir)),
		ServiceUID: int(uid),
		StateDir:   layout.UpdateStateDir,
		Asset:      fmt.Sprintf("dockmesh_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH),
		Files:      serverFiles(binary),
		Verifier:   releasesig.Default(),
		Log:        logger,
		Service: &serverService{
			binary: binary,
			dbPath: paths.DBPath,
			health: healthURL(env["DOCKMESH_HTTP_ADDR"]),
			client: &http.Client{
				Timeout:   3 * time.Second,
				Transport: &http.Transport{Proxy: nil},
				// A redirect from whatever answers on the port must not
				// send root's probe anywhere else.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
		},
	})
	if err != nil {
		return err
	}
	if res.Status != "" {
		logger.Info("update run finished", "status", res.Status, "from", res.From, "to", res.To, "step", res.Step, "reason", res.Reason)
	}
	if res.Status == rootupdate.StatusFailed {
		return errors.New("the rollback did not finish; it is retried on the next run")
	}
	return nil
}

// serverFiles lists what a server update installs: the binary, dmctl next
// to it, and the agent binaries and installer the server hands to hosts,
// in the asset dir install.sh derives from the binary's directory.
func serverFiles(binary string) []rootupdate.File {
	binDir := filepath.Dir(binary)
	assets := filepath.Join(filepath.Dir(binDir), "share", "dockmesh")
	return []rootupdate.File{
		{Name: "dockmesh", Dest: binary, KeepPrev: true},
		{Name: "dmctl", Dest: filepath.Join(binDir, "dmctl"), Optional: true},
		{Name: "dockmesh-agent-linux-amd64", Dest: filepath.Join(assets, "bin", "dockmesh-agent-linux-amd64"), Optional: true},
		{Name: "dockmesh-agent-linux-arm64", Dest: filepath.Join(assets, "bin", "dockmesh-agent-linux-arm64"), Optional: true},
		{Name: "install-agent.sh", Dest: filepath.Join(assets, "install-agent.sh"), Optional: true},
	}
}

// healthURL is the health endpoint on the address the server listens on.
// An unspecified or named host is probed on loopback; only an IP literal
// is used as given, so the config cannot point root's probe elsewhere.
func healthURL(addr string) string {
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "", "8080"
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil, ip.IsUnspecified() && ip.To4() != nil:
		host = "127.0.0.1"
	case ip.IsUnspecified():
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/health"
}

// serverService is the server's side of rootupdate.Service.
type serverService struct {
	binary string
	dbPath string
	health string
	client *http.Client
}

func systemctl(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput() // #nosec G204 -- fixed unit names
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *serverService) Stop(ctx context.Context) error {
	return systemctl(ctx, "stop", serverUnitName)
}

func (s *serverService) Start(ctx context.Context) error {
	return systemctl(ctx, "start", serverUnitName)
}

func (s *serverService) DaemonReload(ctx context.Context) error {
	return systemctl(ctx, "daemon-reload")
}

// unitFiles are the units an update may rewrite and a rollback restores.
var unitFiles = []string{layout.UnitFile, layout.UpdatePathUnitFile, layout.UpdateServiceUnitFile}

func (s *serverService) ReadUnit() ([]byte, error) {
	units := map[string][]byte{}
	for _, f := range unitFiles {
		data, err := layout.ReadUnitFile(f)
		if err != nil {
			return nil, err
		}
		units[f] = data
	}
	return json.Marshal(units)
}

func (s *serverService) RestoreUnit(data []byte) error {
	var units map[string][]byte
	if err := json.Unmarshal(data, &units); err != nil {
		return err
	}
	var errs []error
	for _, f := range unitFiles {
		if content, ok := units[f]; ok {
			errs = append(errs, layout.PutUnitFile(f, content))
		}
	}
	return errors.Join(errs...)
}

// WriteUnit runs the new binary's unit writer, so the unit comes from the
// template of the version being installed.
func (s *serverService) WriteUnit(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "system", "write-unit") // #nosec G204 -- the verified binary just installed
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s system write-unit: %w: %s", binary, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Snapshot and RestoreSnapshot run the installed binary as the dockmesh
// account. Both run while the old version is installed: before the swap,
// and after the rollback put it back.
func (s *serverService) Snapshot(ctx context.Context) error {
	_, err := runAsService(ctx, s.binary, dbStepTimeout, "update", updateSnapshotCmd, "--db", s.dbPath)
	return err
}

func (s *serverService) RestoreSnapshot(ctx context.Context) error {
	_, err := runAsService(ctx, s.binary, dbStepTimeout, "update", updateRestoreCmd, "--db", s.dbPath)
	return err
}

func (s *serverService) Healthy(ctx context.Context, want string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.health, nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body); err != nil {
		return false
	}
	return body.Status == "ok" && strings.TrimPrefix(body.Version, "v") == strings.TrimPrefix(want, "v")
}

func updateDB(sub string, args []string) error {
	fs := flag.NewFlagSet("update "+sub, flag.ExitOnError)
	dbPath := fs.String("db", "", "database file")
	_ = fs.Parse(args)
	if !filepath.IsAbs(*dbPath) {
		return errors.New("--db needs an absolute path")
	}
	if os.Geteuid() == 0 {
		return errors.New("runs as the dockmesh account, not as root")
	}
	if sub == updateSnapshotCmd {
		return db.WriteUpdateSnapshot(*dbPath)
	}
	return db.RestoreUpdateSnapshot(*dbPath)
}
