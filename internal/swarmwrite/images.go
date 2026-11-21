package swarmwrite

import (
	"context"
	"errors"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// DistributionAPI is the registry lookup an image check uses.
type DistributionAPI interface {
	infoAPI
	DistributionInspect(ctx context.Context, imageRef string, options client.DistributionInspectOptions) (client.DistributionInspectResult, error)
}

// imageCheckTimeout bounds the lookup of one image.
var imageCheckTimeout = 30 * time.Second

// CheckImages reads the registry digest of each image the way the daemon
// resolves it when a service is updated, so the result compares with the
// digest a service spec is pinned to.
func CheckImages(ctx context.Context, cli DistributionAPI, req agents.SwarmImageCheckReq) (agents.SwarmImageCheckResp, error) {
	if err := req.Validate(); err != nil {
		return agents.SwarmImageCheckResp{}, err
	}
	if err := requireManager(ctx, cli); err != nil {
		return agents.SwarmImageCheckResp{}, err
	}
	out := agents.SwarmImageCheckResp{Results: []agents.SwarmImageDigest{}}
	for _, ref := range req.Images {
		c, cancel := context.WithTimeout(ctx, imageCheckTimeout)
		res, err := cli.DistributionInspect(c, ref.Image, client.DistributionInspectOptions{EncodedRegistryAuth: ref.RegistryAuth})
		cancel()
		d := agents.SwarmImageDigest{Image: ref.Image}
		if err != nil {
			d.Error = imageCheckReason(err)
		} else {
			d.Digest = string(res.Descriptor.Digest)
		}
		out.Results = append(out.Results, d)
	}
	return out, nil
}

// imageCheckReason names why a lookup failed in a few words; the daemon's
// text can carry internal registry addresses.
func imageCheckReason(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err) || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "denied"):
		return "the registry refused the credentials"
	case cerrdefs.IsNotFound(err) || strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "not found"):
		return "the registry has no such image or tag"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout"):
		return "the registry did not answer in time"
	}
	return "the registry could not be reached"
}
