// Package swarmprove answers req.swarm.prove on an agent: it signs the
// server's challenge with the host's Swarm node key, which shows that this
// host's daemon is the node its certificate names.
//
// The key is only readable by root, so a helper container from the pinned
// busybox image gets the certificates directory bind-mounted read-only and
// the files are copied out of it. The helper is created, copied from and
// removed; it never runs. The key exists only in this process's memory for
// the signature and is overwritten right after.
package swarmprove

import (
	"archive/tar"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/docker"
)

// Image is the pinned helper image the other helpers use too.
const Image = docker.BusyboxHelperImage

// Component labels the helper container.
const Component = "swarm-proof-helper"

const (
	mountPoint = "/certs"
	certFile   = "swarm-node.crt"
	keyFile    = "swarm-node.key"
	// maxFile bounds what is read of either file.
	maxFile = 64 << 10
	// Timeout is below the server's request timeout.
	Timeout = 20 * time.Second
)

var (
	ErrNoSwarm = errors.New("this host is not an active Swarm node")
	// ErrKeyLocked: the Swarm uses autolock, so the key on disk is
	// encrypted and can't sign.
	ErrKeyLocked = errors.New("the Swarm node key is encrypted (autolock); it can't sign a proof")
	ErrBadKey    = errors.New("the Swarm node key has an unsupported format")
)

// API is the part of the Docker client a proof uses.
type API interface {
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
	client.ImageAPIClient
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	CopyFromContainer(ctx context.Context, id string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// Prove answers req. It refuses before touching any file when the request
// is malformed or the daemon is not an active Swarm node.
func Prove(ctx context.Context, cli API, req agents.SwarmProveReq) (agents.SwarmProveResp, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	msg, err := agents.ProofMessage(req)
	if err != nil {
		return agents.SwarmProveResp{}, err
	}
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return agents.SwarmProveResp{}, fmt.Errorf("read swarm state: %w", err)
	}
	if info.Info.Swarm.LocalNodeState != swarm.LocalNodeStateActive || info.Info.Swarm.NodeID == "" {
		return agents.SwarmProveResp{}, ErrNoSwarm
	}
	root := info.Info.DockerRootDir
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return agents.SwarmProveResp{}, fmt.Errorf("unexpected docker root dir %q", root)
	}
	certPEM, keyPEM, err := readFiles(ctx, cli, filepath.Join(root, "swarm", "certificates"))
	defer clear(keyPEM)
	if err != nil {
		return agents.SwarmProveResp{}, err
	}
	bundle := agents.CertificatesOnly(certPEM)
	if bundle == "" {
		return agents.SwarmProveResp{}, errors.New("the Swarm node certificate is missing or unreadable")
	}
	sig, err := sign(keyPEM, msg)
	if err != nil {
		return agents.SwarmProveResp{}, err
	}
	return agents.SwarmProveResp{CertPEM: bundle, Signature: sig}, nil
}

// readFiles copies the node certificate and key out of a helper that is
// never started. The caller clears the key bytes.
func readFiles(ctx context.Context, cli API, dir string) (cert, key []byte, err error) {
	if err := ensureImage(ctx, cli); err != nil {
		return nil, nil, err
	}
	resp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:           Image,
			Cmd:             []string{"true"},
			User:            "0:0",
			NetworkDisabled: true,
			Labels:          map[string]string{docker.LabelManaged: "true", "dockmesh.component": Component},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{
				Type: mount.TypeBind, Source: dir, Target: mountPoint, ReadOnly: true,
				BindOptions: &mount.BindOptions{NonRecursive: true},
			}},
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			LogConfig:      docker.HelperLogConfig,
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create proof helper: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.HelperTimeout)
		defer cancel()
		_, _ = cli.ContainerRemove(rctx, resp.ID, client.ContainerRemoveOptions{Force: true})
	}()
	cert, err = copyFile(ctx, cli, resp.ID, certFile)
	if err != nil {
		return nil, nil, err
	}
	key, err = copyFile(ctx, cli, resp.ID, keyFile)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// copyFile returns the one regular file name from the helper's mount. The
// archive's buffers are never pooled, so nothing of a key outlives the
// returned slice.
func copyFile(ctx context.Context, cli API, id, name string) ([]byte, error) {
	res, err := cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: path.Join(mountPoint, name)})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer res.Content.Close()
	tr := tar.NewReader(res.Content)
	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if hdr.Typeflag != tar.TypeReg || hdr.Name != name || hdr.Size <= 0 || hdr.Size > maxFile {
		return nil, fmt.Errorf("read %s: not a regular file of expected size", name)
	}
	buf := make([]byte, hdr.Size)
	if _, err := io.ReadFull(tr, buf); err != nil {
		clear(buf)
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return buf, nil
}

func ensureImage(ctx context.Context, cli API) error {
	if _, err := cli.ImageInspect(ctx, Image); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return err
	}
	if err := docker.PullAnonymous(ctx, cli, Image); err != nil {
		return fmt.Errorf("pull %s: %w", Image, err)
	}
	return nil
}

// sign signs msg with the PEM key and overwrites every copy of the key it
// made. Go can't promise that no copy is left in freed memory, but none
// is kept, logged or returned.
func sign(keyPEM, msg []byte) ([]byte, error) {
	blk, _ := pem.Decode(keyPEM)
	if blk == nil {
		return nil, ErrBadKey
	}
	defer clear(blk.Bytes)
	if blk.Headers["Proc-Type"] != "" || blk.Type == "ENCRYPTED PRIVATE KEY" {
		return nil, ErrKeyLocked
	}
	var key *ecdsa.PrivateKey
	switch blk.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			return nil, ErrBadKey
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, ErrBadKey
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, ErrBadKey
		}
		key = ek
	default:
		return nil, ErrBadKey
	}
	defer zero(key)
	digest := sha256.Sum256(msg)
	return key.Sign(rand.Reader, digest[:], crypto.SHA256)
}

// zero overwrites the private scalar in place.
func zero(k *ecdsa.PrivateKey) {
	if k == nil || k.D == nil {
		return
	}
	words := k.D.Bits()
	clear(words)
	k.D.SetInt64(0)
}
