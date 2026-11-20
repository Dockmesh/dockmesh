//go:build unix

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// managedServer serves signed releases on the agent listener's release
// path over TLS and points upgradeOrigin at it.
func managedServer(t *testing.T, tag, signTag string, archive []byte) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, err := releasesig.New(pub.String())
	if err != nil {
		t.Fatal(err)
	}
	oldVerifier := managedVerifier
	managedVerifier = func() *releasesig.Verifier { return v }
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + managedArchive() + "\n")
	files := map[string][]byte{
		releasesig.ChecksumsFile: checksums,
		releasesig.SignatureFile: minisign.SignWithComments(priv, checksums, releasesig.TrustedComment(signTag), ""),
		managedArchive():         archive,
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, agents.ReleasePath+tag+"/")
		if data, found := files[name]; ok && found {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	oldOrigin := upgradeOrigin
	upgradeOrigin.dialURL = "wss://" + srv.Listener.Addr().String() + "/ws"
	upgradeOrigin.tls = srv.Client().Transport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() {
		srv.Close()
		managedVerifier = oldVerifier
		upgradeOrigin = oldOrigin
	})
}

func setAgentVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func TestStageManaged(t *testing.T) {
	setAgentVersion(t, "v0.6.0")
	tests := []struct {
		name    string
		tag     string
		signTag string
		want    error
		staged  bool
	}{
		{"newer release", "v0.6.1", "v0.6.1", nil, true},
		{"signature of another tag", "v0.6.1", "v0.6.2", releasesig.ErrTag, false},
		{"same version", "v0.6.0", "v0.6.0", errSameBinary, false},
		{"older version", "v0.5.3", "v0.5.3", errNotNewer, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := t.TempDir()
			managedServer(t, tt.tag, tt.signTag, []byte("archive"))
			err := stageManaged(data, tt.tag)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("stageManaged = %v, want %v", err, tt.want)
			}
			raw, rerr := os.ReadFile(filepath.Join(updateDir(data), rootupdate.RequestFile))
			if tt.staged != (rerr == nil) {
				t.Fatalf("request written = %v, want %v", rerr == nil, tt.staged)
			}
			if !tt.staged {
				return
			}
			var req rootupdate.Request
			if err := json.Unmarshal(raw, &req); err != nil || req.Version != tt.tag {
				t.Errorf("request %s", raw)
			}
			if got, _ := os.ReadFile(filepath.Join(updateDir(data), managedArchive())); string(got) != "archive" {
				t.Errorf("archive %q", got)
			}
		})
	}
}

