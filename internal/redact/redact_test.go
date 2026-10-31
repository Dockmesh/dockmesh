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
			got, err := Keep(tt.next, stored, secrets)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Keep = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
	// Reusing the stored password against another server would hand it over.
	if _, err := Keep(map[string]any{"host": "attacker"}, stored, secrets); !errors.Is(err, ErrReenter) {
		t.Errorf("changed host with kept password: err = %v, want ErrReenter", err)
	}
	if _, err := Keep(map[string]any{"host": "attacker", "password": "typed"}, stored, secrets); err != nil {
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
