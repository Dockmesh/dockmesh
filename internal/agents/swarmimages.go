package agents

import "github.com/distribution/reference"

// MaxSwarmImageChecks bounds one image check.
const MaxSwarmImageChecks = 200

// SwarmImageCheckReq asks a manager for the registry digest of each
// image, with the registry credentials the server holds for it.
type SwarmImageCheckReq struct {
	Images []SwarmImageRef `json:"images"`
}

type SwarmImageRef struct {
	Image        string `json:"image"`
	RegistryAuth string `json:"registry_auth,omitempty"`
}

type SwarmImageCheckResp struct {
	Results []SwarmImageDigest `json:"results"`
}

// SwarmImageDigest is the digest the registry has for Image now, or why
// it couldn't be read.
type SwarmImageDigest struct {
	Image  string `json:"image"`
	Digest string `json:"digest,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Validate checks an image check request: image references without a
// digest, each once.
func (r SwarmImageCheckReq) Validate() error {
	if len(r.Images) == 0 || len(r.Images) > MaxSwarmImageChecks {
		return refuse("an image check carries 1 to %d images", MaxSwarmImageChecks)
	}
	seen := map[string]bool{}
	for _, i := range r.Images {
		if len(i.Image) > 512 {
			return refuse("invalid image reference")
		}
		named, err := reference.ParseNormalizedNamed(i.Image)
		if err != nil {
			return refuse("invalid image reference")
		}
		if _, pinned := named.(reference.Canonical); pinned {
			return refuse("image %s carries a digest", i.Image)
		}
		if len(i.RegistryAuth) > 64<<10 {
			return refuse("registry auth of %s too long", i.Image)
		}
		if seen[i.Image] {
			return refuse("image %s listed twice", i.Image)
		}
		seen[i.Image] = true
	}
	return nil
}
