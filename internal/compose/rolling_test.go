package compose

import (
	"testing"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
)

func TestRollingOptionsDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   RollingOptions
		want RollingOptions
	}{
		{
			"zero value gets parallelism=1, stop-first, pause",
			RollingOptions{},
			RollingOptions{Parallelism: 1, Order: OrderStopFirst, FailureAction: FailurePause},
		},
		{
			"explicit values preserved",
			RollingOptions{Parallelism: 4, Delay: 2 * time.Second, Order: OrderStartFirst, FailureAction: FailureRollback},
			RollingOptions{Parallelism: 4, Delay: 2 * time.Second, Order: OrderStartFirst, FailureAction: FailureRollback},
		},
		{
			"negative parallelism clamped to 1",
			RollingOptions{Parallelism: -5},
			RollingOptions{Parallelism: 1, Order: OrderStopFirst, FailureAction: FailurePause},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Defaults()
			if got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestExtractUpdateOptions(t *testing.T) {
	t.Run("no deploy block", func(t *testing.T) {
		got := ExtractUpdateOptions(composetypes.ServiceConfig{})
		if got != (RollingOptions{}) {
			t.Fatalf("want zero, got %+v", got)
		}
	})

	t.Run("deploy without update_config", func(t *testing.T) {
		got := ExtractUpdateOptions(composetypes.ServiceConfig{Deploy: &composetypes.DeployConfig{}})
		if got != (RollingOptions{}) {
			t.Fatalf("want zero, got %+v", got)
		}
	})

	t.Run("full update_config", func(t *testing.T) {
		par := uint64(3)
		got := ExtractUpdateOptions(composetypes.ServiceConfig{
			Deploy: &composetypes.DeployConfig{
				UpdateConfig: &composetypes.UpdateConfig{
					Parallelism:   &par,
					Delay:         composetypes.Duration(10 * time.Second),
					Order:         "start-first",
					FailureAction: "rollback",
				},
			},
		})
		want := RollingOptions{
			Parallelism:   3,
			Delay:         10 * time.Second,
			Order:         OrderStartFirst,
			FailureAction: FailureRollback,
		}
		if got != want {
			t.Fatalf("got %+v want %+v", got, want)
		}
	})
}
