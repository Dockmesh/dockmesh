package host

import (
	"context"
	"sync"
	"time"
)

// AllHostsID is the sentinel value used on the query string / host picker
// to request "aggregate across every online host". Handlers compare
// against this directly so we don't rely on magic strings elsewhere.
const AllHostsID = "all"

// IsAll reports whether the given host id selector means "fan out across
// all online hosts". Empty string and "local" are single-host selectors;
// only the explicit AllHostsID triggers fan-out.
func IsAll(id string) bool { return id == AllHostsID }

// fanOutDefaultTimeout is the per-host deadline for a single fan-out call.
// If a host doesn't respond within this window we give up on it and
// return the rows we already have, listing the slow host in the response's
// unreachable_hosts array. Picked to be generous enough that healthy hosts
// don't get culled under a normal list load, tight enough that one stuck
// agent can't freeze the main page for seconds.
const fanOutDefaultTimeout = 3 * time.Second

// Unreachable describes one host that was selected by the fan-out but
// failed to produce results — either because its call returned an error
// or because it timed out. Reason is the short error string for display;
// we deliberately do not leak stack traces.
type Unreachable struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Reason   string `json:"reason"`
}

// FanOutResult is the shape every all-mode list handler returns. Items is
// the concatenation of each host's rows (already tagged with their
// origin host by the caller's fn), Unreachable lists hosts that failed.
//
// Unreachable is always at least an empty slice (never nil) so JSON
// encoders render `"unreachable_hosts": []` consistently on the wire.
//
// Handlers own the shape of T: typically a small wrapper struct that
// embeds the underlying resource type (e.g. dtypes.Container) with
// `HostID` / `HostName` fields added. Struct embedding flattens the
// host metadata alongside the resource fields in the final JSON, so
// the frontend reads `item.host_id` and `item.Id` side-by-side without
// an extra `.row` indirection.
type FanOutResult[T any] struct {
	Items       []T           `json:"items"`
	Unreachable []Unreachable `json:"unreachable_hosts"`
}

// PickAll returns every currently-reachable Host in the registry: local
// first, then every agent whose status is "online". Agents that are
// offline, pending, or revoked are skipped — we don't want to spend a
// 3-second timeout per dead agent every time a user opens a list page.
//
// The returned slice is safe to iterate without further locking; each
// Host value owns its own live reference to the underlying agent stream
// or docker client.
func (r *Registry) PickAll(ctx context.Context) []Host {
	out := []Host{r.local}
	if r.agents == nil {
		return out
	}
	list, err := r.agents.List(ctx)
	if err != nil {
		// Agent list unavailable (transient DB/cache issue). Degrade to
		// local only; caller still gets a valid response.
		return out
	}
	for _, a := range list {
		if a.Status != "online" {
			continue
		}
		live := r.agents.GetConnected(a.ID)
		if live == nil {
			continue
		}
		out = append(out, NewRemote(live.ID, live.Name, live))
	}
	return out
}

// FanOut runs fn against every host concurrently with the default per-host
// timeout and aggregates the results. A host that returns an error or
// times out is recorded in Unreachable and does not contribute rows; the
// function never returns an error itself — partial results are the whole
// point.
//
// The generic parameter T is the row type produced by fn. Typically each
// handler defines a small local wrapper struct like:
//
//	type containerRow struct {
//	    dtypes.Container
//	    HostID   string `json:"host_id"`
//	    HostName string `json:"host_name"`
//	}
//
// and builds []containerRow inside fn by iterating the host's own list
// and filling in HostID / HostName from h.ID() / h.Name().
func FanOut[T any](ctx context.Context, hosts []Host, fn func(context.Context, Host) ([]T, error)) FanOutResult[T] {
	return FanOutTimeout(ctx, hosts, fanOutDefaultTimeout, fn)
}

// FanOutTimeout is FanOut with an explicit per-host deadline. Used by
// handlers that know their backend call is faster or slower than the
// default (e.g. image list can be slower on a host with thousands of
// layers; log/exec/stats streams are long-lived and do not fan out at
// all).
func FanOutTimeout[T any](ctx context.Context, hosts []Host, perHost time.Duration, fn func(context.Context, Host) ([]T, error)) FanOutResult[T] {
	result := FanOutResult[T]{
		Items:       []T{},
		Unreachable: []Unreachable{},
	}
	if len(hosts) == 0 {
		return result
	}

	type out struct {
		rows []T
		fail *Unreachable
	}
	ch := make(chan out, len(hosts))
	var wg sync.WaitGroup

	for _, h := range hosts {
		wg.Add(1)
		go func(h Host) {
			defer wg.Done()
			// Shared deadline budget: the caller's ctx cancellation still
			// bubbles up, but we additionally cap per-host runtime so one
			// slow host can't hold up the whole response.
			subCtx, cancel := context.WithTimeout(ctx, perHost)
			defer cancel()

			rows, err := fn(subCtx, h)
			if err != nil {
				ch <- out{fail: &Unreachable{
					HostID:   h.ID(),
					HostName: h.Name(),
					Reason:   err.Error(),
				}}
				return
			}
			ch <- out{rows: rows}
		}(h)
	}

	wg.Wait()
	close(ch)

	for r := range ch {
		if r.fail != nil {
			result.Unreachable = append(result.Unreachable, *r.fail)
			continue
		}
		result.Items = append(result.Items, r.rows...)
	}
	return result
}
