package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCallerRolesAndID(t *testing.T) {
	tests := []struct {
		name, body, wantID, wantRoles string
	}{
		{"global role", `{"id":"u1","assignments":[{"role":"admin","scope":{"type":"global"}}]}`, "u1", "admin"},
		{"scoped roles", `{"id":"u1","assignments":[{"role":"operator","scope":{"type":"host","host":"edge-1"}},{"role":"viewer","scope":{"type":"host_tag","value":"prod"}}]}`, "u1", "operator (host: edge-1), viewer (host_tag: prod)"},
		{"no assignments", `{"id":"u1","assignments":[]}`, "u1", "-"},
		{"api token", `{"kind":"api_token","username":"api-token","api_token_id":7}`, "7", "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var me map[string]any
			if err := json.Unmarshal([]byte(tt.body), &me); err != nil {
				t.Fatal(err)
			}
			if got := callerID(me); got != tt.wantID {
				t.Errorf("id = %q, want %q", got, tt.wantID)
			}
			if got := callerRoles(me); got != tt.wantRoles {
				t.Errorf("roles = %q, want %q", got, tt.wantRoles)
			}
		})
	}
}

func TestKeepInsecure(t *testing.T) {
	tests := []struct {
		name   string
		saved  *Config
		server string
		want   bool
	}{
		{"same server keeps it", &Config{Server: "https://a", Insecure: true}, "https://a", true},
		{"other server drops it", &Config{Server: "https://a", Insecure: true}, "https://b", false},
		{"never set", &Config{Server: "https://a"}, "https://a", false},
		{"no config", nil, "https://a", false},
	}
	for _, tt := range tests {
		if got := keepInsecure(tt.saved, tt.server); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLoginHelpNamesThisOSConfigPath(t *testing.T) {
	path, err := configPath()
	if err != nil {
		t.Skip(err)
	}
	if long := newLoginCmd().Long; !strings.Contains(long, path) {
		t.Fatalf("login --help doesn't name %s:\n%s", path, long)
	}
}

func TestBackupJobTypeComesFromTargetType(t *testing.T) {
	var j backupJob
	if err := json.Unmarshal([]byte(`{"id":1,"name":"n","target_type":"s3","schedule":"@daily","enabled":true}`), &j); err != nil {
		t.Fatal(err)
	}
	if j.Type != "s3" {
		t.Fatalf("TYPE = %q, want s3", j.Type)
	}
}
