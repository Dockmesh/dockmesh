package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/dockmesh/dockmesh/internal/webtls"
)

//go:embed setup.example.yaml
var setupExample string

// maxPasswordFile bounds admin.password_file.
const maxPasswordFile = 4 << 10

// setupFileRun is one `setup apply --file`.
type setupFileRun struct {
	file   setup.File
	values setup.ApplyValues
}

// appliedSetup is what a finished setup apply reports.
type appliedSetup struct {
	URL         string
	Fingerprint string
	Admin       string
	// Local: set up in place; the server is not started by the apply.
	Local bool
}

func runSetupApply(args []string) error {
	fset := flag.NewFlagSet("setup apply", flag.ExitOnError)
	path := fset.String("file", "", "setup file (see: dockmesh setup example)")
	dryRun := fset.Bool("dry-run", false, "check the file and this host, change nothing")
	porcelain := fset.Bool("porcelain", false, "print key=value lines for install.sh")
	_ = fset.Parse(args)
	if *path == "" {
		return errors.New("usage: dockmesh setup apply --file <setup file> [--dry-run]")
	}
	run, warnings, err := loadSetupFile(*path)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		if *porcelain {
			fmt.Printf("warning=%s\n", w)
		} else {
			fmt.Fprintln(os.Stderr, "warning  "+w)
		}
	}
	res, err := applySetup(context.Background(), run, *dryRun)
	if err != nil {
		return err
	}
	switch {
	case *dryRun && *porcelain:
		fmt.Println("check=ok")
	case *dryRun:
		fmt.Println("The setup file is valid and this server can be set up with it. Nothing was changed.")
	case *porcelain:
		fmt.Printf("url=%s\nfingerprint=%s\nadmin=%s\n", res.URL, res.Fingerprint, res.Admin)
	case res.Local:
		fmt.Println(res.localText())
	default:
		fmt.Printf("Dockmesh is running at %s\n", res.URL)
		if res.Fingerprint != "" {
			fmt.Printf("Certificate  SHA-256 %s\n", res.Fingerprint)
		}
		if res.Admin != "" {
			fmt.Printf("Sign in as %s.\n", res.Admin)
		} else {
			fmt.Println("Sign in with an account from the backup.")
		}
	}
	return nil
}

// loadSetupFile reads and checks a setup file and the files it names,
// and turns it into the values the wizard's commit takes. The warnings,
// printed before anything is applied, only ever name paths and modes.
func loadSetupFile(path string) (*setupFileRun, []string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := openOperatorFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := setup.ParseFile(f)
	f.Close()
	if err != nil {
		return nil, nil, err
	}
	run := &setupFileRun{file: file}
	var warnings []string
	if w := setup.ReadableWarning(path); w != "" {
		warnings = append(warnings, w)
	}
	// The files the setup file names are called by their key, so nothing
	// about the secrets themselves ends up in a log.
	for _, sf := range file.SecretFiles() {
		if setup.ReadableByOthers(sf.Path) {
			warnings = append(warnings, "the file "+sf.Key+" names is readable by other users. Run chmod 600 on it")
		}
	}
	if run.values, err = setupFileValues(file); err != nil {
		return nil, nil, err
	}
	return run, warnings, nil
}

// setupFileValues maps a setup file to ApplyValues. A password file is
// read, checked against the minimum length and hashed here, so only the
// hash goes on.
func setupFileValues(f setup.File) (setup.ApplyValues, error) {
	var v setup.ApplyValues
	v.DataDir = f.DataDirOrDefault()
	v.HTTPAddr = f.Address.HTTPAddr
	v.BaseURL = f.Address.BaseURL
	v.TLSMode = f.TLS.Mode
	v.TLSCertFile = f.TLS.CertFile
	v.TLSKeyFile = f.TLS.KeyFile
	if f.Admin == nil {
		return v, v.ValidateServer()
	}
	v.Admin.Username = f.Admin.Username
	if v.Admin.Username == "" {
		v.Admin.Username = "admin"
	}
	v.Admin.Email = f.Admin.Email
	v.Admin.PasswordHash = f.Admin.PasswordHash
	if f.Admin.PasswordFile != "" {
		pw, err := readPasswordFile(f.Admin.PasswordFile)
		if err != nil {
			return v, err
		}
		if pw == v.Admin.Username {
			return v, errors.New("admin password must not be the username")
		}
		if err := auth.ValidatePassword(auth.PolicyConfig{MinLength: auth.MinPasswordLength}, pw); err != nil {
			return v, fmt.Errorf("%s: %w", f.Admin.PasswordFile, err)
		}
		if v.Admin.PasswordHash, err = auth.HashPassword(pw); err != nil {
			return v, err
		}
	}
	return v, v.Validate()
}

