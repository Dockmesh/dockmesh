package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// GrypeCLI runs the grype binary as a subprocess. Pragmatic choice for
// Phase 2 — the concept §15.12 wants an embedded library, but grype's
// library API has a huge dep graph. Swapping in an embedded impl later
// is a matter of writing a new Scanner and pointing main.go at it.
type GrypeCLI struct {
	Binary  string        // defaults to "grype"
	Timeout time.Duration // per-scan timeout, default 5 min
	// ArchiveDir holds image archives while they are scanned. Empty means
	// the system temp directory, which may be a small tmpfs.
	ArchiveDir string

	archiveSlot chan struct{}
}

// ArchiveDirName is the directory below the data dir that holds image
// archives during a scan. System backups leave it out.
const ArchiveDirName = "scan-tmp"

// PrepareArchiveDir empties the archive directory below dataDir, as a
// crash during a scan leaves its archive behind, and returns its path.
func PrepareArchiveDir(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, ArchiveDirName)
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// MaxScanArchiveBytes bounds an image archive received for scanning, so a
// host can't fill the server's disk.
const MaxScanArchiveBytes int64 = 20 << 30

const (
	// diskReserveBytes stays free while an archive is received: the
	// database lives on the same disk.
	diskReserveBytes uint64 = 1 << 30
	// diskCheckBytes is how much is written between free-space checks.
	diskCheckBytes int64 = 64 << 20

	maxScanOutputBytes = 256 << 20
	maxScanStderrBytes = 64 << 10
)

var (
	// ErrArchiveTooLarge is returned for an image over MaxScanArchiveBytes.
	ErrArchiveTooLarge = errors.New("image is too large to scan")
	// ErrScanDiskFull is returned when an archive would leave too little
	// free space on the server.
	ErrScanDiskFull = errors.New("not enough free disk space on the server to scan this image")
)

// reservingWriter fails once dir has less than diskReserveBytes free.
type reservingWriter struct {
	dst       io.Writer
	dir       string
	free      func(dir string) (uint64, bool)
	sinceLast int64
}

func (w *reservingWriter) check() error {
	if free, ok := w.free(w.dir); ok && free < diskReserveBytes {
		return ErrScanDiskFull
	}
	return nil
}

func (w *reservingWriter) Write(p []byte) (int, error) {
	if w.sinceLast >= diskCheckBytes {
		if err := w.check(); err != nil {
			return 0, err
		}
		w.sinceLast = 0
	}
	n, err := w.dst.Write(p)
	w.sinceLast += int64(n)
	return n, err
}

// cappedBuffer keeps the first max bytes and notes that more arrived.
type cappedBuffer struct {
	bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); len(p) > room {
		b.truncated = true
		_, _ = b.Buffer.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// grypeEnv is the server's environment without its own settings, which
// hold secrets. Grype parses archives from other hosts and reads only its
// GRYPE_ variables and the usual system ones.
func grypeEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "DOCKMESH_")
	})
}

func NewGrypeCLI(binary string) *GrypeCLI {
	if binary == "" {
		binary = "grype"
	}
	return &GrypeCLI{Binary: binary, Timeout: 5 * time.Minute, archiveSlot: make(chan struct{}, 1)}
}

func (g *GrypeCLI) Name() string { return "grype" }

func (g *GrypeCLI) Ready() error {
	if _, err := exec.LookPath(g.Binary); err != nil {
		return fmt.Errorf("grype binary not found in PATH: %w", err)
	}
	return nil
}

func (g *GrypeCLI) Scan(ctx context.Context, image string) (*Report, error) {
	if image == "" {
		return nil, errors.New("image required")
	}
	// Use the docker daemon as source so we don't re-pull via registry.
	// The scheme is always added: a tag can start with "docker:" or
	// "registry:" itself.
	return g.run(ctx, "docker:"+image, image)
}

