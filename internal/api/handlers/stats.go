package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	dtypes "github.com/docker/docker/api/types"
)

// WSStats streams normalized container stats over a WebSocket.
// Auth via ?ticket= query parameter (§15.8). Honours ?host= for remote agents.
func (h *Handlers) WSStats(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "ticket required", http.StatusUnauthorized)
		return
	}
	if _, err := h.Auth.ValidateWSTicket(ticket); err != nil {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}

	target, err := h.pickHost(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	containerID := chi.URLParam(r, "id")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws stats upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Cancel ctx when client disconnects.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	rc, err := target.ContainerStats(ctx, containerID)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
		return
	}
	defer rc.Close()

	// Rolling 5-sample CPU% window. Docker's stats stream ticks at
	// ~1s, so this matches the 5-second smoothing we use on host
	// metrics. Memory / net / block I/O stay instant: they're byte
	// counters Docker reports directly, no delta-division noise.
	const cpuWindow = 5
	cpuSamples := make([]float64, 0, cpuWindow)

	dec := json.NewDecoder(rc)
	for {
		if ctx.Err() != nil {
			return
		}
		var raw dtypes.StatsJSON
		if err := dec.Decode(&raw); err != nil {
			if err != io.EOF {
				slog.Debug("stats decode", "err", err)
			}
			return
		}
		norm := docker.Normalize(&raw)
		cpuSamples = append(cpuSamples, norm.CPUPercent)
		if len(cpuSamples) > cpuWindow {
			cpuSamples = cpuSamples[len(cpuSamples)-cpuWindow:]
		}
		var sum float64
		for _, s := range cpuSamples {
			sum += s
		}
		norm.CPUPercent = sum / float64(len(cpuSamples))
		b, err := json.Marshal(norm)
		if err != nil {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			return
		}
	}
}