// readPasswordFile returns the password in path, without the line end
// an editor adds.
func readPasswordFile(path string) (string, error) {
	f, err := openOperatorFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPasswordFile+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxPasswordFile {
		return "", fmt.Errorf("%s is too large for a password file", path)
	}
	pw := strings.TrimRight(string(data), "\r\n")
	if strings.ContainsAny(pw, "\r\n") {
		return "", fmt.Errorf("%s holds more than one line", path)
	}
	return pw, nil
}

// applySetup applies run: as root on Linux through the wizard's commit,
// which writes the config and the units; otherwise (make dev, a container, a source
// build) in this process on the data dir the server uses.
func applySetup(ctx context.Context, run *setupFileRun, dryRun bool) (appliedSetup, error) {
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		return applySetupAsRoot(ctx, run, dryRun)
	}
	return applySetupLocal(ctx, run, dryRun)
}

// applySetupLocal sets up the data dir of this process's environment.
// There is no root here to write a config or a unit, so the data dir
// cannot move and the listener follows the environment.
func applySetupLocal(ctx context.Context, run *setupFileRun, dryRun bool) (appliedSetup, error) {
	paths := config.ResolvePaths(os.Getenv)
	v := run.values
	if run.file.DataDir == "" {
		v.DataDir = paths.DataDir
	}
	if filepath.Clean(v.DataDir) != paths.DataDir {
		return appliedSetup{}, fmt.Errorf("this server keeps its data in %s; set DOCKMESH_DATA_DIR=%s to use that directory", paths.DataDir, v.DataDir)
	}
	facts := setup.Probe{ConfigFiles: []string{loadedEnvFile}, DBPathSet: os.Getenv("DOCKMESH_DB_PATH") != "", Paths: paths}.Scan()
	facts.Users = countUsersReadOnly(paths.DBPath, facts.DB)
	if mode := setup.Decide(facts); mode != setup.NeverSetUp {
		return appliedSetup{}, notNeverSetUp(mode)
	}
	for _, k := range []struct{ name, val string }{{"address.http_addr", run.file.Address.HTTPAddr}, {"tls", run.file.TLS.Mode}} {
		if k.val != "" {
			fmt.Fprintf(os.Stderr, "warning  %s is not applied without root and systemd; the server takes it from its environment\n", k.name)
		}
	}
	if dryRun {
		return appliedSetup{}, nil
	}
	if r := run.file.Restore; r != nil {
		if err := restoreLocal(ctx, r); err != nil {
			return appliedSetup{}, err
		}
		return appliedSetup{URL: localURL(v), Local: true}, nil
	}
	if err := materialize(ctx, v, setup.PKIDir(paths.DataDir)); err != nil {
		return appliedSetup{}, err
	}
	return appliedSetup{URL: localURL(v), Admin: v.Admin.Username, Local: true}, nil
}

func restoreLocal(ctx context.Context, r *setup.FileRestore) error {
	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}
	f, err := openOperatorFile(r.BackupFile)
	if err != nil {
		return err
	}
	defer f.Close()
	key, err := readBackupKey(r.KeyFile, 0)
	if err != nil {
		return err
	}
	src, err := plaintextArchive(f, key)
	if err != nil {
		return err
	}
	if err := restore.CheckSafety(cfg, false); err != nil {
		return err
	}
	files, closeFiles := cliRestoreFiles(ctx)
	defer closeFiles()
	if _, _, err := restore.Stage(ctx, src, cfg, "setup-file:"+r.BackupFile, restore.StageOptions{Files: files}); err != nil {
		return err
	}
	_, err = restore.ApplyStaged(cfg)
	return err
}

func (r appliedSetup) localText() string {
	msg := "Set up. Start the server and open " + r.URL
	if r.Admin != "" {
		msg += ", then sign in as " + r.Admin
	}
	return msg + "."
}

func localURL(v setup.ApplyValues) string {
	if v.BaseURL != "" {
		return v.BaseURL
	}
	return setupBaseURL(serverHTTPAddr(), false)
}

// appliedURL is where a server set up as root is reached, and the
// fingerprint to check when it serves the internal certificate.
func appliedURL(v setup.ApplyValues, dbDir string) (string, string) {
	mode := webtls.Mode(v.TLSMode)
	https := mode == webtls.Internal || mode == webtls.File
	url := v.BaseURL
	if url == "" {
		url = setupBaseURL(v.HTTPAddr, https)
	}
	fp := ""
	if mode == webtls.Internal {
		if c, err := pki.ReadUICert(dbDir); err == nil {
			fp = pki.Fingerprint(c.Raw)
		}
	}
	return url, fp
}

// notNeverSetUp is the refusal of a setup file on a server in mode.
func notNeverSetUp(mode setup.Mode) error {
	return fmt.Errorf("this server is %s; a setup file only applies to a server that was never set up", mode)
}

func runSetupExample() {
	fmt.Print(setupExample)
}
