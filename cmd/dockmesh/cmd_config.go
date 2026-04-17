package main

import (
	"flag"
	"fmt"
	"os"
)

// runConfigCmd handles `dockmesh config <subcommand>`.
func runConfigCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh config <show>")
		os.Exit(2)
	}
	switch args[0] {
	case "show":
		if err := configShow(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "config show:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

// configShow prints the effective runtime config. Anything derived from
// a secret (JWT secret, DB password in DSN, etc.) is redacted. The
// output is meant for troubleshooting — copy-paste into bug reports.
func configShow(args []string) error {
	_ = flag.NewFlagSet("config show", flag.ExitOnError).Parse(args)

	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}

	fmt.Println("# Effective dockmesh config (secrets redacted)")
	fmt.Printf("HTTPAddr          = %q\n", cfg.HTTPAddr)
	fmt.Printf("BaseURL           = %q\n", cfg.BaseURL)
	fmt.Printf("DBPath            = %q\n", cfg.DBPath)
	fmt.Printf("StacksRoot        = %q\n", cfg.StacksRoot)
	fmt.Printf("SecretsPath       = %q\n", cfg.SecretsPath)
	fmt.Printf("SecretsKeyPath    = %q\n", cfg.SecretsKeyPath)
	fmt.Printf("SecretsEncryptEnv = %t\n", cfg.SecretsEncryptEnv)
	fmt.Printf("AuditGenesisPath  = %q\n", cfg.AuditGenesisPath)
	fmt.Printf("ScannerBinary     = %q\n", cfg.ScannerBinary)
	fmt.Printf("ScannerEnabled    = %t\n", cfg.ScannerEnabled)
	fmt.Printf("ProxyEnabled      = %t\n", cfg.ProxyEnabled)
	fmt.Printf("AgentListen       = %q\n", cfg.AgentListen)
	fmt.Printf("AgentPublicURL    = %q\n", cfg.AgentPublicURL)
	fmt.Printf("AgentSANs         = %q\n", cfg.AgentSANs)
	fmt.Printf("JWTSecret         = <redacted, %d bytes>\n", len(cfg.JWTSecret))
	return nil
}
