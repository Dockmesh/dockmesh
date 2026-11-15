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
	fmt.Printf("Admin %s, password %s (shown only now).\n", res.Admin, password)
	return nil
}
