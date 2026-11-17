package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const testToken = "tok_0123456789abcdef"

func TestEnrollOnStart(t *testing.T) {
	rejected := fmt.Errorf("%w: enroll http 401", errEnrollRejected)
	dialErr := errors.New("dial")
	tests := []struct {
		name string
		// enrolled puts an old pair in the data dir; tokenFile is the
		// content of enroll-token, "" for none.
		enrolled bool
		// pendingPair leaves a complete .next pair and ca.crt, as an
		// enrollment that stopped before the rename does.
		pendingPair bool
		tokenFile   string
		envToken    string
		enrollURL   string
		enrollErr   error
		// wantToken is the token enroll is called with, "" for no call.
		wantToken    string
		wantErr      error
		wantFileGone bool
		wantOldCert  bool
		wantFileKept bool
		wantNewCert  bool
		tokenSymlink bool
	}{
		{name: "token file enrolls", tokenFile: testToken, envToken: "envtoken_0123456789", enrollURL: "https://s/enroll",
			wantToken: testToken, wantFileGone: true, wantNewCert: true},
		{name: "env token enrolls without a file", envToken: "envtoken_0123456789", enrollURL: "https://s/enroll",
			wantToken: "envtoken_0123456789", wantNewCert: true},
		{name: "no token stops", enrollURL: "https://s/enroll", wantErr: errNotEnrolled},
		{name: "no enroll url stops", tokenFile: testToken, wantErr: errNotEnrolled, wantFileKept: true},
		{name: "rejected token is removed", tokenFile: testToken, enrollURL: "https://s/enroll", enrollErr: rejected,
			wantToken: testToken, wantErr: errEnrollRejected, wantFileGone: true},
		{name: "network error keeps the token", tokenFile: testToken, enrollURL: "https://s/enroll", enrollErr: dialErr,
			wantToken: testToken, wantErr: dialErr, wantFileKept: true},
		{name: "enrolled without a file does nothing", enrolled: true, envToken: "envtoken_0123456789", enrollURL: "https://s/enroll",
			wantOldCert: true},
		{name: "token file re-enrolls", enrolled: true, tokenFile: testToken, enrollURL: "https://s/enroll",
			wantToken: testToken, wantFileGone: true, wantNewCert: true},
		{name: "rejected re-enroll keeps the old pair", enrolled: true, tokenFile: testToken, enrollURL: "https://s/enroll",
			enrollErr: rejected, wantToken: testToken, wantFileGone: true, wantOldCert: true},
		{name: "failed re-enroll keeps pair and token", enrolled: true, tokenFile: testToken, enrollURL: "https://s/enroll",
			enrollErr: dialErr, wantToken: testToken, wantFileKept: true, wantOldCert: true},
		{name: "pending pair from an interrupted enrollment is used", pendingPair: true, tokenFile: testToken, enrollURL: "https://s/enroll", enrollErr: rejected,
			wantToken: testToken, wantFileGone: true, wantNewCert: true},
		{name: "symlinked token file is ignored", tokenFile: testToken, tokenSymlink: true, enrollURL: "https://s/enroll",
			wantErr: errNotEnrolled, wantFileGone: true},
		{name: "malformed token file is ignored", tokenFile: "short", enrollURL: "https://s/enroll",
			wantErr: errNotEnrolled, wantFileGone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := dataDirEnrollPaths(dir)
			if tt.enrolled {
				for _, f := range []string{p.cert, p.key, p.ca} {
					writeFile(t, f, "old")
				}
			}
			if tt.pendingPair {
				writeFile(t, p.ca, "new")
				writeFile(t, p.key+".next", "new")
				writeFile(t, p.cert+".next", "new")
			}
			if tt.tokenFile != "" {
				if tt.tokenSymlink {
					target := filepath.Join(t.TempDir(), "elsewhere")
					writeFile(t, target, tt.tokenFile+"\n")
					if err := os.Symlink(target, p.token); err != nil {
						t.Fatal(err)
					}
				} else {
					writeFile(t, p.token, tt.tokenFile+"\n")
				}
			}
			var called string
			fake := func(c agentConfig, p enrollPaths) error {
				called = c.token
				if tt.enrollErr != nil {
					return tt.enrollErr
				}
				for _, f := range []string{p.cert, p.key, p.ca} {
					writeFile(t, f, "new")
				}
				return nil
			}
			err := enrollOnStart(agentConfig{enrollURL: tt.enrollURL, token: tt.envToken}, p, fake)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("err = %v", err)
			case tt.wantErr != nil && err == nil:
				t.Fatalf("err = nil, want %v", tt.wantErr)
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if called != tt.wantToken {
				t.Errorf("enroll called with %q, want %q", called, tt.wantToken)
			}
			_, statErr := os.Lstat(p.token)
			if tt.wantFileGone && statErr == nil {
				t.Error("token file still there")
			}
			if tt.wantFileKept && statErr != nil {
				t.Errorf("token file gone: %v", statErr)
			}
			if tt.wantOldCert || tt.wantNewCert {
				b, err := os.ReadFile(p.cert)
				if err != nil {
					t.Fatal(err)
				}
				want := "new"
				if tt.wantOldCert {
					want = "old"
				}
				if string(b) != want {
					t.Errorf("cert = %q, want %q", b, want)
				}
			}
		})
	}
}

func TestReadEnrollToken(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		prepare func(p string)
		want    string
		wantErr bool
	}{
		{"missing", func(string) {}, "", false},
		{"token with newline", func(p string) { writeFile(t, p, testToken+"\n") }, testToken, false},
		{"empty", func(p string) { writeFile(t, p, "") }, "", true},
		{"bad characters", func(p string) { writeFile(t, p, "tok;rm -rf /;0123456789") }, "", true},
		{"directory", func(p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "", true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, fmt.Sprint(i))
			tt.prepare(p)
			got, err := readEnrollToken(p)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %q, %v; want %q, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
