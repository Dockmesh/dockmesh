package agents

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/moby/moby/api/types/system"
)

// DaemonInfo is what the Docker daemon on a host reports about itself.
// Agents send it in their hello; a nil value means the agent predates it
// and nothing is known.
type DaemonInfo struct {
	// OS and Arch use OCI platform names ("linux", "amd64", "arm/v7"),
	// the form images are published for.
	OS    string      `json:"os,omitempty"`
	Arch  string      `json:"arch,omitempty"`
	Swarm *SwarmState `json:"swarm,omitempty"`
}

// SwarmState is the host's local Swarm membership as the daemon sees it.
// NodeState "inactive" means the host is not part of a Swarm.
type SwarmState struct {
	NodeID    string `json:"node_id,omitempty"`
	NodeState string `json:"node_state,omitempty"`
	Role      string `json:"role,omitempty"`
	ClusterID string `json:"cluster_id,omitempty"`
}

// DaemonInfoFrom converts a docker info response.
func DaemonInfoFrom(info system.Info) *DaemonInfo {
	d := &DaemonInfo{
		OS:   strings.ToLower(info.OSType),
		Arch: NormalizeArch(info.Architecture),
	}
	sw := info.Swarm
	st := &SwarmState{NodeID: sw.NodeID, NodeState: string(sw.LocalNodeState)}
	if sw.LocalNodeState == "active" {
		st.Role = "worker"
		if sw.ControlAvailable {
			st.Role = "manager"
		}
		if sw.Cluster != nil {
			st.ClusterID = sw.Cluster.ID
		}
	}
	d.Swarm = st
	return d.Sanitize()
}

// NormalizeArch maps the kernel machine names dockerd reports (uname -m)
// to OCI platform architectures, so a daemon on "x86_64" and an image for
// "amd64" compare equal.
func NormalizeArch(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	switch a {
	case "x86_64", "x86-64", "amd64":
		return "amd64"
	case "aarch64", "arm64", "armv8", "armv8l", "arm64/v8":
		return "arm64"
	case "armv7", "armv7l", "armhf", "arm", "arm/v7":
		return "arm/v7"
	case "armv6", "armv6l", "armel", "arm/v6":
		return "arm/v6"
	case "i386", "i486", "i586", "i686", "x86", "386":
		return "386"
	}
	return a
}

var swarmNodeStates = map[string]bool{"inactive": true, "pending": true, "active": true, "error": true, "locked": true}

// Sanitize drops values that don't look like what dockerd reports. The
// hello comes from the agent host, so its content is not trusted further
// than the formats below.
func (d *DaemonInfo) Sanitize() *DaemonInfo {
	if d == nil {
		return nil
	}
	out := &DaemonInfo{
		OS:   token(d.OS, 32, "/_-."),
		Arch: token(d.Arch, 32, "/_-."),
	}
	if s := d.Swarm; s != nil {
		st := &SwarmState{
			NodeID:    token(s.NodeID, 64, ""),
			ClusterID: token(s.ClusterID, 64, ""),
		}
		if swarmNodeStates[s.NodeState] {
			st.NodeState = s.NodeState
		}
		if s.Role == "manager" || s.Role == "worker" {
			st.Role = s.Role
		}
		out.Swarm = st
	}
	return out
}

// token returns s when it is at most max characters of lower-case ASCII
// letters, digits and the extra characters, else "".
func token(s string, max int, extra string) string {
	if len(s) > max {
		return ""
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune(extra, r) {
			continue
		}
		return ""
	}
	return s
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// The *Col helpers give the agents-table values; a nil DaemonInfo stores
// NULL so an agent downgraded to an older version doesn't keep stale data.
func (d *DaemonInfo) osCol() sql.NullString {
	if d == nil {
		return sql.NullString{}
	}
	return nullable(d.OS)
}

func (d *DaemonInfo) archCol() sql.NullString {
	if d == nil {
		return sql.NullString{}
	}
	return nullable(d.Arch)
}

func (d *DaemonInfo) swarmCol(field func(*SwarmState) string) sql.NullString {
	if d == nil || d.Swarm == nil {
		return sql.NullString{}
	}
	return nullable(field(d.Swarm))
}

// DaemonInfo returns what agent id's daemon reported: from the live
// session when it is online, else from its last hello. Nil when the agent
// never sent one (older agent version).
func (s *Service) DaemonInfo(ctx context.Context, id string) (*DaemonInfo, error) {
	if ag := s.GetConnected(id); ag != nil {
		return ag.Hello.Daemon, nil
	}
	var osName, arch, nodeID, nodeState, role, clusterID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT daemon_os, daemon_arch, swarm_node_id, swarm_node_state, swarm_role, swarm_cluster_id
		  FROM agents WHERE id = ?`, id).Scan(&osName, &arch, &nodeID, &nodeState, &role, &clusterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !osName.Valid && !arch.Valid && !nodeState.Valid {
		return nil, nil
	}
	d := &DaemonInfo{OS: osName.String, Arch: arch.String}
	if nodeState.Valid {
		d.Swarm = &SwarmState{NodeID: nodeID.String, NodeState: nodeState.String, Role: role.String, ClusterID: clusterID.String}
	}
	return d, nil
}
