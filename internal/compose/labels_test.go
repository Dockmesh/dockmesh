package compose

import (
	"strings"
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"
)

func TestCheckReservedLabels(t *testing.T) {
	tests := []struct {
		name    string
		service composetypes.Labels
		network composetypes.Labels
		volume  composetypes.Labels
		wantErr string
	}{
		{name: "no labels"},
		{name: "own labels", service: composetypes.Labels{"traefik.enable": "true", "com.example.team": "web"}},
		{name: "other compose labels", service: composetypes.Labels{"com.docker.compose.oneoff": "False"}},
		{name: "tracking labels repeated unchanged", service: composetypes.Labels{
			LabelProject: "shop", LabelService: "web", LabelManaged: "true"}},
		{name: "service claims another project", service: composetypes.Labels{LabelProject: "billing"},
			wantErr: `service "web": label "com.docker.compose.project"`},
		{name: "service claims another service", service: composetypes.Labels{LabelService: "db"},
			wantErr: `service "web": label "com.docker.compose.service"`},
		{name: "service unsets managed", service: composetypes.Labels{LabelManaged: "false"},
			wantErr: `label "dockmesh.managed"`},
		{name: "service poses as a Dockmesh component", service: composetypes.Labels{"dockmesh.component": "proxy"},
			wantErr: `service "web": label "dockmesh.component"`},
		{name: "network claims another project", network: composetypes.Labels{LabelProject: "billing"},
			wantErr: `network "back": label "com.docker.compose.project"`},
		{name: "volume claims another project", volume: composetypes.Labels{LabelProject: "billing"},
			wantErr: `volume "data": label "com.docker.compose.project"`},
		{name: "volume with a dockmesh label", volume: composetypes.Labels{"dockmesh.proxy.spec": "x"},
			wantErr: `volume "data": label "dockmesh.proxy.spec"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := &composetypes.Project{
				Name:     "shop",
				Services: composetypes.Services{"web": {Name: "web", Labels: tt.service}},
				Networks: composetypes.Networks{"back": {Labels: tt.network}},
				Volumes:  composetypes.Volumes{"data": {Labels: tt.volume}},
			}
			err := CheckReservedLabels(proj)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "is reserved for Dockmesh") {
				t.Fatalf("error %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// The created container carries the tracking labels whatever the user
// labels are, and keeps the user's own ones.
func TestServiceLabelsKeepTracking(t *testing.T) {
	proj := &composetypes.Project{Name: "shop"}
	svc := composetypes.ServiceConfig{Name: "web", Image: "nginx", Labels: composetypes.Labels{"traefik.enable": "true", LabelProject: "shop"}}
	cfg, _, _, err := serviceToContainerConfig(proj, svc, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{LabelProject: "shop", LabelService: "web", LabelManaged: "true", "traefik.enable": "true"}
	for k, v := range want {
		if cfg.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, cfg.Labels[k], v)
		}
	}
	svc.Labels = composetypes.Labels{LabelProject: "billing"}
	if _, _, _, err := serviceToContainerConfig(proj, svc, nil); err == nil {
		t.Error("a service claiming another project got a container config")
	}
}
