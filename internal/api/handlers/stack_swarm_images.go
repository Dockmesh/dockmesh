package handlers

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type swarmImageOps interface {
	SwarmCheckImages(ctx context.Context, hostID string, req agents.SwarmImageCheckReq) (agents.SwarmImageCheckResp, error)
}

func (h *Handlers) swarmImages() swarmImageOps {
	if a, ok := h.swarm().(swarmImageOps); ok {
		return a
	}
	return h.Hosts
}

type swarmImageStatus struct {
	Service string `json:"service"`
	// Image is the reference the service was deployed with, without the
	// digest it is pinned to.
	Image          string `json:"image"`
	RunningDigest  string `json:"running_digest,omitempty"`
	RegistryDigest string `json:"registry_digest,omitempty"`
	Update         bool   `json:"update"`
	Error          string `json:"error,omitempty"`
}

type swarmImageCheckResponse struct {
	Stack     string             `json:"stack"`
	ClusterID string             `json:"cluster_id"`
	Manager   string             `json:"manager"`
	Services  []swarmImageStatus `json:"services"`
}

// splitPinned splits "nginx:1.29@sha256:…" into the reference and digest.
func splitPinned(image string) (string, string) {
	ref, digest, _ := strings.Cut(image, "@")
	return ref, digest
}

// SwarmImageUpdates compares the digest each service of a stack on a
// cluster is pinned to with the digest the registry has now, read through
// a verified manager with the registry credentials Dockmesh holds for it.
// One check per stack runs at a time: each lookup counts against the
// registry's pull quota.
//
//	GET /api/v1/stacks/{name}/swarm-image-updates
func (h *Handlers) SwarmImageUpdates(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil || h.Deployments == nil || h.Hosts == nil && h.SwarmOps == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	name := chi.URLParam(r, "name")
	d, err := h.Deployments.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load stack deployment failed")
		return
	}
	if d == nil || d.TargetKind != stacks.TargetCluster {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stack " + name + " is not deployed to a Swarm cluster", "code": "stack_not_on_cluster"})
		return
	}
	clusterID := d.TargetID
	res := h.stackTargetReq(stacks.Target{Kind: stacks.TargetCluster, ID: clusterID}, name)
	if !middleware.AllowedOn(r.Context(), rbac.PermStacksDeploy, res) {
		h.writeScopeDenied(w, r, rbac.PermStacksDeploy, res, "stack "+name)
		return
	}
	release, err := h.StackLocks.TryLock(stacklock.Key{Target: stacklock.Target{Kind: "image-check"}, Stack: clusterID + "/" + name}, "image check")
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	defer release()
	reader, err := h.Clusters.ReadHost(r.Context(), clusterID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	list, err := h.swarm().SwarmServices(r.Context(), reader)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	out := swarmImageCheckResponse{Stack: name, ClusterID: clusterID, Services: []swarmImageStatus{}}
	var refs []string
	for _, s := range list.Services {
		if s.Stack != name || s.SpecHash == "" {
			continue
		}
		ref, digest := splitPinned(s.Image)
		out.Services = append(out.Services, swarmImageStatus{
			Service: strings.TrimPrefix(s.Name, name+"_"), Image: ref, RunningDigest: digest,
		})
		if ref != "" && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(refs) > agents.MaxSwarmImageChecks {
		refs = refs[:agents.MaxSwarmImageChecks]
	}
	managers, err := h.Clusters.WriteHosts(r.Context(), clusterID)
	if err != nil {
		writeSwarmWriteError(w, err)
		return
	}
	var resp agents.SwarmImageCheckResp
	for _, m := range managers {
		auth, aerr := h.swarm().RegistryAuthFor(r.Context(), m, refs)
		if aerr != nil {
			writeError(w, http.StatusInternalServerError, "resolving registry credentials failed")
			return
		}
		req := agents.SwarmImageCheckReq{}
		for _, ref := range refs {
			req.Images = append(req.Images, agents.SwarmImageRef{Image: ref, RegistryAuth: auth[ref]})
		}
		out.Manager = m
		resp, err = h.swarmImages().SwarmCheckImages(r.Context(), m, req)
		if !writeNotSent(err) {
			break
		}
	}
	if err != nil {
		writeSwarmWriteError(w, err)
		return
	}
	found := map[string]agents.SwarmImageDigest{}
	for _, x := range resp.Results {
		found[x.Image] = x
	}
	for i := range out.Services {
		s := &out.Services[i]
		x, ok := found[s.Image]
		switch {
		case !ok:
			s.Error = "not checked"
		case x.Error != "":
			s.Error = x.Error
		default:
			s.RegistryDigest = x.Digest
			s.Update = s.RunningDigest != "" && x.Digest != "" && x.Digest != s.RunningDigest
		}
	}
	writeJSON(w, http.StatusOK, out)
}
