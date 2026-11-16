package redact

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMapAndKeepRoundTrip(t *testing.T) {
	secrets := []string{"password", "token"}
	stored := map[string]any{"host": "nas", "password": "hunter2", "token": ""}

	shown := Map(stored, secrets)
	want := map[string]any{"host": "nas", "password": "", "password_set": true, "token": ""}
	if !reflect.DeepEqual(shown, want) {
		t.Fatalf("Map = %v, want %v", shown, want)
	}
	if stored["password"] != "hunter2" {
		t.Fatal("Map modified its input")
	}

	tests := []struct {
		name string
		next map[string]any
		want map[string]any
	}{
		{"sent back unchanged", shown, map[string]any{"host": "nas", "password": "hunter2", "token": ""}},
		{"field omitted", map[string]any{"host": "nas"}, map[string]any{"host": "nas", "password": "hunter2", "token": ""}},
		{"new value", map[string]any{"host": "nas", "password": "new"}, map[string]any{"host": "nas", "password": "new", "token": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Keep(tt.next, stored, secrets, secrets)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Keep = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
	// Reusing the stored password against another server would hand it over.
	if _, err := Keep(map[string]any{"host": "attacker"}, stored, secrets, secrets); !errors.Is(err, ErrReenter) {
		t.Errorf("changed host with kept password: err = %v, want ErrReenter", err)
	}
	if _, err := Keep(map[string]any{"host": "attacker", "password": "typed"}, stored, secrets, secrets); err != nil {
		t.Errorf("changed host with a new password: %v", err)
	}
}

func TestToMap(t *testing.T) {
	type cfg struct {
		Password string `json:"password"`
	}
	for _, in := range []any{cfg{"x"}, json.RawMessage(`{"password":"x"}`), map[string]any{"password": "x"}} {
		if got := ToMap(in); got["password"] != "x" {
			t.Errorf("ToMap(%T) = %v", in, got)
		}
	}
	if ToMap(json.RawMessage(`[1]`)) != nil || ToMap(nil) != nil {
		t.Error("non-objects must yield nil")
	}
}

func TestCheckKeys(t *testing.T) {
	known := []string{"password"}
	tests := []struct {
		name string
		cfg  map[string]any
		ok   bool
	}{
		{"canonical", map[string]any{"host": "a", "password": "b", "X-Custom": 1}, true},
		{"long s", map[string]any{"host": "a", "hoſt": "evil"}, false},
		{"kelvin sign", map[string]any{"access_Key": "evil"}, false},
		{"other non-ascii", map[string]any{"pässwort": "x"}, false},
		{"case variant of a bound name", map[string]any{"Host": "evil"}, false},
		{"case variant of a known name", map[string]any{"PASSWORD": "x"}, false},
		{"case duplicates of unknown names", map[string]any{"extra": 1, "EXTRA": 2}, false},
		{"empty", map[string]any{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckKeys(tt.cfg, known)
			if tt.ok != (err == nil) || (err != nil && !errors.Is(err, ErrFieldName)) {
				t.Errorf("CheckKeys = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestKeepFoldedNamesAndClear(t *testing.T) {
	secrets := []string{"password"}
	prev := map[string]any{"host": "nas", "share": "bk", "password": "hunter2"}
	tests := []struct {
		name    string
		next    map[string]any
		want    map[string]any
		wantErr error
	}{
		{"long s host with kept password", map[string]any{"host": "nas", "share": "bk", "password": "", "hoſt": "evil"}, nil, ErrFieldName},
		{"long s share", map[string]any{"host": "nas", "share": "bk", "ſhare": "evil"}, nil, ErrFieldName},
		{"ascii Host", map[string]any{"host": "nas", "share": "bk", "Host": "evil"}, nil, ErrFieldName},
		{"marker false clears", map[string]any{"host": "evil", "share": "bk", "password": "", "password_set": false},
			map[string]any{"host": "evil", "share": "bk"}, nil},
		{"marker true keeps", map[string]any{"host": "nas", "share": "bk", "password": "", "password_set": true}, prev, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Keep(tt.next, prev, secrets, secrets)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Keep = %v, want %v", got, tt.want)
			}
		})
	}
	// A stored name the decoder folds elsewhere isn't carried over.
	legacy := map[string]any{"host": "nas", "hoſt": "evil", "password": "hunter2"}
	got, err := Keep(map[string]any{"host": "nas"}, legacy, []string{"password", "hoſt"}, secrets)
	if err != nil || got["hoſt"] != nil || got["password"] != "hunter2" {
		t.Errorf("Keep with a legacy folded name = %v, %v", got, err)
	}
}

func TestKeepLegacyCaseVariant(t *testing.T) {
	fields := []string{"host", "password"}
	prev := map[string]any{"host": "nas", "Host": "old", "password": "hunter2"}
	want := map[string]any{"host": "nas", "password": "hunter2"}
	tests := []struct {
		name string
		next map[string]any
	}{
		{"canonical fields only", map[string]any{"host": "nas", "password": ""}},
		{"redacted variant echoed back", map[string]any{"host": "nas", "Host": "", "Host_set": true, "password": "", "password_set": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The variant is a secret for channel types that treat every
			// unknown field as one.
			got, err := Keep(tt.next, prev, []string{"password", "Host"}, fields)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Keep = %v, %v, want %v", got, err, want)
			}
		})
	}
	if _, err := Keep(map[string]any{"host": "nas", "Host": "evil"}, prev, []string{"password"}, fields); !errors.Is(err, ErrFieldName) {
		t.Errorf("variant with a value: err = %v, want ErrFieldName", err)
	}
}

func TestHasForeignKey(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{`{"host":"a","Host":"b"}`, false},
		{`{"ho\u017ft":"evil"}`, true},
		{`{"access_\u212aey":"x"}`, true},
		{`[1]`, false},
		{`not json`, false},
	}
	for _, tt := range tests {
		if got := HasForeignKey([]byte(tt.raw)); got != tt.want {
			t.Errorf("HasForeignKey(%s) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
