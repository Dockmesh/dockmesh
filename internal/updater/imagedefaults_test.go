package updater

import (
	"reflect"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestStandaloneSpecUsesNewImageDefaults(t *testing.T) {
	oldImg := &dockerspec.DockerOCIImageConfig{
		ImageConfig: ocispec.ImageConfig{
			Env:        []string{"PATH=/usr/bin", "APP_VERSION=1.0"},
			Entrypoint: []string{"/entry-v1.sh"},
			Cmd:        []string{"serve", "--v1"},
			WorkingDir: "/app/v1",
			User:       "app",
			Labels:     map[string]string{"org.opencontainers.image.version": "1.0"},
		},
		DockerOCIImageConfigExt: dockerspec.DockerOCIImageConfigExt{
			Healthcheck: &container.HealthConfig{Test: []string{"CMD", "/v1-health"}, Interval: time.Second},
		},
	}
	newImg := &dockerspec.DockerOCIImageConfig{
		DockerOCIImageConfigExt: dockerspec.DockerOCIImageConfigExt{
			Healthcheck: &container.HealthConfig{Test: []string{"CMD", "/v2-health"}},
		},
	}
	inherited := container.Config{
		Image:       "app:1",
		Env:         []string{"PATH=/usr/bin", "APP_VERSION=1.0", "DB_HOST=db"},
		Entrypoint:  []string{"/entry-v1.sh"},
		Cmd:         []string{"serve", "--v1"},
		WorkingDir:  "/app/v1",
		User:        "app",
		Labels:      map[string]string{"org.opencontainers.image.version": "1.0", "team": "web"},
		Healthcheck: &container.HealthConfig{Test: []string{"CMD", "/v1-health"}, Interval: time.Second},
	}
	own := inherited
	own.Env = []string{"PATH=/usr/bin", "APP_VERSION=pinned", "DB_HOST=db"}
	own.Entrypoint = []string{"/custom.sh"}
	own.Cmd = []string{"serve", "--v1"}
	own.WorkingDir = "/srv"
	own.Healthcheck = &container.HealthConfig{Test: []string{"CMD", "/mine"}}

	tests := []struct {
		name       string
		cfg        container.Config
		oldImg     *dockerspec.DockerOCIImageConfig
		wantEnv    []string
		wantEntry  []string
		wantCmd    []string
		wantDir    string
		wantUser   string
		wantLabels map[string]string
		wantHealth []string
	}{
		{
			name: "inherited values dropped", cfg: inherited, oldImg: oldImg,
			wantEnv:    []string{"DB_HOST=db"},
			wantLabels: map[string]string{"team": "web"},
			wantHealth: []string{"CMD", "/v2-health"},
		},
		{
			name: "own values kept", cfg: own, oldImg: oldImg,
			wantEnv:    []string{"APP_VERSION=pinned", "DB_HOST=db"},
			wantEntry:  []string{"/custom.sh"},
			wantCmd:    []string{"serve", "--v1"},
			wantDir:    "/srv",
			wantLabels: map[string]string{"team": "web"},
			wantHealth: []string{"CMD", "/mine"},
		},
		{
			name: "old image unknown", cfg: inherited, oldImg: nil,
			wantEnv:    inherited.Env,
			wantEntry:  inherited.Entrypoint,
			wantCmd:    inherited.Cmd,
			wantDir:    inherited.WorkingDir,
			wantUser:   inherited.User,
			wantLabels: inherited.Labels,
			wantHealth: []string{"CMD", "/v1-health"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			info := &container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: "abc", HostConfig: &container.HostConfig{}},
				Config:            &cfg,
			}
			spec := standaloneSpec(info, "app:2", tt.oldImg, newImg)
			got := spec.cfg
			if got.Image != "app:2" {
				t.Errorf("image = %s", got.Image)
			}
			checks := []struct {
				field     string
				got, want any
			}{
				{"env", got.Env, tt.wantEnv},
				{"entrypoint", []string(got.Entrypoint), tt.wantEntry},
				{"cmd", []string(got.Cmd), tt.wantCmd},
				{"workdir", got.WorkingDir, tt.wantDir},
				{"user", got.User, tt.wantUser},
				{"labels", got.Labels, tt.wantLabels},
			}
			for _, c := range checks {
				if !reflect.DeepEqual(c.got, c.want) && !(isEmpty(c.got) && isEmpty(c.want)) {
					t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
				}
			}
			if spec.health == nil || spec.health.HealthCheck == nil ||
				!reflect.DeepEqual([]string(spec.health.HealthCheck.Test), tt.wantHealth) {
				t.Errorf("health = %+v, want test %v", spec.health, tt.wantHealth)
			}
			if !reflect.DeepEqual(cfg, tt.cfg) {
				t.Error("inspected config was modified")
			}
		})
	}
}

func isEmpty(v any) bool {
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Slice, reflect.Map, reflect.String:
		return r.Len() == 0
	}
	return v == nil
}
