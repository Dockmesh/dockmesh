package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/dockmesh/dockmesh/internal/webtls"
)

// maxApplyFile bounds ApplyFile.
const maxApplyFile = 16 << 10

// applyValuesFor turns the wizard input into the values setup-apply
// commits. The password is checked against the policy and hashed here,
// so only the hash reaches the state dir.
func applyValuesFor(in setup.CommitInput) (setup.ApplyValues, error) {
	var v setup.ApplyValues
	if err := auth.ValidatePassword(auth.PolicyConfig{MinLength: auth.MinPasswordLength}, in.Admin.Password); err != nil {
		return v, err
	}
	hash, err := auth.HashPassword(in.Admin.Password)
	if err != nil {
		return v, err
	}
	v.DataDir = filepath.Clean(in.DataDir)
	v.HTTPAddr = in.TLS.HTTPAddr
	v.TLSMode = in.TLS.Mode
	v.TLSCertFile = in.TLS.CertFile
	v.TLSKeyFile = in.TLS.KeyFile
	v.BaseURL = in.PublicURL
	v.Admin.Username = in.Admin.Username
	v.Admin.Email = in.Admin.Email
	v.Admin.PasswordHash = hash
	return v, v.Validate()
}

// writeApplyValues stores v in the state dir for root, 0600.
func writeApplyValues(stateDir string, v setup.ApplyValues) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".apply-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(stateDir, setup.ApplyFile))
}

// readApplyValues reads and validates ApplyFile from stateDir; f is open
// on it already when root reads it through a checked handle.
func readApplyValues(r io.Reader) (setup.ApplyValues, error) {
	var v setup.ApplyValues
	data, err := io.ReadAll(io.LimitReader(r, maxApplyFile+1))
	if err != nil {
		return v, err
	}
	if len(data) > maxApplyFile {
		return v, errors.New("setup values too large")
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("setup values: %w", err)
	}
	return v, v.Validate()
}

// pkiFiles are the PKI files setup mode created, which the data dir gets
// at the commit so the certificate fingerprint stays the same.
var pkiFiles = []string{"agents-ca.crt", "agents-ca.key", "agents-server.crt", "agents-server.key", pki.UICertFile, "ui-server.key"}

// runSetupMaterialize is `dockmesh setup materialize`, run by root's
// setup-apply as the service account: it creates the database, the admin
// and the settings in the new data dir and copies the PKI there.
func runSetupMaterialize(args []string) error {
	fset := flag.NewFlagSet("setup materialize", flag.ExitOnError)
	stateDir := fset.String("state-dir", "", "setup state dir")
	dataDir := fset.String("data-dir", "", "data dir to set up")
	_ = fset.Parse(args)
	if os.Geteuid() == 0 {
		return errors.New("runs as the dockmesh account, not as root")
	}
	if !filepath.IsAbs(*stateDir) || !filepath.IsAbs(*dataDir) {
		return errors.New("--state-dir and --data-dir need absolute paths")
	}
	f, err := os.Open(filepath.Join(*stateDir, setup.ApplyFile))
	if err != nil {
		return err
	}
	v, err := readApplyValues(f)
	f.Close()
	if err != nil {
		return err
	}
	if filepath.Clean(*dataDir) != v.DataDir {
		return fmt.Errorf("data dir %s is not the one the setup values name", *dataDir)
	}
	return materialize(context.Background(), v, setup.PKIDir(filepath.Dir(*stateDir)))
}

// materialize sets up the data dir of v from the setup state, whose PKI
// is in pkiDir.
func materialize(ctx context.Context, v setup.ApplyValues, pkiDir string) error {
	paths := config.ResolvePaths(func(k string) string {
		if k == "DOCKMESH_DATA_DIR" {
			return v.DataDir
		}
		return ""
	})
	if _, err := os.Lstat(paths.DBPath); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s exists already", paths.DBPath)
	}
	dataSub := filepath.Dir(paths.DBPath)
	if err := os.MkdirAll(dataSub, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.StacksRoot, 0o750); err != nil {
		return err
	}
	for _, name := range pkiFiles {
		if err := copyPKIFile(filepath.Join(pkiDir, name), filepath.Join(dataSub, name)); err != nil {
			return err
		}
	}
	conn, err := db.Open(paths.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := db.Migrate(conn); err != nil {
		return err
	}
	if _, err := auth.NewService(conn, make([]byte, 32)).CreateFirstUserHashed(ctx, v.Admin.Username, v.Admin.Email, v.Admin.PasswordHash, "admin"); err != nil {
		return fmt.Errorf("create the admin: %w", err)
	}
	if v.BaseURL != "" {
		st := settings.NewStore(conn)
		if err := st.Load(ctx); err != nil {
			return err
		}
		if err := saveSetupURLs(ctx, st, v.BaseURL, ":8443"); err != nil {
			return err
		}
	}
	return nil
}

// copyPKIFile copies a PKI file into a new file with the same mode; a
// file the setup never created is skipped.
func copyPKIFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- fixed names in the setup state dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm()|0o200) // #nosec G304 -- fixed names in the new data dir
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, 1<<20)); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, fi.Mode().Perm())
}

