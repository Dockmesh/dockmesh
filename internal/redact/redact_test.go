package redact

import (
	"encoding/json"
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
		{"field omitted", map[string]any{"host": "nas2"}, map[string]any{"host": "nas2", "password": "hunter2", "token": ""}},
		{"new value", map[string]any{"host": "nas", "password": "new"}, map[string]any{"host": "nas", "password": "new", "token": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Keep(tt.next, stored, secrets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Keep = %v, want %v", got, tt.want)
			}
		})
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
