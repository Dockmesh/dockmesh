package handlers

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/redact"
)

func TestBackupTargetFieldsMatchConfigs(t *testing.T) {
	seen := map[string]bool{}
	for _, cfg := range []any{targets.LocalConfig{}, targets.S3Config{}, targets.SFTPConfig{}, targets.SMBConfig{}, targets.WebDAVConfig{}} {
		rt := reflect.TypeOf(cfg)
		for i := range rt.NumField() {
			if name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
				seen[name] = true
			}
		}
	}
	var want []string
	for k := range seen {
		want = append(want, k)
	}
	sort.Strings(want)
	got := append([]string{}, backupTargetFields...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backupTargetFields = %v, want %v", got, want)
	}
}

func TestBackupTargetRefusesFoldedFieldNames(t *testing.T) {
	sftp := map[string]any{"host": "nas", "port": float64(22), "username": "u", "password": "s3cret", "host_key": "ssh-ed25519 AAAA"}
	smb := map[string]any{"host": "nas", "share": "bk", "username": "u", "password": "s3cret"}
	s3 := map[string]any{"endpoint": "s3.example.com", "bucket": "b", "access_key": "AK", "secret_key": "s3cret"}
	tests := []struct {
		name  string
		prev  map[string]any
		next  map[string]any
		check bool
	}{
		{"sftp long s host", sftp, map[string]any{"host": "nas", "port": float64(22), "username": "u", "password": "", "host_key": "", "hoſt": "evil"}, false},
		{"smb long s share", smb, map[string]any{"host": "nas", "share": "bk", "username": "u", "password": "", "ſhare": "evil"}, false},
		{"s3 kelvin access key", s3, map[string]any{"endpoint": "s3.example.com", "bucket": "b", "access_key": "AK", "secret_key": "", "access_Key": "evil"}, false},
		{"sftp ascii Host", sftp, map[string]any{"host": "nas", "port": float64(22), "username": "u", "password": "", "Host": "evil"}, false},
		{"sftp upper-case host key", sftp, map[string]any{"host": "nas", "port": float64(22), "username": "u", "password": "", "HOST_KEY": "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := cleanConfig(tt.next, backupTargetFields); !errors.Is(err, redact.ErrFieldName) {
				t.Errorf("cleanConfig = %v, want ErrFieldName", err)
			}
			if tt.check {
				return
			}
			if _, err := keepConfig(tt.next, tt.prev, backupTargetSecrets, backupTargetFields); !errors.Is(err, redact.ErrFieldName) {
				t.Errorf("keepConfig = %v, want ErrFieldName", err)
			}
			in := &backup.JobInput{TargetType: "smb", TargetConfig: tt.next}
			if err := resolveJobTargetSecrets(in, &backup.Job{TargetType: "smb", TargetConfig: tt.prev}); !errors.Is(err, redact.ErrFieldName) {
				t.Errorf("resolveJobTargetSecrets = %v, want ErrFieldName", err)
			}
		})
	}
}
