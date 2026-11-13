package config

import (
	"path/filepath"
	"testing"
)

func TestResolvePaths(t *testing.T) {
	// oldLayout is the env file install.sh and `dockmesh init` wrote before
	// DOCKMESH_DATA_DIR existed: every path set, no data dir.
	oldLayout := func(dir string) map[string]string {
		return map[string]string{
			"DOCKMESH_DB_PATH":            dir + "/data/dockmesh.db",
			"DOCKMESH_STACKS_ROOT":        dir + "/stacks",
			"DOCKMESH_SECRETS_PATH":       dir + "/data/secrets.env",
			"DOCKMESH_SECRETS_KEY_PATH":   dir + "/data/secrets.age-key",
			"DOCKMESH_AUDIT_GENESIS_PATH": dir + "/data/audit-genesis.sha256",
		}
	}
	derived := func(dir string) Paths {
		return Paths{
			DataDir:          dir,
			DBPath:           dir + "/data/dockmesh.db",
			StacksRoot:       dir + "/stacks",
			SecretsPath:      dir + "/data/secrets.env",
			AuditGenesisPath: dir + "/data/audit-genesis.sha256",
			SecretsKeyPath:   dir + "/data/secrets.age-key",
			UpdateDir:        dir + "/update",
		}
	}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name string
		env  map[string]string
		want Paths
	}{
		{"nothing set", nil, derived("/var/lib/dockmesh")},
		{"data dir only", map[string]string{"DOCKMESH_DATA_DIR": "/srv/dm"}, derived("/srv/dm")},
		{"old layout default", oldLayout("/var/lib/dockmesh"), derived("/var/lib/dockmesh")},
		{"old layout /data", oldLayout("/data"), derived("/data")},
		{"old layout custom dir", oldLayout("/mnt/pool/dockmesh"), derived("/mnt/pool/dockmesh")},
		{"migrated old layout", with(oldLayout("/data"), "DOCKMESH_DATA_DIR", "/data"), derived("/data")},
		{"db path outside data/", map[string]string{"DOCKMESH_DB_PATH": "/opt/dm/dockmesh.db"}, Paths{
			DataDir:          "/opt/dm",
			DBPath:           "/opt/dm/dockmesh.db",
			StacksRoot:       "/opt/dm/stacks",
			SecretsPath:      "/opt/dm/data/secrets.env",
			AuditGenesisPath: "/opt/dm/data/audit-genesis.sha256",
			SecretsKeyPath:   "/opt/dm/data/secrets.age-key",
			UpdateDir:        "/opt/dm/update",
		}},
	}
	overrides := []struct {
		key   string
		field func(*Paths) *string
	}{
		{"DOCKMESH_DB_PATH", func(p *Paths) *string { return &p.DBPath }},
		{"DOCKMESH_STACKS_ROOT", func(p *Paths) *string { return &p.StacksRoot }},
		{"DOCKMESH_SECRETS_PATH", func(p *Paths) *string { return &p.SecretsPath }},
		{"DOCKMESH_SECRETS_KEY_PATH", func(p *Paths) *string { return &p.SecretsKeyPath }},
		{"DOCKMESH_AUDIT_GENESIS_PATH", func(p *Paths) *string { return &p.AuditGenesisPath }},
	}
	for _, o := range overrides {
		for _, base := range []struct {
			name string
			env  map[string]string
		}{
			{"new layout", map[string]string{"DOCKMESH_DATA_DIR": "/srv/dm"}},
			{"old layout", oldLayout("/data")},
		} {
			want := ResolvePaths(func(k string) string { return base.env[k] })
			*o.field(&want) = "/elsewhere/x"
			if o.key == "DOCKMESH_DB_PATH" && base.env["DOCKMESH_DATA_DIR"] == "" {
				want.DataDir = "/elsewhere"
				want.UpdateDir = "/elsewhere/update"
			}
			tests = append(tests, struct {
				name string
				env  map[string]string
				want Paths
			}{base.name + " " + o.key + " set", with(base.env, o.key, "/elsewhere/x"), want})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvePaths(func(k string) string { return tt.env[k] })
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestInferDataDir(t *testing.T) {
	tests := []struct{ db, want string }{
		{"", ""},
		{"/var/lib/dockmesh/data/dockmesh.db", "/var/lib/dockmesh"},
		{"/data/data/dockmesh.db", "/data"},
		{"/opt/dm/dockmesh.db", "/opt/dm"},
		{"rel/data/dockmesh.db", "rel"},
	}
	for _, tt := range tests {
		if got := InferDataDir(tt.db); got != filepath.FromSlash(tt.want) {
			t.Errorf("InferDataDir(%q) = %q, want %q", tt.db, got, tt.want)
		}
	}
}
