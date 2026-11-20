package swarmtask

import (
	"errors"
	"fmt"
	"testing"
)

func TestService(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"task container", map[string]string{LabelServiceID: "x1y2z3", LabelServiceName: "web"}, "web"},
		{"name label missing", map[string]string{LabelServiceID: "x1y2z3"}, "x1y2z3"},
		{"name without id", map[string]string{LabelServiceName: "web"}, ""},
		{"compose container", map[string]string{"com.docker.compose.project": "app"}, ""},
		{"no labels", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Service(tt.labels); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRefuseAndAsManaged(t *testing.T) {
	tests := []struct {
		name    string
		labels  map[string]string
		wantSvc string
	}{
		{"task container", map[string]string{LabelServiceID: "x1", LabelServiceName: "web"}, "web"},
		{"plain container", map[string]string{"a": "b"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Refuse(tt.labels)
			if (err != nil) != (tt.wantSvc != "") {
				t.Fatalf("Refuse = %v, want refusal %v", err, tt.wantSvc != "")
			}
			svc, ok := AsManaged(fmt.Errorf("wrapped: %w", err))
			if ok != (tt.wantSvc != "") || svc != tt.wantSvc {
				t.Errorf("AsManaged = %q, %v; want %q", svc, ok, tt.wantSvc)
			}
		})
	}
	if _, ok := AsManaged(errors.New("other")); ok {
		t.Error("AsManaged matched an unrelated error")
	}
}
