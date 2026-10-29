package updater

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

var rollbackSuffix = regexp.MustCompile(`-?rollback-(\d+)$`)

// baseRef maps a rollback tag back to the ref it was taken from, so a
// container that was rolled back updates from its original tag again.
func baseRef(ref string) string {
	lastSlash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref[lastSlash+1:], ":")
	if colon < 0 {
		return ref
	}
	colon += lastSlash + 1
	tag := rollbackSuffix.ReplaceAllString(ref[colon+1:], "")
	if tag == ref[colon+1:] {
		return ref
	}
	if tag == "" {
		tag = "latest"
	}
	return ref[:colon+1] + tag
}

// repoDigestFor picks the digest of ref's repository from an image's
// RepoDigests. An image tagged in several repositories carries one
// digest per repository, and the first is not necessarily ref's.
func repoDigestFor(ref string, repoDigests []string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ""
	}
	for _, rd := range repoDigests {
		d, err := reference.ParseNormalizedNamed(rd)
		if err != nil {
			continue
		}
		canon, ok := d.(reference.Canonical)
		if !ok || d.Name() != named.Name() {
			continue
		}
		return canon.Digest().String()
	}
	return ""
}

// imageDigest is the registry digest of img for ref, or its local ID
// when it has none (built or loaded locally).
func imageDigest(img *dtypes.ImageInspect, ref string) string {
	if d := repoDigestFor(ref, img.RepoDigests); d != "" {
		return d
	}
	return img.ID
}

// rollbackTagsToPrune returns the rollback tags of ref beyond the keep
// newest, oldest first.
func rollbackTagsToPrune(ref string, tags []string, keep int) []string {
	prefix := rollbackPrefix(ref)
	type tagged struct {
		tag string
		ts  int64
	}
	var found []tagged
	for _, t := range tags {
		rest, ok := strings.CutPrefix(t, prefix)
		if !ok {
			continue
		}
		ts, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			continue
		}
		found = append(found, tagged{t, ts})
	}
	if len(found) <= keep {
		return nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ts < found[j].ts })
	out := make([]string, 0, len(found)-keep)
	for _, f := range found[:len(found)-keep] {
		out = append(out, f.tag)
	}
	return out
}

// pruneRollbackTags removes rollback tags of ref beyond the newest
// rollbackKeep. Removing a tag only untags; an image a container still
// runs is refused by the daemon and kept.
func (s *Service) pruneRollbackTags(ctx context.Context, cli *client.Client, ref string) {
	imgs, err := cli.ImageList(ctx, dtypes.ImageListOptions{})
	if err != nil {
		slog.Warn("updater: list images for rollback prune failed", "err", err)
		return
	}
	var tags []string
	for _, img := range imgs {
		tags = append(tags, img.RepoTags...)
	}
	for _, t := range rollbackTagsToPrune(ref, tags, s.rollbackKeep) {
		if _, err := cli.ImageRemove(ctx, t, dtypes.ImageRemoveOptions{}); err != nil {
			slog.Info("updater: rollback tag kept", "tag", t, "err", err)
			continue
		}
		slog.Info("updater: pruned rollback tag", "tag", t)
	}
}
