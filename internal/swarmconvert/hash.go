package swarmconvert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"

	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// SpecHash is the dockmesh.spec-hash of spec: the SHA-256 of its JSON
// without the replica count and without the label itself, so scaling a
// service is not a change to what Dockmesh deployed.
func SpecHash(spec swarm.ServiceSpec) string {
	spec.Labels = maps.Clone(spec.Labels)
	delete(spec.Labels, agents.LabelSpecHash)
	if r := spec.Mode.Replicated; r != nil {
		spec.Mode.Replicated = &swarm.ReplicatedService{}
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// WithSpecHash returns spec labelled with its SpecHash.
func WithSpecHash(spec swarm.ServiceSpec) swarm.ServiceSpec {
	h := SpecHash(spec)
	spec.Labels = maps.Clone(spec.Labels)
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	spec.Labels[agents.LabelSpecHash] = h
	return spec
}