// ScanArchive stores the archive in ArchiveDir, as grype needs a seekable
// file, and scans that file.
func (g *GrypeCLI) ScanArchive(ctx context.Context, archive io.Reader, image string) (*Report, error) {
	if image == "" {
		return nil, errors.New("image required")
	}
	// One at a time, so that parallel scans can't add up on the disk.
	select {
	case g.archiveSlot <- struct{}{}:
		defer func() { <-g.archiveSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f, err := os.CreateTemp(g.ArchiveDir, "dockmesh-scan-*.tar")
	if err != nil {
		return nil, fmt.Errorf("create scan archive: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	dst := &reservingWriter{dst: f, dir: filepath.Dir(f.Name()), free: freeBytes}
	if err := dst.check(); err != nil {
		return nil, err
	}
	n, err := io.Copy(dst, io.LimitReader(archive, MaxScanArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("receive image archive: %w", err)
	}
	if n > MaxScanArchiveBytes {
		return nil, ErrArchiveTooLarge
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write scan archive: %w", err)
	}
	return g.run(ctx, "docker-archive:"+f.Name(), image)
}

// run scans source and files the report under image.
func (g *GrypeCLI) run(ctx context.Context, source, image string) (*Report, error) {
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, g.Binary, source, "-o", "json", "--quiet") //nolint:gosec // source always starts with a scheme, so it can't be an option
	cmd.Env = grypeEnv()
	stdout := cappedBuffer{max: maxScanOutputBytes}
	stderr := cappedBuffer{max: maxScanStderrBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.truncated {
		return nil, errors.New("grype output is too large")
	}
	out := stdout.Bytes()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			// Grype exits 1 when vulnerabilities are found — that's normal.
			// Only treat exit codes > 1 as real errors. Exit 1 still
			// produces valid JSON on stdout which we parse below.
			if ee.ExitCode() > 1 || len(out) == 0 {
				return nil, fmt.Errorf("grype exit %d: %s", ee.ExitCode(), stderr.String())
			}
		} else {
			return nil, fmt.Errorf("run grype: %w", err)
		}
	}

	var raw grypeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse grype json: %w", err)
	}

	rep := &Report{
		Image:           image,
		Scanner:         "grype",
		ScannerVersion:  raw.Descriptor.Version,
		ScannedAt:       time.Now().UTC(),
		Vulnerabilities: []Vulnerability{},
	}
	for _, m := range raw.Matches {
		sev := normalizeSeverity(m.Vulnerability.Severity)
		v := Vulnerability{
			ID:       m.Vulnerability.ID,
			Severity: sev,
			Package:  m.Artifact.Name,
			Version:  m.Artifact.Version,
			Type:     m.Artifact.Type,
			URL:      m.Vulnerability.DataSource,
		}
		if len(m.Vulnerability.Fix.Versions) > 0 {
			v.FixedIn = strings.Join(m.Vulnerability.Fix.Versions, ", ")
		}
		rep.Vulnerabilities = append(rep.Vulnerabilities, v)
		rep.Summary.add(sev)
	}
	return rep, nil
}

// grypeOutput is the subset of `grype -o json` we care about. Grype's full
// schema has many more fields we don't use.
type grypeOutput struct {
	Matches []grypeMatch `json:"matches"`
	Descriptor struct {
		Version string `json:"version"`
	} `json:"descriptor"`
}

type grypeMatch struct {
	Vulnerability grypeVuln     `json:"vulnerability"`
	Artifact      grypeArtifact `json:"artifact"`
}

type grypeVuln struct {
	ID         string   `json:"id"`
	Severity   string   `json:"severity"`
	DataSource string   `json:"dataSource"`
	Fix        grypeFix `json:"fix"`
}

type grypeFix struct {
	Versions []string `json:"versions"`
	State    string   `json:"state"`
}

type grypeArtifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
}

func normalizeSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "critical":
		return SevCritical
	case "high":
		return SevHigh
	case "medium":
		return SevMedium
	case "low":
		return SevLow
	case "negligible":
		return SevNegligible
	default:
		return SevUnknown
	}
}
