package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// runImportCmd handles `dockmesh import <source> [flags]`. For now only
// `compose-dir` is supported; Portainer BoltDB, Dockge, Coolify etc.
// can slot in here later as separate subcommands.
func runImportCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh import <compose-dir> --path <dir> [--prefix ...] [--force] [--dry-run]")
		os.Exit(2)
	}
	switch args[0] {
	case "compose-dir":
		if err := importComposeDir(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "import compose-dir:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown import source: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "supported: compose-dir")
		os.Exit(2)
	}
}

// composeFileNames is the priority list for finding the compose file in
// a stack directory. Matches what Docker Compose itself accepts.
var composeFileNames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

type importResult struct {
	source string // subdir path
	stack  string // resolved stack name
	action string // "created" | "skipped" | "failed" | "would-create" | "would-skip" | "would-fail"
	reason string // for skipped/failed
	hasEnv bool
}

func importComposeDir(args []string) error {
	fs := flag.NewFlagSet("import compose-dir", flag.ExitOnError)
	path := fs.String("path", "", "source directory containing stack subdirectories (required)")
	prefix := fs.String("prefix", "", "prefix to add to imported stack names (e.g. --prefix legacy- so 'nginx' becomes 'legacy-nginx')")
	force := fs.Bool("force", false, "overwrite existing stacks instead of skipping them")
	dryRun := fs.Bool("dry-run", false, "print what would happen without writing anything")
	_ = fs.Parse(args)

	if *path == "" {
		return fmt.Errorf("--path is required")
	}

	info, err := os.Stat(*path)
	if err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", *path)
	}

	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}

	// We need a stacks.Manager so name validation + encryption + fsnotify
	// hooks all behave the same as the live server. We pass the same
	// secrets service the server uses so .env files are encrypted when
	// the deployment has secrets encryption enabled.
	secretsSvc, err := secrets.New(cfg.SecretsKeyPath, cfg.SecretsEncryptEnv)
	if err != nil {
		return fmt.Errorf("secrets init: %w", err)
	}
	mgr, err := stacks.NewManager(cfg.StacksRoot, secretsSvc)
	if err != nil {
		return fmt.Errorf("stacks manager: %w", err)
	}
	defer mgr.Close()

	entries, err := os.ReadDir(*path)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var results []importResult
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(*path, e.Name())
		composePath := findComposeFile(subdir)
		if composePath == "" {
			// Silent skip — not every subdir is a stack. Mentioning
			// non-stack dirs in the report would be noise.
			continue
		}

		name, nameErr := slugifyStackName(*prefix + e.Name())
		res := importResult{source: subdir, stack: name}

		switch {
		case nameErr != nil:
			res.action = fail3(*dryRun, "fail")
			res.reason = nameErr.Error()
			results = append(results, res)
			continue
		}

		composeBytes, readErr := os.ReadFile(composePath)
		if readErr != nil {
			res.action = fail3(*dryRun, "fail")
			res.reason = "read compose: " + readErr.Error()
			results = append(results, res)
			continue
		}

		envPath := filepath.Join(subdir, ".env")
		var envContent string
		if b, err := os.ReadFile(envPath); err == nil {
			envContent = string(b)
			res.hasEnv = true
		}

		if *dryRun {
			// Check if the target already exists purely for reporting.
			if _, err := mgr.Get(name); err == nil {
				if *force {
					res.action = "would-overwrite"
				} else {
					res.action = "would-skip"
					res.reason = "already exists"
				}
			} else {
				res.action = "would-create"
			}
			results = append(results, res)
			continue
		}

		// Live write.
		if _, err := mgr.Create(name, string(composeBytes), envContent); err != nil {
			if errors.Is(err, stacks.ErrExists) {
				if !*force {
					res.action = "skipped"
					res.reason = "already exists (use --force to overwrite)"
					results = append(results, res)
					continue
				}
				if _, err := mgr.Update(name, string(composeBytes), envContent); err != nil {
					res.action = "failed"
					res.reason = "overwrite: " + err.Error()
					results = append(results, res)
					continue
				}
				res.action = "overwrote"
				results = append(results, res)
				continue
			}
			res.action = "failed"
			res.reason = err.Error()
			results = append(results, res)
			continue
		}
		res.action = "created"
		results = append(results, res)
	}

	return printImportReport(results, *dryRun)
}

// findComposeFile returns the first matching compose filename in dir,
// or "" if none found.
func findComposeFile(dir string) string {
	for _, name := range composeFileNames {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// slugifyStackName converts an arbitrary directory name to something
// that passes stacks.ValidateName. Rules:
//   - lowercase
//   - [^a-z0-9-] → '-'
//   - collapse consecutive '-'
//   - trim leading/trailing '-'
//   - length 2..63
//
// Returns an error if the result is empty, too short, or matches a
// reserved name ("admin", "system", …).
var slugBadChars = regexp.MustCompile(`[^a-z0-9-]+`)
var slugRepeated = regexp.MustCompile(`-+`)

func slugifyStackName(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = slugBadChars.ReplaceAllString(s, "-")
	s = slugRepeated.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "", fmt.Errorf("directory name %q produces empty slug", raw)
	}
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	if err := stacks.ValidateName(s); err != nil {
		return "", fmt.Errorf("slug %q: %w", s, err)
	}
	return s, nil
}

// fail3 picks between "fail"/"would-fail" depending on dry-run mode.
func fail3(dryRun bool, base string) string {
	if dryRun {
		return "would-" + base
	}
	return base
}

func printImportReport(results []importResult, dryRun bool) error {
	if len(results) == 0 {
		fmt.Println("No stacks found. Expected subdirectories containing compose.yaml / docker-compose.yml.")
		return nil
	}
	var created, skipped, failed, overwrote int
	for _, r := range results {
		envMark := ""
		if r.hasEnv {
			envMark = " (.env)"
		}
		switch r.action {
		case "created", "would-create":
			created++
			fmt.Printf("  %-16s  %s%s  <- %s\n", r.action+":", r.stack, envMark, r.source)
		case "overwrote", "would-overwrite":
			overwrote++
			fmt.Printf("  %-16s  %s%s  <- %s\n", r.action+":", r.stack, envMark, r.source)
		case "skipped", "would-skip":
			skipped++
			fmt.Printf("  %-16s  %s  (%s)\n", r.action+":", r.stack, r.reason)
		case "failed", "would-fail":
			failed++
			fmt.Printf("  %-16s  %s  (%s)\n", r.action+":", r.stack, r.reason)
		}
	}
	fmt.Println()
	if dryRun {
		fmt.Printf("Dry run: %d would create, %d would overwrite, %d would skip, %d would fail\n",
			created, overwrote, skipped, failed)
		fmt.Println("Re-run without --dry-run to apply.")
	} else {
		fmt.Printf("Summary: %d created, %d overwrote, %d skipped, %d failed\n",
			created, overwrote, skipped, failed)
	}
	if failed > 0 && !dryRun {
		return fmt.Errorf("%d stack(s) failed to import", failed)
	}
	return nil
}
