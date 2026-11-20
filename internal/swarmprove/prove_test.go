package swarmprove

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// fakeDocker serves the node files from a helper's mount and records what
// was done with the helper.
type fakeDocker struct {
	client.ImageAPIClient
	state   swarm.LocalNodeState
	root    string
	files   map[string][]byte
	created []client.ContainerCreateOptions
	removed []string
	started bool
}

func (f *fakeDocker) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return client.SystemInfoResult{Info: system.Info{DockerRootDir: f.root, Swarm: swarm.Info{NodeID: "n1", LocalNodeState: f.state}}}, nil
}

func (f *fakeDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{}, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.created = append(f.created, o)
	return client.ContainerCreateResult{ID: "helper1"}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.started = true
	return client.ContainerStartResult{}, nil
}

func (f *fakeDocker) CopyFromContainer(_ context.Context, _ string, o client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	name := path.Base(o.SourcePath)
	data, ok := f.files[name]
	if !ok {
		return client.CopyFromContainerResult{}, errors.New("no such file")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(data)
	_ = tw.Close()
	return client.CopyFromContainerResult{Content: io.NopCloser(&buf)}, nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.removed = append(f.removed, id)
	return client.ContainerRemoveResult{}, nil
}

func nodeFiles(t *testing.T) (crt, key []byte, pub *ecdsa.PublicKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "n1"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	crt = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Headers: map[string]string{"kek-version": "0"}, Bytes: kd})
	return crt, key, &k.PublicKey
}

func req() agents.SwarmProveReq {
	return agents.SwarmProveReq{Nonce: bytes.Repeat([]byte{7}, agents.ProofNonceSize), ServerID: "srv", HostID: "h1"}
}

func TestProveSignsWithNodeKey(t *testing.T) {
	crt, key, pub := nodeFiles(t)
	f := &fakeDocker{state: swarm.LocalNodeStateActive, root: "/var/lib/docker", files: map[string][]byte{"swarm-node.crt": crt, "swarm-node.key": key}}
	got, err := Prove(context.Background(), f, req())
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := agents.ProofMessage(req())
	d := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(pub, d[:], got.Signature) {
		t.Fatal("signature does not verify")
	}
	if got.CertPEM != string(crt) || strings.Contains(got.CertPEM, "PRIVATE KEY") {
		t.Fatalf("bundle %q", got.CertPEM)
	}
	if f.started {
		t.Fatal("helper was started")
	}
	if len(f.created) != 1 || len(f.removed) != 1 {
		t.Fatalf("created %d removed %d", len(f.created), len(f.removed))
	}
	o := f.created[0]
	mnt := o.HostConfig.Mounts
	if len(mnt) != 1 || mnt[0].Source != "/var/lib/docker/swarm/certificates" || !mnt[0].ReadOnly || o.HostConfig.NetworkMode != "none" || o.Config.Image != Image {
		t.Fatalf("helper %+v %+v", o.Config, o.HostConfig)
	}
}

func TestProveRefuses(t *testing.T) {
	crt, key, _ := nodeFiles(t)
	locked := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00"}, Bytes: []byte{1, 2}})
	tests := []struct {
		name  string
		state swarm.LocalNodeState
		root  string
		files map[string][]byte
		req   agents.SwarmProveReq
		want  error
		touch bool
	}{
		{name: "not in a Swarm", state: swarm.LocalNodeStateInactive, root: "/var/lib/docker", want: ErrNoSwarm},
		{name: "short nonce", state: swarm.LocalNodeStateActive, root: "/var/lib/docker", req: agents.SwarmProveReq{Nonce: []byte{1}, ServerID: "s", HostID: "h"}, want: agents.ErrBadProofRequest},
		{name: "relative root", state: swarm.LocalNodeStateActive, root: "docker"},
		{name: "autolock", state: swarm.LocalNodeStateActive, root: "/var/lib/docker", files: map[string][]byte{"swarm-node.crt": crt, "swarm-node.key": locked}, want: ErrKeyLocked, touch: true},
		{name: "no key", state: swarm.LocalNodeStateActive, root: "/var/lib/docker", files: map[string][]byte{"swarm-node.crt": crt}, touch: true},
		{name: "key only", state: swarm.LocalNodeStateActive, root: "/var/lib/docker", files: map[string][]byte{"swarm-node.crt": key, "swarm-node.key": key}, touch: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDocker{state: tc.state, root: tc.root, files: tc.files}
			r := tc.req
			if r.HostID == "" {
				r = req()
			}
			_, err := Prove(context.Background(), f, r)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("error leaks key material: %v", err)
			}
			if (len(f.created) > 0) != tc.touch || len(f.created) != len(f.removed) || f.started {
				t.Fatalf("created %d removed %d started %v", len(f.created), len(f.removed), f.started)
			}
		})
	}
}

func TestZeroClearsScalar(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	words := k.D.Bits()
	zero(k)
	for _, w := range words[:cap(words)] {
		if w != 0 {
			t.Fatal("scalar words left")
		}
	}
}
