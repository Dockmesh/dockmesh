package host

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/docker/docker/client"
)

var selfContainerID atomic.Value

// SelfContainerID is the id of the container the Dockmesh server runs in
// on the local Docker host, "" when it runs outside a container or the
// container wasn't found.
func SelfContainerID() string {
	id, _ := selfContainerID.Load().(string)
	return id
}

// SetSelfContainerID records the server's own container id.
func SetSelfContainerID(id string) { selfContainerID.Store(id) }

var containerIDPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// DetectSelfContainer finds the container this process runs in: Docker
// mounts /etc/hostname and friends from /var/lib/docker/containers/<id>,
// cgroup v1 paths carry the id, and the default hostname is its first 12
// characters. Each candidate is confirmed against the daemon, so a guess
// never names some other container.
func DetectSelfContainer(ctx context.Context, cli client.ContainerAPIClient) string {
	if cli == nil {
		return ""
	}
	var candidates []string
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		candidates = append(candidates, containerIDCandidates(string(b), true)...)
	}
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		candidates = append(candidates, containerIDCandidates(string(b), false)...)
	}
	if hn, err := os.Hostname(); err == nil && len(hn) == 12 {
		candidates = append(candidates, hn)
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		info, err := cli.ContainerInspect(ctx, c)
		if err == nil && strings.HasPrefix(info.ID, c) {
			return info.ID
		}
	}
	return ""
}

// containerIDCandidates returns the container ids named in mountinfo or
// cgroup content. With underContainers only ids of a "containers/<id>"
// directory count: mountinfo also lists image layer ids.
func containerIDCandidates(content string, underContainers bool) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		for _, id := range containerIDPattern.FindAllString(line, -1) {
			if !underContainers || strings.Contains(line, "/containers/"+id) {
				out = append(out, id)
			}
		}
	}
	return out
}