func TestStageManagedRefusesWhilePending(t *testing.T) {
	setAgentVersion(t, "v0.6.0")
	data := t.TempDir()
	managedServer(t, "v0.6.1", "v0.6.1", []byte("archive"))
	if err := os.MkdirAll(updateDir(data), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updateDir(data), rootupdate.PendingFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stageManaged(data, "v0.6.1"); !errors.Is(err, errUpgradeBusy) {
		t.Fatalf("stage while an update is pending: %v", err)
	}
}

func TestAgentServiceHealthy(t *testing.T) {
	data := t.TempDir()
	dir := updateDir(data)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	s := &agentService{reqDir: dir, uid: uint32(os.Getuid())}
	write := func(v string, mod time.Time) {
		p := filepath.Join(dir, runningFile)
		if err := os.WriteFile(p, []byte(v), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	s.started = time.Now()
	write("v0.6.1", s.started.Add(-time.Minute))
	if s.Healthy(t.Context(), "v0.6.1") {
		t.Error("a health file from before the start counted")
	}
	write("v0.6.0", s.started.Add(time.Second))
	if s.Healthy(t.Context(), "v0.6.1") {
		t.Error("the old version counted as healthy")
	}
	write("v0.6.1", s.started.Add(time.Second))
	if !s.Healthy(t.Context(), "v0.6.1") {
		t.Error("the new version did not count as healthy")
	}
	if err := os.Remove(filepath.Join(dir, runningFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, runningFile)); err != nil {
		t.Fatal(err)
	}
	if s.Healthy(t.Context(), "v0.6.1") {
		t.Error("followed a symlinked health file")
	}
}

func TestUpdateResultReportedUntilWelcomed(t *testing.T) {
	data := t.TempDir()
	if lastUpdateResult(data) != nil {
		t.Fatal("result without a file")
	}
	if err := os.MkdirAll(updateDir(data), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		status rootupdate.Status
		kept   bool
	}{
		{rootupdate.StatusRolledBack, false},
		{rootupdate.StatusFailed, true},
		{rootupdate.StatusUnconfirmed, true},
	} {
		b, _ := json.Marshal(rootupdate.Result{Status: tt.status, From: "v0.6.0", To: "v0.6.1", Reason: "health"})
		p := filepath.Join(updateDir(data), rootupdate.ResultFile)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		staged := filepath.Join(updateDir(data), managedArchive())
		if err := os.WriteFile(staged, []byte("archive"), 0o640); err != nil {
			t.Fatal(err)
		}
		res := lastUpdateResult(data)
		if res == nil || res.Status != string(tt.status) || res.To != "v0.6.1" {
			t.Fatalf("result %+v", res)
		}
		clearUpdateResult(data, res)
		if _, err := os.Stat(p); (err == nil) != tt.kept {
			t.Errorf("%s: result kept = %v, want %v", tt.status, err == nil, tt.kept)
		}
		if _, err := os.Stat(staged); (err == nil) != tt.kept {
			t.Errorf("%s: staged archive kept = %v, want %v", tt.status, err == nil, tt.kept)
		}
	}
}

func TestReleaseURL(t *testing.T) {
	got, err := releaseURL("wss://srv:8443/api/v1/agents/connect?x=1", "v0.6.1")
	if err != nil || got != "https://srv:8443"+agents.ReleasePath+"v0.6.1/" {
		t.Errorf("releaseURL = %q, %v", got, err)
	}
	if _, err := releaseURL("ws://srv:8080/connect", "v0.6.1"); err == nil {
		t.Error("plain ws accepted for a managed update")
	}
}

func TestLegacyCopyStep(t *testing.T) {
	const managedUnit = "[Service]\nUser=dockmesh-agent\nExecStart=/usr/local/bin/dockmesh-agent\n"
	tests := []struct {
		name    string
		unit    string
		script  bool
		dataBin bool
		legacy  bool
	}{
		{"managed unit", managedUnit, false, false, false},
		{"root copy step in the unit", "[Service]\nExecStartPre=-+/etc/dockmesh-agent/refresh-path-copy\n" + managedUnit, false, false, true},
		{"refresh script left", managedUnit, true, false, true},
		{"binary left in the data dir", managedUnit, false, true, true},
	}
	old := execStartPre
	execStartPre = func() (string, error) { return "", nil }
	t.Cleanup(func() { execStartPre = old })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			unit := filepath.Join(dir, "dockmesh-agent.service")
			script := filepath.Join(dir, "refresh-path-copy")
			if err := os.MkdirAll(filepath.Join(data, "bin"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unit, []byte(tt.unit), 0o644); err != nil {
				t.Fatal(err)
			}
			if tt.script {
				if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.dataBin {
				if err := os.WriteFile(filepath.Join(data, "bin", "dockmesh-agent"), []byte("x"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := legacyCopyStepAt(data, script, unit, false); (err != nil) != tt.legacy {
				t.Errorf("legacyCopyStepAt = %v, want legacy=%v", err, tt.legacy)
			}
		})
	}
	t.Run("ExecStartPre in a drop-in", func(t *testing.T) {
		execStartPre = func() (string, error) { return "{ path=/etc/dockmesh-agent/refresh-path-copy ; argv[]=… }", nil }
		dir := t.TempDir()
		unit := filepath.Join(dir, "dockmesh-agent.service")
		if err := os.WriteFile(unit, []byte(managedUnit), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := legacyCopyStepAt(dir, filepath.Join(dir, "none"), unit, false); err == nil {
			t.Error("ExecStartPre from systemd not seen")
		}
	})
	t.Run("systemd unreachable", func(t *testing.T) {
		execStartPre = func() (string, error) { return "", errors.New("no systemd") }
		dir := t.TempDir()
		unit := filepath.Join(dir, "dockmesh-agent.service")
		if err := os.WriteFile(unit, []byte(managedUnit), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := legacyCopyStepAt(dir, filepath.Join(dir, "none"), unit, true); err == nil {
			t.Error("root went on without asking systemd")
		}
		if err := legacyCopyStepAt(dir, filepath.Join(dir, "none"), unit, false); err != nil {
			t.Errorf("layout detection without systemd: %v", err)
		}
	})
}

func TestRefusedRequestFreesTheUpgradeLock(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(updateDir(data), 0o750); err != nil {
		t.Fatal(err)
	}
	if !upgradeMu.TryLock() {
		t.Fatal("upgrade lock already held")
	}
	b, _ := json.Marshal(rootupdate.Result{Status: rootupdate.StatusRefused, From: "v0.6.0", To: "v0.6.1", Reason: "verify"})
	if err := os.WriteFile(filepath.Join(updateDir(data), rootupdate.ResultFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { releaseWhenTaken(data); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lock still held after a refusal")
	}
	if !upgradeMu.TryLock() {
		t.Fatal("upgrade lock not freed")
	}
	upgradeMu.Unlock()
}
