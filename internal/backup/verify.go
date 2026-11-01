package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// VerifyResult is the per-type structured report from VerifyRun. Each
// backup type fills in the bits that make sense for it; the UI shows
// whichever fields are populated.
type VerifyResult struct {
	RunID    int64         `json:"run_id"`
	Type     string        `json:"type"` // "system" | "stack" | "volume"
	Passed   bool          `json:"passed"`
	Checks   []VerifyCheck `json:"checks"`
	Summary  string        `json:"summary,omitempty"`
	Counts   VerifyCounts  `json:"counts"`
	SHA256OK bool          `json:"sha256_ok,omitempty"`
	// diskNeed estimates the disk space each volume of the archive takes
	// once extracted, keyed by volume ("" for a volume run).
	diskNeed map[string]int64
}

// extractedSize estimates the disk space of an extracted archive: its
// payload plus a block per entry.
func extractedSize(bytes int64, entries int) int64 { return bytes + int64(entries)*4096 }

func (r *VerifyResult) setNeed(vol string, bytes int64, entries int) {
	if r.diskNeed == nil {
		r.diskNeed = map[string]int64{}
	}
	r.diskNeed[vol] = extractedSize(bytes, entries)
}

// VerifyCheck mirrors the existing restore.SanityCheck shape so the UI
// can render system-restore checks and stack/volume verifies through
// the same component.
type VerifyCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "ok" | "warn" | "fail"
	Message string `json:"message,omitempty"`
}

type VerifyCounts struct {
	Entries    int      `json:"entries"`
	Bytes      int64    `json:"bytes"`
	Volumes    []string `json:"volumes,omitempty"`     // populated for stack runs
	StackFiles []string `json:"stack_files,omitempty"` // populated for stack runs
}

// VerifyRun reads the archive for the given run, dispatches to the
// type-specific verifier, and returns the structured result. System runs
// get their deep checks in the handler (internal/restore), which wraps
// OpenRunHashed instead.
func (s *Service) VerifyRun(ctx context.Context, runID int64) (*VerifyResult, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(run.Sources) == 0 {
		return nil, errors.New("run has no sources — cannot determine backup type")
	}
	srcType := run.Sources[0].Type
	if srcType == "system" {
		return nil, errors.New("system runs are verified with the restore checks (POST /backups/runs/{id}/verify)")
	}
	res := &VerifyResult{RunID: runID, Type: srcType, Passed: true}

	rc, finish, err := s.openRunHashed(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("open run archive: %w", err)
	}
	defer rc.Close()

	switch srcType {
	case "stack":
		verifyStackArchive(res, rc)
	case "volume":
		verifyVolumeArchive(res, rc)
	default:
		return nil, fmt.Errorf("unknown source type %q", srcType)
	}
	res.add(finish())
	if res.Passed && res.Summary == "" {
		res.Summary = "archive intact"
	}
	return res, nil
}

// OpenRunHashed opens a run's archive as plaintext. finish drains what
// the caller left unread and compares the hash with the stored one.
func (s *Service) OpenRunHashed(ctx context.Context, runID int64) (io.ReadCloser, func() VerifyCheck, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return s.openRunHashed(ctx, run)
}

// openRunHashed opens the run's archive for the walkers. run.SHA256
// covers the bytes as stored on the target (ciphertext for encrypted
// runs), so the hash is taken below the decryption.
func (s *Service) openRunHashed(ctx context.Context, run *Run) (io.ReadCloser, func() VerifyCheck, error) {
	h := sha256.New()
	rc, err := s.openRun(ctx, run, h)
	if err != nil {
		return nil, nil, err
	}
	finish := func() VerifyCheck {
		// Tar readers stop at the end-of-archive marker and gzip at its
		// trailer; the padding after them is part of the hash.
		if _, err := io.Copy(io.Discard, rc); err != nil {
			return VerifyCheck{Name: "sha256", Status: "fail", Message: "read archive: " + err.Error()}
		}
		return shaCheck(hex.EncodeToString(h.Sum(nil)), run.SHA256)
	}
	return rc, finish, nil
}

func (r *VerifyResult) add(c VerifyCheck) {
	r.Checks = append(r.Checks, c)
	if c.Status == "fail" {
		r.Passed = false
	}
	if c.Name == "sha256" && c.Status == "ok" {
		r.SHA256OK = true
	}
}

func (r *VerifyResult) fail(name, msg string) {
	r.add(VerifyCheck{Name: name, Status: "fail", Message: msg})
}

