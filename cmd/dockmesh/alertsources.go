package main

import (
	"context"
	"errors"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/system"
)

type dockerEventSource interface {
	ContainerEvents(ctx context.Context) (<-chan events.Message, error)
}

type diskMountSource interface {
	DiskMounts(ctx context.Context) ([]system.Mount, error)
}

// alertEventHost adapts a host.Host to the alert engine's event input.
type alertEventHost struct{ h host.Host }

func (a alertEventHost) ID() string   { return a.h.ID() }
func (a alertEventHost) Name() string { return a.h.Name() }

func (a alertEventHost) ContainerEvents(ctx context.Context) (<-chan alerts.ContainerEvent, error) {
	src, ok := a.h.(dockerEventSource)
	if !ok {
		return nil, alerts.ErrEventsUnsupported
	}
	msgs, err := src.ContainerEvents(ctx)
	if errors.Is(err, host.ErrEventsUnsupported) {
		return nil, alerts.ErrEventsUnsupported
	}
	if err != nil {
		return nil, err
	}
	out := make(chan alerts.ContainerEvent, 16)
	go func() {
		defer close(out)
		for m := range msgs {
			attrs := m.Actor.Attributes
			ev := alerts.ContainerEvent{
				HostID:    a.h.ID(),
				HostName:  a.h.Name(),
				Container: attrs["name"],
				Stack:     attrs["com.docker.compose.project"],
				Action:    string(m.Action),
				ExitCode:  attrs["exitCode"],
				Time:      time.Unix(0, m.TimeNano),
			}
			if m.TimeNano == 0 {
				ev.Time = time.Now()
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func alertSources(reg *host.Registry, tags *hosttags.Service, proxySvc *proxy.Service) alerts.Sources {
	src := alerts.Sources{
		EventHosts: func(ctx context.Context) []alerts.EventHost {
			hosts := reg.PickAll(ctx)
			out := make([]alerts.EventHost, 0, len(hosts))
			for _, h := range hosts {
				out = append(out, alertEventHost{h})
			}
			return out
		},
		Mounts: func(ctx context.Context) []alerts.HostMounts {
			var out []alerts.HostMounts
			for _, h := range reg.PickAll(ctx) {
				dm, ok := h.(diskMountSource)
				if !ok {
					continue
				}
				hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				mounts, err := dm.DiskMounts(hctx)
				cancel()
				if err != nil {
					continue
				}
				out = append(out, alerts.HostMounts{HostID: h.ID(), HostName: h.Name(), Mounts: mounts})
			}
			return out
		},
		HostTags: tags.Tags,
	}
	if proxySvc != nil {
		src.Certs = func(ctx context.Context) []alerts.CertStatus {
			routes, err := proxySvc.ListRoutes(ctx)
			if err != nil {
				return nil
			}
			var out []alerts.CertStatus
			for _, r := range proxySvc.EnrichWithCertInfo(ctx, routes) {
				if r.CertValidTo != nil {
					out = append(out, alerts.CertStatus{Domain: r.Host, NotAfter: *r.CertValidTo})
				}
			}
			return out
		}
	}
	return src
}
