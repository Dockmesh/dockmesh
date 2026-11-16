package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/go-chi/chi/v5"
)

const channelCanary = "canary-7f3a9c"

func newChannelRouter(t *testing.T) (*chi.Mux, *notify.Service) {
	t.Helper()
	svc := notify.NewService(openRedactionTestDB(t))
	h := &Handlers{Notify: svc}
	r := chi.NewRouter()
	r.Get("/channels", h.ListNotificationChannels)
	r.Post("/channels", h.CreateNotificationChannel)
	r.Put("/channels/{id}", h.UpdateNotificationChannel)
	return r, svc
}

func channelCall(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload string
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = string(b)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(payload)))
	return rr
}

func storedConfig(t *testing.T, svc *notify.Service, id int64) map[string]any {
	t.Helper()
	for _, c := range svc.Channels() {
		if c.ID == id {
			var m map[string]any
			if err := json.Unmarshal(c.Config, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatalf("channel %d not stored", id)
	return nil
}

type channelResp struct {
	ID     int64          `json:"id"`
	Config map[string]any `json:"config"`
}

func TestChannelConfigNeverReturnsSecrets(t *testing.T) {
	extra := map[string]any{
		"secret":   channelCanary,
		"headers":  map[string]any{"Authorization": "Bearer " + channelCanary},
		"retries":  float64(3),
		"language": "de",
	}
	tests := []struct {
		typ    string
		public map[string]any
		secret map[string]any
	}{
		{"webhook", nil, map[string]any{"url": "https://hooks.example.com/" + channelCanary}},
		{"discord", nil, map[string]any{"url": "https://discord.example.com/" + channelCanary}},
		{"slack", nil, map[string]any{"url": "https://slack.example.com/" + channelCanary}},
		{"teams", nil, map[string]any{"url": "https://teams.example.com/" + channelCanary}},
		{"ntfy", map[string]any{"priority": float64(4)},
			map[string]any{"url": "https://ntfy.example.com/" + channelCanary, "token": channelCanary}},
		{"gotify", nil, map[string]any{"url": "https://gotify.example.com", "token": channelCanary}},
		{"email", map[string]any{"host": "smtp.example.com", "port": float64(587), "username": "alerts",
			"from": "alerts@example.com", "to": []any{"ops@example.com"}},
			map[string]any{"password": channelCanary}},
		{"pagerduty", map[string]any{"client": "Dockmesh", "client_url": "https://dockmesh.example.com"},
			map[string]any{"integration_key": channelCanary}},
		{"pushover", map[string]any{"device": "phone", "sound": "siren"},
			map[string]any{"app_token": channelCanary, "user_key": channelCanary + "u"}},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			r, svc := newChannelRouter(t)
			cfg := map[string]any{}
			for _, part := range []map[string]any{tt.public, tt.secret, extra} {
				for k, v := range part {
					cfg[k] = v
				}
			}
			in := map[string]any{"type": tt.typ, "name": "n", "config": cfg, "enabled": true}
			created := channelCall(t, r, http.MethodPost, "/channels", in)
			if created.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			list := channelCall(t, r, http.MethodGet, "/channels", nil)
			for name, rr := range map[string]*httptest.ResponseRecorder{"create": created, "list": list} {
				if strings.Contains(rr.Body.String(), channelCanary) {
					t.Fatalf("%s response leaks a secret: %s", name, rr.Body.String())
				}
			}
			var got channelResp
			if err := json.Unmarshal(created.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"language": "de"}
			for k, v := range tt.public {
				want[k] = v
			}
			for k := range cfg {
				if _, ok := want[k]; !ok {
					want[k] = ""
					want[k+"_set"] = true
				}
			}
			if !reflect.DeepEqual(got.Config, want) {
				t.Errorf("config = %v, want %v", got.Config, want)
			}

			// Sending the redacted object back keeps every stored value.
			path := "/channels/" + strconv.FormatInt(got.ID, 10)
			in["config"] = got.Config
			if rr := channelCall(t, r, http.MethodPut, path, in); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), channelCanary) {
				t.Fatalf("update with redacted config: %d %s", rr.Code, rr.Body.String())
			}
			if stored := storedConfig(t, svc, got.ID); !reflect.DeepEqual(stored, cfg) {
				t.Errorf("stored after round trip = %v, want %v", stored, cfg)
			}
		})
	}
}

