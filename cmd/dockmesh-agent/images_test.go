package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/dockmesh/dockmesh/internal/agents"
)

type fakeImageAPI struct {
	client.ImageAPIClient
	body    string
	err     error
	gotRef  string
	gotAuth string
	saved   []string
}

func (f *fakeImageAPI) ImagePull(_ context.Context, ref string, o image.PullOptions) (io.ReadCloser, error) {
	f.gotRef, f.gotAuth = ref, o.RegistryAuth
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func (f *fakeImageAPI) ImageSave(_ context.Context, refs []string, _ ...client.ImageSaveOption) (io.ReadCloser, error) {
	f.saved = refs
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestImageStreams(t *testing.T) {
	auth := "eyJwYXNzd29yZCI6Imh1bnRlcjIifQ==" // {"password":"hunter2"}
	cases := []struct {
		name     string
		params   map[string]any
		api      fakeImageAPI
		wantErr  string
		wantData string
	}{
		{
			name:     "pull relays progress with the request's credential",
			params:   map[string]any{"image": "reg.test/app:1", "registry_auth": auth},
			api:      fakeImageAPI{body: "{\"status\":\"ok\"}\n"},
			wantData: "{\"status\":\"ok\"}\n",
		},
		{
			name:    "pull failure masks the credential",
			params:  map[string]any{"image": "reg.test/app:1", "registry_auth": auth},
			api:     fakeImageAPI{err: errors.New("login failed for hunter2")},
			wantErr: "login failed for ",
		},
		{name: "pull needs an image", params: map[string]any{}, wantErr: "image required"},
		{
			name:     "export sends the archive",
			params:   map[string]any{"image": "nginx:1"},
			api:      fakeImageAPI{body: "tar"},
			wantData: "tar",
		},
		{name: "export needs an image", params: map[string]any{}, wantErr: "image required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, frames := wsPair(t)
			reg := newReg(t, 16)
			open := agents.StreamOpen{StreamID: reg.id, Params: tc.params}
			var err error
			if strings.HasPrefix(tc.name, "export") {
				err = runImageExportStream(conn, &tc.api, open, reg)
			} else {
				err = runImagePullStream(conn, &tc.api, open, reg)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("credential leaked: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var sd agents.StreamData
			if jerr := json.Unmarshal(recvFrame(t, frames).Payload, &sd); jerr != nil {
				t.Fatal(jerr)
			}
			if !bytes.Equal(sd.Data, []byte(tc.wantData)) {
				t.Fatalf("data %q, want %q", sd.Data, tc.wantData)
			}
			if tc.params["registry_auth"] != nil && tc.api.gotAuth != auth {
				t.Fatalf("auth = %q", tc.api.gotAuth)
			}
		})
	}
}
