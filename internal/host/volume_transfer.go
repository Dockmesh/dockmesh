package host

import (
	"errors"
	"strings"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// ErrAgentNoVolumeTransfer refuses a migration with volumes through an
// agent that lacks stream flow control, staged imports or volume create.
var ErrAgentNoVolumeTransfer = errors.New("the agent is too old to move volumes between hosts; update it from the Hosts page")

// VolumeMissing reports whether err from InspectVolume says the volume
// doesn't exist, on the local daemon or through an agent.
func VolumeMissing(err error) bool {
	return err != nil && (cerrdefs.IsNotFound(err) || strings.Contains(err.Error(), "no such volume"))
}

// VolumeTransferError is nil when h can send (receive false) or receive
// a volume in a migration. The local host always can.
func VolumeTransferError(h Host, receive bool) error {
	r, ok := h.(*RemoteHost)
	if !ok {
		return nil
	}
	ag, err := r.live()
	if err != nil {
		return err
	}
	need := []string{agents.CapStreamCredit}
	if receive {
		need = append(need, agents.CapVolumeStaged, agents.CapVolumeManage)
	}
	for _, c := range need {
		if !ag.Hello.Has(c) {
			return ErrAgentNoVolumeTransfer
		}
	}
	return nil
}
