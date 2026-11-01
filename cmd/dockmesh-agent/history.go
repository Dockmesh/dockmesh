package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/system"
)

const (
	historyInterval = 30 * time.Second
	historyFlush    = time.Minute
	// historyCatchUp is the wait between batches while a backlog drains;
	// the server refuses batches closer than agents.MetricsMinGap.
	historyCatchUp = 15 * time.Second
	historyAckWait = 45 * time.Second
	// historyKeep is how long samples wait for a server that takes them,
	// so a short disconnect or server restart leaves no gap.
	historyKeep = 30 * time.Minute
)

// historyBuffer holds the samples not yet shipped to the server.
type historyBuffer struct {
	mu   sync.Mutex
	host []agents.MetricsHostSample
	ctrs []agents.MetricsContainerSample
}

var history historyBuffer

func (b *historyBuffer) add(h agents.MetricsHostSample, ctrs []agents.MetricsContainerSample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.host = append(b.host, h)
	b.ctrs = append(b.ctrs, ctrs...)
	b.trim(time.Now())
}

// trim drops samples older than historyKeep. Callers hold mu.
func (b *historyBuffer) trim(now time.Time) {
	cutoff := now.Add(-historyKeep).Unix()
	i := 0
	for i < len(b.host) && b.host[i].TS < cutoff {
		i++
	}
	j := 0
	for j < len(b.ctrs) && b.ctrs[j].TS < cutoff {
		j++
	}
	if i > 0 || j > 0 {
		slog.Warn("history: dropped samples the server did not take in time", "host", i, "containers", j)
	}
	b.host = b.host[i:]
	b.ctrs = b.ctrs[j:]
}

// pending reports whether samples are waiting.
func (b *historyBuffer) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.host) > 0 || len(b.ctrs) > 0
}

// take removes up to one batch worth of the oldest samples.
func (b *historyBuffer) take() agents.MetricsBatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	nh := min(len(b.host), agents.MaxMetricsHostRows)
	nc := min(len(b.ctrs), agents.MaxMetricsContainers)
	batch := agents.MetricsBatch{
		Host:       append([]agents.MetricsHostSample(nil), b.host[:nh]...),
		Containers: append([]agents.MetricsContainerSample(nil), b.ctrs[:nc]...),
	}
	b.host = b.host[nh:]
	b.ctrs = b.ctrs[nc:]
	return batch
}

// putBack returns a batch that could not be sent, ahead of newer samples.
func (b *historyBuffer) putBack(batch agents.MetricsBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.host = append(batch.Host, b.host...)
	b.ctrs = append(batch.Containers, b.ctrs...)
	b.trim(time.Now())
}

// runHistorySampler records host and container samples every
// historyInterval for as long as the agent runs, connected or not.
func runHistorySampler(ctx context.Context) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		slog.Warn("history: docker client init failed, recording host samples only", "err", err)
	}
	t := time.NewTicker(historyInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ts := time.Now().Unix()
			m := system.Collect()
			host := agents.MetricsHostSample{
				TS: ts, CPUPercent: m.CPUPercent, CPUCores: m.CPUCores,
				MemUsed: m.MemUsed, MemTotal: m.MemTotal, DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal,
			}
			var ctrs []agents.MetricsContainerSample
			if cli != nil {
				ctrs = sampleContainers(ctx, cli, ts)
			}
			history.add(host, ctrs)
		}
	}
}

func sampleContainers(ctx context.Context, cli *client.Client, ts int64) []agents.MetricsContainerSample {
	ctx, cancel := context.WithTimeout(ctx, historyInterval-5*time.Second)
	defer cancel()
	list, err := cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		slog.Debug("history: list containers", "err", err)
		return nil
	}
	var (
		mu  sync.Mutex
		out []agents.MetricsContainerSample
		wg  sync.WaitGroup
	)
	sem := make(chan struct{}, 8)
	for _, ct := range list {
		if len(ct.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(ct.Names[0], "/")
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := docker.SampleStats(ctx, cli, ct.ID)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, agents.MetricsContainerSample{
				TS: ts, Name: name, CPUPercentCore: s.CPUPercentCore, OnlineCPUs: s.OnlineCPUs,
				MemUsed: s.MemUsed, MemLimit: s.MemLimit, NetRx: s.NetRx, NetTx: s.NetTx,
				BlkRead: s.BlkRead, BlkWrite: s.BlkWrite,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// flushHistory ships the buffer while the session lasts: every
// historyFlush, or every historyCatchUp while a backlog drains. A batch
// the server did not acknowledge goes back into the buffer.
func flushHistory(ctx context.Context, conn *websocket.Conn) {
	wait := historyFlush
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = historyFlush
		batch := history.take()
		if len(batch.Host) == 0 && len(batch.Containers) == 0 {
			continue
		}
		batch.SentAt = time.Now().Unix()
		actx, cancel := context.WithTimeout(ctx, historyAckWait)
		var ack json.RawMessage
		err := askServer(actx, conn, agents.FrameAgentMetrics, batch, &ack)
		cancel()
		if err != nil {
			history.putBack(batch)
			slog.Debug("history: batch not stored, keeping it", "err", err)
			wait = historyCatchUp
			continue
		}
		if history.pending() {
			wait = historyCatchUp
		}
	}
}