func TestChannelUpdateKeepsAndReplacesSecrets(t *testing.T) {
	stored := map[string]any{"url": "https://hooks.example.com/a", "secret": channelCanary, "retries": float64(3), "language": "en"}
	tests := []struct {
		name     string
		config   map[string]any
		wantCode int
		want     map[string]any
	}{
		{"unknown fields omitted", map[string]any{"url": "", "language": "en"}, http.StatusOK, stored},
		{"unknown fields null", map[string]any{"url": "", "secret": nil, "retries": nil, "language": "en"}, http.StatusOK, stored},
		{"new non-string value", map[string]any{"url": "", "retries": float64(5), "language": "en"}, http.StatusOK,
			map[string]any{"url": "https://hooks.example.com/a", "secret": channelCanary, "retries": float64(5), "language": "en"}},
		{"changed url keeps secret", map[string]any{"url": "https://evil.example.com/", "language": "en"}, http.StatusBadRequest, stored},
		{"changed url with new secret", map[string]any{"url": "https://new.example.com/", "secret": "fresh", "retries": float64(3), "language": "en"}, http.StatusOK,
			map[string]any{"url": "https://new.example.com/", "secret": "fresh", "retries": float64(3), "language": "en"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, svc := newChannelRouter(t)
			created := channelCall(t, r, http.MethodPost, "/channels",
				map[string]any{"type": "webhook", "name": "n", "config": stored, "enabled": true})
			var c channelResp
			if err := json.Unmarshal(created.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			rr := channelCall(t, r, http.MethodPut, "/channels/"+strconv.FormatInt(c.ID, 10),
				map[string]any{"type": "webhook", "name": "n", "config": tt.config, "enabled": true})
			if rr.Code != tt.wantCode {
				t.Fatalf("update: %d, want %d: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), channelCanary) {
				t.Fatalf("update response leaks a secret: %s", rr.Body.String())
			}
			if got := storedConfig(t, svc, c.ID); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stored = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedactChannelNonObjectConfig(t *testing.T) {
	for _, raw := range []string{`"` + channelCanary + `"`, `["` + channelCanary + `"]`, `not json`} {
		c := redactChannel(&notify.Channel{Type: "webhook", Config: json.RawMessage(raw)})
		if string(c.Config) != "{}" {
			t.Errorf("config %s returned as %s, want {}", raw, c.Config)
		}
	}
}

func TestUpdateMissingChannel(t *testing.T) {
	r, _ := newChannelRouter(t)
	rr := channelCall(t, r, http.MethodPut, "/channels/42",
		map[string]any{"type": "webhook", "name": "n", "config": map[string]any{"url": "https://x.example.com"}})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("update of a missing channel: %d %s", rr.Code, rr.Body.String())
	}
}

func TestChannelRefusesFoldedFieldNames(t *testing.T) {
	stored := map[string]any{"host": "smtp.example.com", "port": float64(587), "username": "alerts",
		"password": channelCanary, "from": "a@example.com", "to": []any{"ops@example.com"}}
	tests := []struct {
		name  string
		extra map[string]any
	}{
		{"long s host", map[string]any{"hoſt": "evil.example.com"}},
		{"ascii Host", map[string]any{"Host": "evil.example.com"}},
		{"upper-case from", map[string]any{"FROM": "x@evil.example.com"}},
		{"kelvin sign", map[string]any{"Key": "x"}},
		{"case duplicates", map[string]any{"note": "a", "Note": "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, svc := newChannelRouter(t)
			bad := map[string]any{}
			for k, v := range stored {
				bad[k] = v
			}
			for k, v := range tt.extra {
				bad[k] = v
			}
			if rr := channelCall(t, r, http.MethodPost, "/channels",
				map[string]any{"type": "email", "name": "n", "config": bad}); rr.Code != http.StatusBadRequest {
				t.Fatalf("create: %d, want 400: %s", rr.Code, rr.Body.String())
			}
			created := channelCall(t, r, http.MethodPost, "/channels",
				map[string]any{"type": "email", "name": "n", "config": stored})
			var c channelResp
			if err := json.Unmarshal(created.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			update := map[string]any{}
			for k, v := range c.Config {
				update[k] = v
			}
			for k, v := range tt.extra {
				update[k] = v
			}
			rr := channelCall(t, r, http.MethodPut, "/channels/"+strconv.FormatInt(c.ID, 10),
				map[string]any{"type": "email", "name": "n", "config": update})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("update with kept password: %d, want 400: %s", rr.Code, rr.Body.String())
			}
			if got := storedConfig(t, svc, c.ID); !reflect.DeepEqual(got, stored) {
				t.Errorf("stored = %v, want %v", got, stored)
			}
		})
	}
}

func TestChannelSecretClearedByFalseMarker(t *testing.T) {
	r, svc := newChannelRouter(t)
	created := channelCall(t, r, http.MethodPost, "/channels", map[string]any{"type": "ntfy", "name": "n",
		"config": map[string]any{"url": "https://ntfy.example.com/t", "token": channelCanary}})
	var c channelResp
	if err := json.Unmarshal(created.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	rr := channelCall(t, r, http.MethodPut, "/channels/"+strconv.FormatInt(c.ID, 10), map[string]any{"type": "ntfy", "name": "n",
		"config": map[string]any{"url": "", "url_set": true, "token": "", "token_set": false}})
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	want := map[string]any{"url": "https://ntfy.example.com/t"}
	if got := storedConfig(t, svc, c.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("stored = %v, want %v", got, want)
	}
}

// Channels stored before field names were checked stay editable: the
// redacted variant comes back empty and is dropped on save.
func TestLegacyChannelVariantRoundTrip(t *testing.T) {
	base := map[string]any{"host": "smtp.example.com", "port": float64(587), "username": "alerts",
		"password": channelCanary, "from": "a@example.com", "to": []any{"ops@example.com"}}
	tests := []struct {
		name    string
		variant string
	}{
		{"ascii case variant", "Host"},
		{"long s", "hoſt"},
	}
	for _, tt := range tests {
		for _, echoAll := range []bool{false, true} {
			t.Run(tt.name+"/echo all="+strconv.FormatBool(echoAll), func(t *testing.T) {
				r, svc := newChannelRouter(t)
				legacy := map[string]any{tt.variant: "evil.example.com"}
				for k, v := range base {
					legacy[k] = v
				}
				raw, err := json.Marshal(legacy)
				if err != nil {
					t.Fatal(err)
				}
				c, err := svc.Create(t.Context(), notify.ChannelInput{Type: "email", Name: "n", Config: raw, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				var list []channelResp
				if err := json.Unmarshal(channelCall(t, r, http.MethodGet, "/channels", nil).Body.Bytes(), &list); err != nil {
					t.Fatal(err)
				}
				shown := list[0].Config
				if shown[tt.variant] != "" || shown[tt.variant+"_set"] != true {
					t.Fatalf("variant not redacted: %v", shown)
				}
				update := map[string]any{}
				for k, v := range shown {
					if echoAll || notify.PublicConfigField("email", k) || k == "password" {
						update[k] = v
					}
				}
				rr := channelCall(t, r, http.MethodPut, "/channels/"+strconv.FormatInt(c.ID, 10),
					map[string]any{"type": "email", "name": "n", "config": update, "enabled": true})
				if rr.Code != http.StatusOK {
					t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
				}
				if got := storedConfig(t, svc, c.ID); !reflect.DeepEqual(got, base) {
					t.Errorf("stored = %v, want %v", got, base)
				}
			})
		}
	}
}
