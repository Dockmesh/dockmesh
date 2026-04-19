package compose

import (
	"testing"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
)

func durPtr(d time.Duration) *composetypes.Duration {
	cd := composetypes.Duration(d)
	return &cd
}

func retryPtr(n uint64) *uint64 { return &n }

func TestHealthWaitTimeout(t *testing.T) {
	cases := []struct {
		name string
		svc  *composetypes.ServiceConfig
		want time.Duration
	}{
		{"nil service", nil, fallbackWait},
		{"no healthcheck", &composetypes.ServiceConfig{}, fallbackWait},
		{
			"disabled healthcheck",
			&composetypes.ServiceConfig{HealthCheck: &composetypes.HealthCheckConfig{Disable: true}},
			fallbackWait,
		},
		{
			"empty test",
			&composetypes.ServiceConfig{HealthCheck: &composetypes.HealthCheckConfig{Test: nil}},
			fallbackWait,
		},
		{
			"start_period + interval*retries",
			&composetypes.ServiceConfig{HealthCheck: &composetypes.HealthCheckConfig{
				Test:        composetypes.HealthCheckTest{"CMD", "ok"},
				StartPeriod: durPtr(5 * time.Second),
				Interval:    durPtr(2 * time.Second),
				Retries:     retryPtr(5),
			}},
			5*time.Second + 2*time.Second*5 + healthWaitMargin,
		},
		{
			"default retries when zero",
			&composetypes.ServiceConfig{HealthCheck: &composetypes.HealthCheckConfig{
				Test:     composetypes.HealthCheckTest{"CMD", "ok"},
				Interval: durPtr(1 * time.Second),
			}},
			1*time.Second*3 + healthWaitMargin,
		},
		{
			"zero budget falls back to 60s",
			&composetypes.ServiceConfig{HealthCheck: &composetypes.HealthCheckConfig{
				Test: composetypes.HealthCheckTest{"CMD", "ok"},
			}},
			60*time.Second + healthWaitMargin,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthWaitTimeout(tc.svc)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestComposeHealthToDocker(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if composeHealthToDocker(nil) != nil {
			t.Fatal("want nil")
		}
	})
	t.Run("disabled maps to NONE sentinel", func(t *testing.T) {
		got := composeHealthToDocker(&composetypes.HealthCheckConfig{Disable: true})
		if got == nil || len(got.Test) != 1 || got.Test[0] != "NONE" {
			t.Fatalf("got %+v, want {Test:[NONE]}", got)
		}
	})
	t.Run("empty test returns nil (inherit image)", func(t *testing.T) {
		if composeHealthToDocker(&composetypes.HealthCheckConfig{}) != nil {
			t.Fatal("want nil so docker inherits image HEALTHCHECK")
		}
	})
	t.Run("full translation", func(t *testing.T) {
		got := composeHealthToDocker(&composetypes.HealthCheckConfig{
			Test:          composetypes.HealthCheckTest{"CMD", "curl", "-f", "http://localhost"},
			Interval:      durPtr(10 * time.Second),
			Timeout:       durPtr(3 * time.Second),
			StartPeriod:   durPtr(5 * time.Second),
			StartInterval: durPtr(1 * time.Second),
			Retries:       retryPtr(4),
		})
		if got == nil {
			t.Fatal("want non-nil")
		}
		if got.Interval != 10*time.Second || got.Timeout != 3*time.Second ||
			got.StartPeriod != 5*time.Second || got.StartInterval != 1*time.Second {
			t.Fatalf("durations: %+v", got)
		}
		if got.Retries != 4 {
			t.Fatalf("retries %d want 4", got.Retries)
		}
		if len(got.Test) != 4 || got.Test[0] != "CMD" {
			t.Fatalf("test %v", got.Test)
		}
	})
}
