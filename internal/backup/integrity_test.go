package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

var errReadPastEnd = errors.New("invalid response error: unverified packet returned")

// readTarget serves one stored archive through a configurable reader.
type readTarget struct {
	targets.Target
	stored []byte
	reader func(stored []byte) io.Reader
}

func (t *readTarget) Read(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(t.reader(t.stored)), nil
}

// failAtEnd delivers every byte and then fails instead of returning
// io.EOF, the way the SMB target behaved against ksmbd.
func failAtEnd(b []byte) io.Reader {
	return io.MultiReader(bytes.NewReader(b), errReader{errReadPastEnd})
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func encrypted(t *testing.T, plain []byte) ([]byte, *age.X25519Identity) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), id
}

func TestArchiveIntegrity(t *testing.T) {
	// One full age chunk plus a short last one, like a small volume archive.
	plain := make([]byte, 87000)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	stored, id := encrypted(t, plain)
	flipped := bytes.Clone(stored)
	flipped[len(flipped)/2] ^= 0xff

	tests := []struct {
		name    string
		served  []byte
		reader  func([]byte) io.Reader
		wantErr bool
	}{
		{"intact", stored, func(b []byte) io.Reader { return bytes.NewReader(b) }, false},
		{"one byte at a time", stored, func(b []byte) io.Reader { return &oneByteReader{b: b} }, false},
		{"read error after the last byte", stored, failAtEnd, true},
		{"truncated", stored[:len(stored)-100], func(b []byte) io.Reader { return bytes.NewReader(b) }, true},
		{"flipped byte", flipped, func(b []byte) io.Reader { return bytes.NewReader(b) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := &readTarget{stored: tt.served, reader: tt.reader}
			run := &Run{TargetPath: "jobs/a/x.tar.gz.age", SizeBytes: int64(len(stored)), SHA256: sha(stored)}

			err := readBack(context.Background(), tg, run.TargetPath, run.SizeBytes, run.SHA256)
			if (err != nil) != tt.wantErr {
				t.Fatalf("readBack err = %v, want error %v", err, tt.wantErr)
			}

			rc, err := fetchArchive(context.Background(), tg, run)
			if tt.wantErr {
				if err == nil {
					rc.Close()
					t.Fatal("fetchArchive accepted an archive that differs from the run")
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchArchive: %v", err)
			}
			defer rc.Close()
			dec, err := age.Decrypt(rc, id)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(dec)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("restored %d bytes that differ from the %d backed up", len(got), len(plain))
			}
		})
	}
}

func TestFetchArchiveWithoutRecordedHash(t *testing.T) {
	stored := []byte("archive from a run that predates the hash column")
	tg := &readTarget{stored: stored, reader: func(b []byte) io.Reader { return bytes.NewReader(b) }}
	rc, err := fetchArchive(context.Background(), tg, &Run{TargetPath: "old.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, stored) {
		t.Fatalf("got %q, %v", got, err)
	}

	tg.reader = failAtEnd
	if rc, err := fetchArchive(context.Background(), tg, &Run{TargetPath: "old.tar.gz"}); err == nil {
		rc.Close()
		t.Fatal("a read error was ignored when no hash was recorded")
	}
}

type oneByteReader struct {
	b   []byte
	off int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.b[r.off]
	r.off++
	return 1, nil
}

// storeEncryptedVolumeRun writes an encrypted volume archive the way the
// executor does and records a successful run for it.
func storeEncryptedVolumeRun(t *testing.T, svc *Service, dir string) (*Run, string) {
	t.Helper()
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, JobInput{
		Name: "enc", TargetType: "local", TargetConfig: map[string]any{"path": dir},
		Sources: []Source{{Type: "volume", Name: "data"}}, Encrypt: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	body := make([]byte, 80000)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "f.bin", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	tg, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		t.Fatal(err)
	}
	const path = "jobs/enc/run.tar.gz.age"
	w, err := tg.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	tee := newTeeWriter(w, h)
	enc, err := wrapEncrypt(tee, svc.secrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(tgz.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	runID, err := svc.store.startRun(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.finishRun(ctx, runID, "success", tee.size, path, hex.EncodeToString(h.Sum(nil)), nil); err != nil {
		t.Fatal(err)
	}
	run, err := svc.store.getRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return run, filepath.Join(dir, filepath.FromSlash(path))
}

func TestVerifyEncryptedVolumeRun(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, file string)
		want    bool
	}{
		{"intact", func(*testing.T, string) {}, true},
		{"last chunk altered", func(t *testing.T, file string) {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			b[len(b)-20] ^= 0xff
			if err := os.WriteFile(file, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			targets.SetLocalRoots(func() []string { return []string{dir} })
			t.Cleanup(func() { targets.SetLocalRoots(nil) })
			svc, _ := newTestService(t, true)
			run, file := storeEncryptedVolumeRun(t, svc, dir)
			tt.corrupt(t, file)

			res, err := svc.VerifyRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if res.Passed != tt.want || res.SHA256OK != tt.want {
				t.Fatalf("passed=%v sha256_ok=%v, want %v: %+v", res.Passed, res.SHA256OK, tt.want, res.Checks)
			}
		})
	}
}
