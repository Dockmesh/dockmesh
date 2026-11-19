package setup

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyValuesNotifications(t *testing.T) {
	hook := NotificationValues{Type: "ntfy", Name: " Ops ", Config: json.RawMessage(`{"url":"https://ntfy.sh/dm","token":"tk_x"}`)}
	tests := []struct {
		name string
		n    []NotificationValues
		ok   bool
	}{
		{"none", nil, true},
		{"ntfy", []NotificationValues{hook}, true},
		{"unknown type", []NotificationValues{{Type: "fax", Config: json.RawMessage(`{}`)}}, false},
		{"foreign field", []NotificationValues{{Type: "ntfy", Config: json.RawMessage(`{"url":"https://n","cmd":"x"}`)}}, false},
		{"control in name", []NotificationValues{{Type: "ntfy", Name: "a\nb", Config: hook.Config}}, false},
		{"too many", func() []NotificationValues {
			var out []NotificationValues
			for range 11 {
				out = append(out, hook)
			}
			return out
		}(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validValues()
			v.Notifications = tt.n
			err := v.Validate()
			if (err == nil) != tt.ok {
				t.Fatalf("Validate: %v, want ok=%v", err, tt.ok)
			}
			if err != nil || len(v.Notifications) == 0 {
				return
			}
			if v.Notifications[0].Name != "Ops" {
				t.Errorf("name %q", v.Notifications[0].Name)
			}
			env, _ := v.ConfigEnv("/usr/local/share/dockmesh")
			if strings.Contains(string(env), "tk_x") {
				t.Error("a channel secret reached the config")
			}
		})
	}
	v := ApplyValues{DataDir: "/srv/dm", Restore: true, Notifications: []NotificationValues{hook}}
	if err := v.Validate(); err == nil {
		t.Error("a restore took notification channels")
	}
}
