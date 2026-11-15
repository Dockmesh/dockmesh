package targets

import (
	"encoding/json"
	"reflect"
	"testing"
)

// roundTrip stores a location the way a run row does.
func roundTrip(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLocation(t *testing.T) {
	SetEmptyPathDir("/var/lib/dockmesh/backups")
	t.Cleanup(func() { SetEmptyPathDir("") })
	cases := []struct {
		name string
		typ  string
		cfg  map[string]any
		want map[string]any
	}{
		{"credentials dropped", "sftp",
			map[string]any{"host": "nas", "port": 22, "username": "u", "password": "pw", "private_key": "k", "passphrase": "p",
				"path": "/b", "host_key": "ssh-ed25519 AAA", "host_key_pending": "ssh-ed25519 BBB"},
			map[string]any{"host": "nas", "port": float64(22), "username": "u", "path": "/b", "host_key": "ssh-ed25519 AAA"}},
		{"s3 secret dropped", "s3",
			map[string]any{"endpoint": "s3", "bucket": "b", "prefix": "p", "access_key": "AK", "secret_key": "SK"},
			map[string]any{"endpoint": "s3", "bucket": "b", "prefix": "p", "access_key": "AK"}},
		{"unset local path resolved", "local", map[string]any{}, map[string]any{"path": "/var/lib/dockmesh/backups"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Location(tc.typ, tc.cfg); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameLocation(t *testing.T) {
	s3 := map[string]any{"endpoint": "s3", "bucket": "b", "prefix": "p", "access_key": "AK", "use_ssl": true}
	sftp := map[string]any{"host": "nas", "port": 22, "username": "u", "path": "/b"}
	with := func(m map[string]any, k string, v any) map[string]any {
		out := map[string]any{}
		for kk, vv := range m {
			out[kk] = vv
		}
		out[k] = v
		return out
	}
	cases := []struct {
		name string
		typ  string
		a, b map[string]any
		same bool
	}{
		{"read back from JSON", "s3", s3, roundTrip(t, s3), true},
		{"s3 prefix changed", "s3", s3, with(s3, "prefix", "q"), false},
		{"s3 bucket changed", "s3", s3, with(s3, "bucket", "c"), false},
		{"s3 access key changed", "s3", s3, with(s3, "access_key", "AK2"), true},
		{"sftp path changed", "sftp", sftp, with(sftp, "path", "/c"), false},
		{"sftp host changed", "sftp", sftp, with(sftp, "host", "nas2"), false},
		{"sftp user changed", "sftp", sftp, with(sftp, "username", "v"), true},
		{"sftp host key recorded later", "sftp", sftp, with(sftp, "host_key", "ssh-ed25519 AAA"), true},
		{"empty string and absent", "smb", map[string]any{"host": "h", "path": ""}, map[string]any{"host": "h"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameLocation(tc.typ, tc.a, tc.b); got != tc.same {
				t.Fatalf("SameLocation = %v, want %v", got, tc.same)
			}
		})
	}
}

func TestAtLocation(t *testing.T) {
	cur := map[string]any{"host": "new-nas", "port": 22, "username": "u", "password": "pw", "path": "/new",
		"host_key": "ssh-ed25519 NEW", "host_key_pending": "ssh-ed25519 PEND"}
	cases := []struct {
		name string
		loc  map[string]any
		want map[string]any
	}{
		{"other server: its own recorded key", map[string]any{"host": "old-nas", "port": float64(22), "username": "old", "path": "/old", "host_key": "ssh-ed25519 OLD"},
			map[string]any{"host": "old-nas", "port": float64(22), "username": "u", "password": "pw", "path": "/old", "host_key": "ssh-ed25519 OLD"}},
		{"other server without a recorded key", map[string]any{"host": "old-nas", "path": "/old"},
			map[string]any{"host": "old-nas", "username": "u", "password": "pw", "path": "/old"}},
		{"same server: the target's key", map[string]any{"host": "new-nas", "port": float64(22), "path": "/old"},
			map[string]any{"host": "new-nas", "port": float64(22), "username": "u", "password": "pw", "path": "/old",
				"host_key": "ssh-ed25519 NEW", "host_key_pending": "ssh-ed25519 PEND"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AtLocation("sftp", tc.loc, cur); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %v\nwant %v", got, tc.want)
			}
		})
	}

	s3 := AtLocation("s3", map[string]any{"endpoint": "s3", "bucket": "b", "prefix": "old"},
		map[string]any{"endpoint": "s3", "bucket": "b", "prefix": "new", "access_key": "AK", "secret_key": "SK"})
	if s3["prefix"] != "old" || s3["secret_key"] != "SK" {
		t.Fatalf("s3 at old prefix: %v", s3)
	}
}
