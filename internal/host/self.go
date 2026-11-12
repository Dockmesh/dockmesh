package host

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/moby/moby/client"
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

// DetectSelfContainer finds the container this process runs in. Docker
// bind-mounts /etc/hostname, /etc/hosts and /etc/resolv.conf from
// /var/lib/docker/containers/<id>, cgroup v1 paths carry the id, and the
// default hostname is its first 12 characters. Only those mount points
// count: a process on the host also sees every container's shm mount
// below that directory. Each candidate is confirmed against the daemon.
func DetectSelfContainer(ctx context.Context, cli client.ContainerAPIClient) string {
	if cli == nil {
		return ""
	}
	var candidates []string
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		candidates = append(candidates, mountinfoContainerIDs(string(b))...)
	}
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		candidates = append(candidates, containerIDPattern.FindAllString(string(b), -1)...)
	}
	if hn, err := os.Hostname(); err == nil && shortIDPattern.MatchString(hn) {
		candidates = append(candidates, hn)
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		res, err := cli.ContainerInspect(ctx, c, client.ContainerInspectOptions{})
		if err == nil && strings.HasPrefix(res.Container.ID, c) {
			return res.Container.ID
		}
	}
	return ""
}

var shortIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// selfMountPoints are the files Docker mounts into every container from
// the container's own directory.
var selfMountPoints = map[string]bool{"/etc/hostname": true, "/etc/hosts": true, "/etc/resolv.conf": true}

// mountinfoContainerIDs returns the ids of "containers/<id>" directories
// mounted at one of selfMountPoints.
func mountinfoContainerIDs(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !selfMountPoints[f[4]] {
			continue
		}
		for _, id := range containerIDPattern.FindAllString(f[3], -1) {
			if strings.Contains(f[3], "/containers/"+id) {
				out = append(out, id)
			}
		}
	}
	return out
}
