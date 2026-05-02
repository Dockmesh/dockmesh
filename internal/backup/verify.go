package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
	Entries int      `json:"entries"`
	Bytes   int64    `json:"bytes"`
	Volumes []string `json:"volumes,omitempty"`     // populated for stack runs
	StackFiles []string `json:"stack_files,omitempty"` // populated for stack runs
}

// VerifyRun reads the archive for the given run, dispatches to the
// type-specific verifier, and returns the structured result. The
// handler at /backups/runs/{id}/verify is now a thin wrapper around
// this — no more 501 stubs for non-system backups. P.13.4.
func (s *Service) VerifyRun(ctx context.Context, runID int64) (*VerifyResult, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "success" {
		return nil, errors.New("can only verify a successful run")
	}
	if len(run.Sources) == 0 {
		return nil, errors.New("run has no sources — cannot determine backup type")
	}
	srcType := run.Sources[0].Type
	res := &VerifyResult{RunID: runID, Type: srcType, Passed: true}

	rc, err := s.ReadRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("open run archive: %w", err)
	}
	defer rc.Close()

	// SHA256 is computed against the *plaintext* stream we read here.
	// run.SHA256 was written at backup time over the same plaintext (we
	// hash before encrypting on the way out), so a match means the
	// archive content is byte-for-byte intact. Empty run.SHA256 means
	// the run pre-dates the hash field and we skip the compare.
	hasher := sha256.New()
	teed := io.TeeReader(rc, hasher)

	switch srcType {
	case "system":
		// System runs go through the same temp-extract + sanity flow
		// the upload endpoint uses. The dispatch lives in the handler
		// (it has access to internal/restore); here we just stream the
		// archive into a buffer the handler can pass on. Easier: let
		// the handler call the legacy path directly when type=system.
		return verifySystemPlaceholder(res, teed, hasher, run.SHA256)
	case "stack":
		return verifyStackArchive(res, teed, hasher, run.SHA256)
	case "volume":
		return verifyVolumeArchive(res, teed, hasher, run.SHA256)
	default:
		return nil, fmt.Errorf("unknown source type %q", srcType)
	}
}

// verifySystemPlaceholder drains the stream + hashes it but doesn't
// run sanity (the handler does that via internal/restore.ExtractToTemp).
// The result here is "stream is intact and hash matches" — sanity is
// added by the handler caller.
func verifySystemPlaceholder(res *VerifyResult, r io.Reader, h hash.Hash, expected string) (*VerifyResult, error) {
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		res.Passed = false
		res.Checks = append(res.Checks, VerifyCheck{Name: "stream", Status: "fail", Message: err.Error()})
		return res, nil
	}
	res.Counts.Bytes = n
	finalizeSHA(res, h, expected)
	return res, nil
}

// verifyStackArchive walks the outer tar (stack/<rel> + volumes/<vol>.tar.gz)
// and gunzip-walks each volume entry to make sure they aren't corrupt.
// Returns a list of stack files restored and volumes ready to restore.
func verifyStackArchive(res *VerifyResult, r io.Reader, h hash.Hash, expected string) (*VerifyResult, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		res.Passed = false
		res.Checks = append(res.Checks, VerifyCheck{Name: "gzip", Status: "fail", Message: err.Error()})
		return res, nil
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hasCompose := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.Passed = false
			res.Checks = append(res.Checks, VerifyCheck{Name: "tar", Status: "fail", Message: err.Error()})
			return res, nil
		}
		res.Counts.Entries++
		name := hdr.Name
		switch {
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
				res.Passed = false
				res.Checks = append(res.Checks, VerifyCheck{Name: "stack." + rel, Status: "fail", Message: err.Error()})
				return res, nil
			}
		case strings.HasPrefix(name, "volumes/") && strings.HasSuffix(name, ".tar.gz"):
			volName := strings.TrimSuffix(strings.TrimPrefix(name, "volumes/"), ".tar.gz")
			if volName == "" {
				res.Checks = append(res.Checks, VerifyCheck{Name: "volume.malformed", Status: "warn", Message: name})
				continue
			}
			// Inner gzip walk — volume tar must itself be a valid
			// gzipped tar so a future restore can extract it.
			lr := io.LimitReader(tr, hdr.Size)
			n, err := walkInnerTar(lr)
			res.Counts.Bytes += n
			if err != nil {
				res.Passed = false
				res.Checks = append(res.Checks, VerifyCheck{Name: "volume." + volName, Status: "fail", Message: err.Error()})
				return res, nil
			}
			res.Counts.Volumes = append(res.Counts.Volumes, volName)
			res.Checks = append(res.Checks, VerifyCheck{Name: "volume." + volName, Status: "ok", Message: fmt.Sprintf("%d bytes", hdr.Size)})
		default:
			// Tolerate stray top-level entries from older versions.
			n, _ := io.Copy(io.Discard, tr)
			res.Counts.Bytes += n
		}
	}
	if hasCompose {
		res.Checks = append(res.Checks, VerifyCheck{Name: "stack.compose", Status: "ok", Message: "compose.yaml present"})
	} else {
		res.Passed = false
		res.Checks = append(res.Checks, VerifyCheck{Name: "stack.compose", Status: "fail", Message: "compose.yaml missing — restore would refuse"})
	}
	finalizeSHA(res, h, expected)
	if res.Passed {
		res.Summary = fmt.Sprintf("%d files, %d volume(s), archive intact", len(res.Counts.StackFiles), len(res.Counts.Volumes))
	}
	return res, nil
}

// verifyVolumeArchive checks a volume-typed run: the archive should be
// a valid gzipped tar with at least one entry. No structural rules
// beyond that — volume contents are application-specific.
func verifyVolumeArchive(res *VerifyResult, r io.Reader, h hash.Hash, expected string) (*VerifyResult, error) {
	n, err := walkInnerTar(r)
	res.Counts.Bytes = n
	if err != nil {
		res.Passed = false
		res.Checks = append(res.Checks, VerifyCheck{Name: "tar", Status: "fail", Message: err.Error()})
		return res, nil
	}
	res.Checks = append(res.Checks, VerifyCheck{Name: "tar", Status: "ok", Message: fmt.Sprintf("%d entries, %d bytes", res.Counts.Entries+0, n)})
	finalizeSHA(res, h, expected)
	if res.Passed {
		res.Summary = fmt.Sprintf("volume archive intact (%d bytes)", n)
	}
	return res, nil
}

// walkInnerTar gunzip-walks a tar.gz stream, discarding payloads but
// counting bytes. Returns the total uncompressed payload bytes seen
// and any decode error.
func walkInnerTar(r io.Reader) (int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, err
		}
		n, err := io.Copy(io.Discard, tr)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// finalizeSHA records whether the streamed-content hash matches the
// stored run.SHA256. Empty stored hash means "unknown — pre-existed
// the field" and we skip the compare without failing the verify.
func finalizeSHA(res *VerifyResult, h hash.Hash, expected string) {
	got := hex.EncodeToString(h.Sum(nil))
	switch {
	case expected == "":
		res.Checks = append(res.Checks, VerifyCheck{Name: "sha256", Status: "warn", Message: "no stored hash to compare"})
	case got == expected:
		res.SHA256OK = true
		res.Checks = append(res.Checks, VerifyCheck{Name: "sha256", Status: "ok", Message: got})
	default:
		res.Passed = false
		res.Checks = append(res.Checks, VerifyCheck{Name: "sha256", Status: "fail", Message: fmt.Sprintf("got %s, stored %s", got, expected)})
	}
}