// verifyStackArchive walks the outer tar (stack/<rel> + volumes/<vol>.tar.gz),
// gunzip-walks each volume entry and compares the volumes found with the
// list the backup recorded in its manifest.
func verifyStackArchive(res *VerifyResult, r io.Reader) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		res.fail("gzip", err.Error())
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hasCompose := false
	var manifest *stackManifest
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.fail("tar", err.Error())
			return
		}
		res.Counts.Entries++
		name := hdr.Name
		switch {
		case name == stackManifestName:
			m, err := readStackManifest(tr)
			if err != nil {
				res.fail("manifest", err.Error())
				return
			}
			manifest = m
		case strings.HasPrefix(name, "stack/"):
			rel := strings.TrimPrefix(name, "stack/")
			if rel == "" {
				continue
			}
			if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
				res.Counts.StackFiles = append(res.Counts.StackFiles, rel)
				if rel == "compose.yaml" {
					hasCompose = true
				}
			}
			n, err := io.Copy(io.Discard, tr)
			res.Counts.Bytes += n
			if err != nil {
				res.fail("stack."+rel, err.Error())
				return
			}
		case strings.HasPrefix(name, "volumes/") && strings.HasSuffix(name, ".tar.gz"):
			volName := strings.TrimSuffix(strings.TrimPrefix(name, "volumes/"), ".tar.gz")
			if volName == "" {
				res.add(VerifyCheck{Name: "volume.malformed", Status: "warn", Message: name})
				continue
			}
			n, entries, err := walkInnerTar(io.LimitReader(tr, hdr.Size))
			res.Counts.Bytes += n
			res.setNeed(volName, n, entries)
			if err != nil {
				res.fail("volume."+volName, err.Error())
				return
			}
			res.Counts.Volumes = append(res.Counts.Volumes, volName)
			res.add(VerifyCheck{Name: "volume." + volName, Status: "ok", Message: fmt.Sprintf("%d bytes", hdr.Size)})
		default:
			n, _ := io.Copy(io.Discard, tr)
			res.Counts.Bytes += n
		}
	}
	if hasCompose {
		res.add(VerifyCheck{Name: "stack.compose", Status: "ok", Message: "compose.yaml present"})
	} else {
		res.fail("stack.compose", "compose.yaml missing — restore would refuse")
	}
	res.add(volumeListCheck(manifest, res.Counts.Volumes))
	if res.Passed {
		res.Summary = fmt.Sprintf("%d files, %d volume(s), archive intact", len(res.Counts.StackFiles), len(res.Counts.Volumes))
	}
}

func volumeListCheck(m *stackManifest, found []string) VerifyCheck {
	if m == nil {
		return VerifyCheck{Name: "volumes", Status: "warn",
			Message: "archive has no volume list (made by an older version); completeness not checked"}
	}
	have := make(map[string]bool, len(found))
	for _, v := range found {
		have[v] = true
	}
	var missing []string
	for _, v := range m.Volumes {
		if !have[v] {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return VerifyCheck{Name: "volumes", Status: "fail", Message: "missing volume(s): " + strings.Join(missing, ", ")}
	}
	return VerifyCheck{Name: "volumes", Status: "ok", Message: fmt.Sprintf("all %d volume(s) present", len(m.Volumes))}
}

// verifyVolumeArchive checks a volume-typed run: the archive should be
// a valid gzipped tar. No structural rules beyond that — volume contents
// are application-specific.
func verifyVolumeArchive(res *VerifyResult, r io.Reader) {
	n, entries, err := walkInnerTar(r)
	res.Counts.Bytes = n
	res.setNeed("", n, entries)
	if err != nil {
		res.fail("tar", err.Error())
		return
	}
	res.add(VerifyCheck{Name: "tar", Status: "ok", Message: fmt.Sprintf("%d bytes", n)})
	if res.Passed {
		res.Summary = fmt.Sprintf("volume archive intact (%d bytes)", n)
	}
}

// walkInnerTar gunzip-walks a tar.gz stream, discarding payloads but
// counting bytes. Returns the total uncompressed payload bytes and the
// entries seen, and any decode error.
func walkInnerTar(r io.Reader) (int64, int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, entries, err
		}
		entries++
		n, err := io.Copy(io.Discard, tr)
		total += n
		if err != nil {
			return total, entries, err
		}
	}
	return total, entries, nil
}

// shaCheck compares the recomputed hash with the stored one. An empty
// stored hash means the run predates the field.
func shaCheck(got, expected string) VerifyCheck {
	switch {
	case expected == "":
		return VerifyCheck{Name: "sha256", Status: "warn", Message: "no stored hash to compare"}
	case got == expected:
		return VerifyCheck{Name: "sha256", Status: "ok", Message: got}
	default:
		return VerifyCheck{Name: "sha256", Status: "fail", Message: fmt.Sprintf("got %s, stored %s", got, expected)}
	}
}
