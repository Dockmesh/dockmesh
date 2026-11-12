package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/client"
)

type fakeImageAPI struct {
	client.ImageAPIClient
	body    string
	err     error
	gotRef  string
	gotAuth string
}

// fakePull is a pull response that is only read as a stream.
type fakePull struct {
	io.ReadCloser
	client.ImagePullResponse
}

func (p fakePull) Read(b []byte) (int, error) { return p.ReadCloser.Read(b) }
func (p fakePull) Close() error               { return p.ReadCloser.Close() }

func (f *fakeImageAPI) ImagePull(_ context.Context, ref string, o client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.gotRef, f.gotAuth = ref, o.RegistryAuth
	if f.err != nil {
		return nil, f.err
	}
	return fakePull{ReadCloser: io.NopCloser(strings.NewReader(f.body))}, nil
}

func (f *fakeImageAPI) ImageSave(_ context.Context, refs []string, _ ...client.ImageSaveOption) (client.ImageSaveResult, error) {
	f.gotRef = strings.Join(refs, ",")
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestImageStreams(t *testing.T) {
	auth := "eyJwYXNzd29yZCI6Imh1bnRlcjIifQ==" // {"password":"hunter2"}
	pull := func(api *fakeImageAPI, open agents.StreamOpen, reg *streamReg, conn *websocket.Conn) error {
		return runImagePullStream(conn, api, open, reg)
	}
	export := func(api *fakeImageAPI, open agents.StreamOpen, reg *streamReg, conn *websocket.Conn) error {
		return runImageExportStream(conn, api, open, reg)
	}
	cases := []struct {
		name     string
		run      func(*fakeImageAPI, agents.StreamOpen, *streamReg, *websocket.Conn) error
		params   map[string]any
		api      fakeImageAPI
		wantErr  string
		wantData string
		wantRef  string
	}{
		{
			name:     "pull relays progress with the request's credential",
			run:      pull,
			wantRef:  "reg.test/app:1",
			params:   map[string]any{"image": "reg.test/app:1", "registry_auth": auth},
			api:      fakeImageAPI{body: "{\"status\":\"ok\"}\n"},
			wantData: "{\"status\":\"ok\"}\n",
		},
		{
			name:    "pull failure masks the credential",
			run:     pull,
			params:  map[string]any{"image": "reg.test/app:1", "registry_auth": auth},
			api:     fakeImageAPI{err: errors.New("login failed for hunter2")},
			wantErr: "login failed for ",
		},
		{name: "pull needs an image", run: pull, params: map[string]any{}, wantErr: "image required"},
		{
			name:     "export sends the archive",
			run:      export,
			wantRef:  "nginx:1",
			params:   map[string]any{"image": "nginx:1"},
			api:      fakeImageAPI{body: "tar"},
			wantData: "tar",
		},
		{name: "export needs an image", run: export, params: map[string]any{}, wantErr: "image required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, frames := wsPair(t)
			reg := newReg(t, 16)
			open := agents.StreamOpen{StreamID: reg.id, Params: tc.params}
			err := tc.run(&tc.api, open, reg, conn)
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
			if tc.api.gotRef != tc.wantRef {
				t.Fatalf("ref = %q, want %q", tc.api.gotRef, tc.wantRef)
			}
		})
	}
}