// checkServeConfig is `dockmesh serve --check-config`: it checks what a start
// with the loaded config needs, without writing anything.
func checkServeConfig() error {
	mode, err := webtls.ParseMode(os.Getenv("DOCKMESH_TLS_MODE"))
	if err != nil {
		return err
	}
	addr := webtls.ListenAddr(mode, os.Getenv("DOCKMESH_HTTP_ADDR"))
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("DOCKMESH_HTTP_ADDR %q: %w", addr, err)
	}
	paths := config.ResolvePaths(os.Getenv)
	fi, err := os.Stat(paths.DataDir)
	if err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("data dir %s is not a directory", paths.DataDir)
	}
	probe, err := os.CreateTemp(paths.DataDir, ".check-config-*")
	if err != nil {
		return fmt.Errorf("data dir %s is not writable by the service account: %w", paths.DataDir, err)
	}
	probe.Close()
	if err := os.Remove(probe.Name()); err != nil {
		return err
	}
	if mode == webtls.File {
		if _, err := webtls.LoadPair(os.Getenv("DOCKMESH_TLS_CERT_FILE"), os.Getenv("DOCKMESH_TLS_KEY_FILE")); err != nil {
			return err
		}
	}
	return nil
}

// waitApplyResult polls the setup-apply result root writes into the update
// dir, newer than since.
func waitApplyResult(ctx context.Context, read func() ([]byte, time.Time, error), since time.Time) (applyResult, error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if data, mod, err := read(); err == nil && !mod.Before(since) {
			var r applyResult
			if json.Unmarshal(data, &r) == nil && r.Status != "" {
				return r, nil
			}
		}
		select {
		case <-ctx.Done():
			return applyResult{}, ctx.Err()
		case <-tick.C:
		}
	}
}

// setupResultFile is where root reports a setup-apply, in the update dir
// next to, and apart from, the result of an update.
const setupResultFile = "setup-result.json"

// applyResult is the result file of a setup-apply.
type applyResult struct {
	Status string `json:"status"`
	Step   string `json:"step,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Setup-apply result states.
const (
	applyRestarting = "restarting"
	applyRefused    = "refused"
	applyFailed     = "failed"
	applyApplied    = "applied"
)

// freshSetupCommit is the wizard commit of a new install. With the root
// updater (a systemd install) it hands the values to setup-apply and
// follows it until the server restarts on the new config; without it (a
// container, a source build) the service sets up its own data dir and
// restarts.
func freshSetupCommit(dataDir string, restart func()) setup.CommitFunc {
	return func(ctx context.Context, in setup.CommitInput, emit func(step, msg, status string)) error {
		v, err := applyValuesFor(in)
		if err != nil {
			emit("values", err.Error(), "fail")
			return err
		}
		if err := writeApplyValues(setup.StateDir(dataDir), v); err != nil {
			emit("values", err.Error(), "fail")
			return err
		}
		if !rootUpdaterInstalled() {
			return materializeInProcess(ctx, dataDir, v, restart, emit)
		}
		reqDir := layout.UpdateRequestDir(dataDir)
		if err := os.MkdirAll(reqDir, 0o750); err != nil {
			emit("apply", err.Error(), "fail")
			return err
		}
		since := time.Now().Add(-time.Second)
		req, _ := json.Marshal(rootupdate.Request{Type: rootupdate.RequestSetupApply})
		if err := writeFileReplace(filepath.Join(reqDir, rootupdate.RequestFile), req); err != nil {
			emit("apply", err.Error(), "fail")
			return err
		}
		emit("apply", "the root updater checks and applies the setup", "info")
		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		res, err := waitApplyResult(waitCtx, func() ([]byte, time.Time, error) {
			path := filepath.Join(reqDir, setupResultFile)
			fi, err := os.Stat(path)
			if err != nil {
				return nil, time.Time{}, err
			}
			data, err := os.ReadFile(path) // #nosec G304 -- fixed name in the update dir
			return data, fi.ModTime(), err
		}, since)
		if err != nil {
			emit("apply", "no answer from the root updater: "+err.Error(), "fail")
			return err
		}
		if res.Status != applyRestarting {
			msg := res.Reason
			if msg == "" {
				msg = res.Status
			}
			emit(res.Step, msg, "fail")
			return errors.New(msg)
		}
		emit("restart", "setup applied; the server restarts on the new configuration", "ok")
		return nil
	}
}

// rootUpdaterInstalled reports a systemd install with the root updater
// units, which setup-apply needs.
func rootUpdaterInstalled() bool {
	_, err := os.Stat(layout.UpdatePathUnitFile)
	return err == nil
}

// materializeInProcess sets up the data dir the server was started with
// and restarts into it. There is no root to write a config here, so the
// data dir cannot move and the listener follows the environment.
func materializeInProcess(ctx context.Context, dataDir string, v setup.ApplyValues, restart func(), emit func(step, msg, status string)) error {
	if v.DataDir != filepath.Clean(dataDir) {
		err := fmt.Errorf("this server keeps its data in %s; set DOCKMESH_DATA_DIR to use another directory", dataDir)
		emit("data_dir", err.Error(), "fail")
		return err
	}
	if err := materialize(ctx, v, setup.PKIDir(dataDir)); err != nil {
		emit("materialize", err.Error(), "fail")
		return err
	}
	if err := os.RemoveAll(setup.StateDir(dataDir)); err != nil {
		emit("cleanup", err.Error(), "warn")
	}
	emit("restart", "setup applied; the server restarts", "ok")
	time.AfterFunc(2*time.Second, restart)
	return nil
}

// writeFileReplace replaces path with data through a temp file and rename.
func writeFileReplace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
