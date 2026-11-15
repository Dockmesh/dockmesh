package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/setup"
)

const initDeprecation = "warning  dockmesh init is deprecated and will be removed in 0.7.0. Use the setup wizard, or: sudo dockmesh setup apply --file <file> (template: dockmesh setup example)"

// runInitCmd is `dockmesh init`, a thin wrapper on `setup apply`: --file
// applies a setup file, --yes builds one from the flags with a generated
// admin password.
func runInitCmd(args []string) {
	fmt.Fprintln(os.Stderr, initDeprecation)
	if err := runInit(args); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}
}

func runInit(args []string) error {
	fset := flag.NewFlagSet("init", flag.ExitOnError)
	file := fset.String("file", "", "setup file, as for setup apply --file")
	yes := fset.Bool("yes", false, "set up with the flags below and a generated admin password")
	dataDir := fset.String("data-dir", "", "data directory (default /var/lib/dockmesh)")
	listen := fset.String("listen", "", "web listen address (default :8080)")
	adminUser := fset.String("admin-user", "admin", "admin username")
	baseURL := fset.String("base-url", "", "public base URL")
	pwFile := fset.String("password-file", defaultInitPasswordFile(), "with --yes: new file that gets the generated admin password (mode 0600)")
	_ = fset.Bool("service", true, "ignored: the service is always installed")
	_ = fset.Bool("systemd", true, "ignored: the service is always installed")
	_ = fset.Parse(args)
	if *file != "" {
		return runSetupApply([]string{"--file", *file})
	}
	if !*yes {
		return errors.New("init no longer asks questions; pass --yes, or --file <setup file>")
	}
	password, err := auth.GeneratePassword(20)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	// The password goes to a private file, never to the terminal: init
	// runs under cloud-init and CI, which keep their output in logs.
	if err := writeNewPrivateFile(*pwFile, password+"\n"); err != nil {
		return fmt.Errorf("admin password file: %w", err)
	}
	f := setup.File{
		DataDir: *dataDir,
		Admin:   &setup.FileAdmin{Username: *adminUser, PasswordHash: hash},
		Address: setup.FileAddress{BaseURL: *baseURL, HTTPAddr: *listen},
	}
	v, err := setupFileValues(f)
	if err != nil {
		return err
	}
	res, err := applySetup(context.Background(), &setupFileRun{file: f, values: v}, false)
	if err != nil {
		_ = os.Remove(*pwFile)
		return err
	}
	if res.Local {
		fmt.Println(res.localText())
	} else {
		fmt.Printf("Dockmesh is running at %s\n", res.URL)
		if res.Fingerprint != "" {
			fmt.Printf("Certificate  SHA-256 %s\n", res.Fingerprint)
		}
	}
	fmt.Printf("Sign in as %s; the password is in %s.\n", res.Admin, *pwFile)
	return nil
}

func defaultInitPasswordFile() string {
	if os.Geteuid() == 0 {
		return "/root/dockmesh-admin-password"
	}
	return "dockmesh-admin-password"
}

// writeNewPrivateFile creates path, which must not exist, with mode 0600.
func writeNewPrivateFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the operator's own file
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
