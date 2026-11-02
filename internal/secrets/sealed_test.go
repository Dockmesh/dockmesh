package secrets

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealString(t *testing.T) {
	on, err := New(filepath.Join(t.TempDir(), "k"), true)
	if err != nil {
		t.Fatal(err)
	}
	off, err := New("", false)
	if err != nil {
		t.Fatal(err)
	}
	var none *Service

	sealed, err := on.SealString("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, SealedPrefix) || strings.Contains(sealed, "hunter2") || !IsSealed(sealed) {
		t.Fatalf("sealed = %q", sealed)
	}

	tests := []struct {
		name    string
		svc     *Service
		stored  string
		want    string
		wantErr error
	}{
		{"sealed value opens", on, sealed, "hunter2", nil},
		{"legacy plaintext passes", on, "plain", "plain", nil},
		{"plaintext with prefix but not base64", on, "age:not base64!", "age:not base64!", nil},
		{"plaintext with prefix and base64", on, "age:Passw0rd", "age:Passw0rd", nil},
		{"plaintext with prefix and short base64", on, "age:test", "age:test", nil},
		{"empty", on, "", "", nil},
		{"disabled service refuses sealed value", off, sealed, "", ErrSealedButDisabled},
		{"nil service refuses sealed value", none, sealed, "", ErrSealedButDisabled},
		{"disabled service passes plaintext", off, "plain", "plain", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.svc.OpenString(tt.stored)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	for _, svc := range []*Service{off, none} {
		if got, err := svc.SealString("x"); err != nil || got != "x" {
			t.Errorf("seal without encryption = %q, %v", got, err)
		}
	}
	if got, _ := on.SealString(""); got != "" {
		t.Errorf("empty sealed to %q", got)
	}
}
