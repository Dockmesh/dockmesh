package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// stacksAdoptCmd wires up `dmctl stack adopt [path]`. See the doc
// comment inside RunE for the full flow narrative — keeping it there
// means it sits next to the code that actually executes it.
func stacksAdoptCmd() *cobra.Command {
	var (
		flagName     string
		flagDryRun   bool
		flagYes      bool
		flagWithEnv  bool
		flagMaxSize  int64 = 100 * 1024 * 1024
	)
	cmd := &cobra.Command{
		Use:   "adopt [path]",
		Short: "Take over a running docker-compose project — bind it to dockmesh without restarting containers",
		Long: `Hand management of an existing running compose project over to dockmesh.

Given a directory containing a compose.yaml whose containers are already
up (typically via "docker compose up"), this command:

  1. Discovers the running project on the target host
  2. Packages compose.yaml + supporting files (build contexts, configs
     referenced by relative paths, etc.) into a tar.gz
  3. Uploads them to the dockmesh server, which writes them into its
     stacks directory and binds the existing containers via their
     com.docker.compose.project label

No docker operations run against the containers. They keep running
exactly as they were.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}
			info, err := os.Stat(absPath)
			if err != nil {
				return fmt.Errorf("stat %s: %w", absPath, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory — point at the folder containing your compose.yaml", absPath)
			}

			composePath, err := findComposeFile(absPath)
			if err != nil {
				return err
			}
			composeBytes, err := os.ReadFile(composePath)
			if err != nil {
				return fmt.Errorf("read %s: %w", composePath, err)
			}

			projectName := flagName
			if projectName == "" {
				projectName = sanitizeProjectName(filepath.Base(absPath))
			}

			c, err := newClient()
			if err != nil {
				return err
			}

			// --- Discovery phase --------------------------------------------------
			q := url.Values{}
			if flagHost != "" {
				q.Set("host", flagHost)
			}
			var discovered []discoveredStack
			if err := c.request("GET", "/api/v1/stacks/discovered", q, nil, &discovered); err != nil {
				return fmt.Errorf("discover stacks on server: %w", err)
			}
			var match *discoveredStack
			for i := range discovered {
				if discovered[i].ProjectName == projectName {
					match = &discovered[i]
					break
				}
			}
			if match == nil {
				return fmt.Errorf(
					"no running compose project %q found on the target host.\n"+
						"Check the project label with:\n"+
						"  docker inspect <container> --format '{{ index .Config.Labels \"com.docker.compose.project\" }}'\n"+
						"If it differs from %q, pass --name to override.",
					projectName, projectName,
				)
			}

			// --- Tarball phase ----------------------------------------------------
			bundle, fileList, bundleSize, err := buildBundle(absPath, composePath, flagWithEnv, flagMaxSize)
			if err != nil {
				return err
			}

			// --- Diff report ------------------------------------------------------
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "\nAdopting stack '%s' on host %s\n\n", projectName, hostDisplay(match))
			fmt.Fprintf(w, "  Running services detected (%d):\n", match.ServiceCount)
			for _, s := range match.Services {
				fmt.Fprintf(w, "    %-20s  %s (%s)\n", s.Name, truncate(s.Image, 40), s.State)
			}
			fmt.Fprintln(w)
			fmt.Fprintf(w, "  Bundle (%s from %s):\n", humanBytes(bundleSize), absPath)
			for _, f := range fileList {
				fmt.Fprintf(w, "    %s\n", f)
			}
			fmt.Fprintln(w)

			warnings := []string{}
			composeStr := string(composeBytes)
			if strings.Contains(composeStr, "build:") {
				fmt.Fprintln(w, "  ⚠ Compose uses build: context — works for single-host redeploy once")
				fmt.Fprintln(w, "    the source is copied into the stack dir, but for reproducible")
				fmt.Fprintln(w, "    multi-host deploys push the image to a registry.")
				warnings = append(warnings, "build-context")
			}
			if strings.Contains(composeStr, "container_name:") {
				fmt.Fprintln(w, "  ⚠ container_name: is set for one or more services — prevents running")
				fmt.Fprintln(w, "    more than one instance of this stack on the same host.")
				warnings = append(warnings, "container-name-set")
			}
			if hasRelativeBindMount(composeStr) {
				warnings = append(warnings, "relative-paths")
			}
			if len(warnings) > 0 {
				fmt.Fprintln(w)
			}

			if flagDryRun {
				fmt.Fprintln(w, "Dry run — nothing uploaded. Rerun without --dry-run to adopt.")
				return nil
			}
			if !flagYes {
				fmt.Fprint(w, "Proceed? [y/N]: ")
				if !confirmPrompt(cmd.InOrStdin()) {
					fmt.Fprintln(w, "Aborted.")
					return nil
				}
			}

			// --- Upload -----------------------------------------------------------
			var envContent string
			envPath := filepath.Join(absPath, ".env")
			if flagWithEnv {
				if b, err := os.ReadFile(envPath); err == nil {
					envContent = string(b)
				}
			}
			payload := map[string]any{
				"name":              projectName,
				"host_id":           flagHost,
				"compose":           composeStr,
				"env":               envContent,
				"bundle":            base64.StdEncoding.EncodeToString(bundle),
				"accepted_warnings": warnings,
			}
			if flagHost == "" {
				payload["host_id"] = match.HostID // default to the host where we found the containers
			}
			var result adoptResult
			if err := c.request("POST", "/api/v1/stacks/adopt", nil, payload, &result); err != nil {
				return err
			}

			fmt.Fprintf(w, "\n✓ Adopted '%s'\n", result.Name)
			fmt.Fprintf(w, "  Host:             %s\n", result.HostID)
			fmt.Fprintf(w, "  Bound containers: %d\n", result.BoundContainers)
			if len(result.Warnings) > 0 {
				fmt.Fprintf(w, "  Warnings:         %s\n", strings.Join(result.Warnings, ", "))
			}
			fmt.Fprintln(w)
			return nil
		},
	}
	cmd.Flags().StringVar(&flagName, "name", "",
		"Stack name (must match com.docker.compose.project label). Defaults to the directory basename.")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false,
		"Show what would be adopted without uploading anything.")
	cmd.Flags().BoolVar(&flagYes, "yes", false,
		"Skip the confirmation prompt — intended for CI.")
	cmd.Flags().BoolVar(&flagWithEnv, "with-env", false,
		"Include a .env file alongside compose.yaml. Off by default — .env often contains secrets.")
	cmd.Flags().Int64Var(&flagMaxSize, "max-size", flagMaxSize,
		"Max uncompressed bundle size in bytes. Server enforces its own limit too.")
	return cmd
}

// Response shapes — local mirrors of the OpenAPI schemas, duplicated
// here because we don't want dmctl importing the full handlers package.

type discoveredStack struct {
	ProjectName  string                   `json:"project_name"`
	HostID       string                   `json:"host_id"`
	HostName     string                   `json:"host_name"`
	ServiceCount int                      `json:"service_count"`
	Services     []discoveredStackService `json:"services"`
}
type discoveredStackService struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Image string `json:"image"`
}
type adoptResult struct {
	Name            string   `json:"name"`
	HostID          string   `json:"host_id"`
	BoundContainers int      `json:"bound_containers"`
	Warnings        []string `json:"warnings"`
	DriftServices   []string `json:"drift_services"`
}

// findComposeFile returns the path of the first compose file that
// exists in dir, in docker-compose's conventional lookup order.
func findComposeFile(dir string) (string, error) {
	candidates := []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no compose file in %s (looked for %s)", dir, strings.Join(candidates, ", "))
}

// buildBundle walks dir and tars every file into a gzip archive in
// memory. Files ignored by convention: .git, node_modules, IDE junk,
// OS junk, and the compose.yaml itself (server gets that via the
// request body as the authoritative copy). .env is included only when
// --with-env is set, because .env typically contains secrets.
//
// Returns (bundleBytes, relativeFileList, uncompressedTotal, error).
func buildBundle(root, composePath string, withEnv bool, maxSize int64) ([]byte, []string, int64, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Normalised for skip-check.
	relCompose, _ := filepath.Rel(root, composePath)

	// Directory names that we always skip. VCS metadata, language
	// build caches, IDE / tooling scratch dirs — none of which belong
	// in a compose context tarball and all of which can easily eat
	// hundreds of MB. We deliberately don't skip "dist", "build", or
	// "target" because those are routinely compose build: contexts
	// (e.g. `build: ./build/prod`) — let the user trim those if they
	// really want to. The rule: skip things that are always junk,
	// keep anything that might be meaningful context.
	ignoreDirs := map[string]bool{
		".git":         true,
		".hg":          true,
		".svn":         true,
		"node_modules": true,
		"__pycache__":  true,
		".venv":        true,
		"venv":         true,
		".idea":        true,
		".vscode":      true,
		".claude":      true, // Claude Code workspace scratch
		".next":        true, // Next.js build cache
		".nuxt":        true, // Nuxt build cache
		"coverage":     true, // jest / nyc / istanbul output
		".terraform":   true,
		".gradle":      true,
	}

	var fileList []string
	var total int64

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)

		if info.IsDir() {
			if ignoreDirs[base] {
				return filepath.SkipDir
			}
			return nil
		}
		// File-level skips
		switch base {
		case ".DS_Store", "Thumbs.db":
			return nil
		}
		if rel == relCompose {
			return nil
		}
		if rel == ".env" && !withEnv {
			return nil
		}
		// Don't cross symlinks — tar them as regular files would require
		// resolving; cleaner to just skip.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		// Path within the tarball — always forward slashes.
		tarPath := filepath.ToSlash(rel)
		hdr := &tar.Header{
			Name:     tarPath,
			Mode:     int64(info.Mode() & 0o777),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(tw, f)
		_ = f.Close()
		if copyErr != nil {
			return copyErr
		}
		total += n
		if total > maxSize {
			return fmt.Errorf("bundle exceeds %s (so far: %s at %s) — trim the folder or raise --max-size",
				humanBytes(maxSize), humanBytes(total), rel)
		}
		fileList = append(fileList, tarPath)
		return nil
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if err := tw.Close(); err != nil {
		return nil, nil, 0, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, 0, err
	}
	sort.Strings(fileList)
	return buf.Bytes(), fileList, total, nil
}

func sanitizeProjectName(s string) string {
	// Mirror docker-compose's own project-name normalisation: lowercase,
	// strip anything not [a-z0-9-_]. We don't emit underscores because
	// the stacks manager's ValidateName forbids them — but we do accept
	// them in user input and flag the mismatch if discovery fails.
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hasRelativeBindMount(compose string) bool {
	// A volume mount in compose is either `- ./path:/mount` (short form)
	// or `- source: ./path` (long form). Both are textual; cheap regex
	// match is sufficient for the warning heuristic.
	return strings.Contains(compose, "- ./") ||
		strings.Contains(compose, "source: ./")
}

func hostDisplay(d *discoveredStack) string {
	if d.HostName != "" {
		return d.HostName
	}
	return d.HostID
}

func humanBytes(n int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
	)
	switch {
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/kib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func confirmPrompt(in io.Reader) bool {
	scan := bufio.NewScanner(in)
	if !scan.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(scan.Text()))
	return ans == "y" || ans == "yes"
}
