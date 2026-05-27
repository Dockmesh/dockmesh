// Caddy Prometheus-metrics scraper. Caddy exposes /metrics on its
// admin endpoint when admin.config.metrics is configured (or the
// server has a `metrics` directive). We don't try to install a metrics
// scraper inside Caddy itself — we just hit /metrics on 127.0.0.1:2019
// and project the counters we care about (request rate, status-code
// distribution, p95 latency) into a per-host snapshot the UI can render.
package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MetricsSnapshot is what the proxy dashboard reads. Numbers are
// 5-minute rolling rates derived from cumulative counters scraped now
// versus the previous snapshot — first call after process start has
// rate=0 across the board, that's deliberate (no synthetic peaks).
type MetricsSnapshot struct {
	ScrapedAt    time.Time            `json:"scraped_at"`
	TotalReqs    uint64               `json:"total_requests"`
	RPS          float64              `json:"requests_per_second"`
	StatusBuckets map[string]uint64   `json:"status_buckets"` // "2xx", "3xx", "4xx", "5xx"
	P95LatencyMS float64              `json:"p95_latency_ms"`
	PerHost      []HostMetric         `json:"per_host"`
}

// HostMetric is the per-route slice of the snapshot. host matches
// Route.Host so the UI can join with the route list without an extra
// lookup.
type HostMetric struct {
	Host          string             `json:"host"`
	Reqs          uint64             `json:"requests"`
	RPS           float64            `json:"requests_per_second"`
	StatusBuckets map[string]uint64  `json:"status_buckets"`
	P95LatencyMS  float64            `json:"p95_latency_ms"`
}

type metricsState struct {
	lastSnapshot *MetricsSnapshot
	lastScrapeAt time.Time
}

// MetricsState lives on Service so we can compute rates across calls.
// Not persisted — restart resets to zero.
func (s *Service) initMetricsState() {
	if s.metrics == nil {
		s.metrics = &metricsState{}
	}
}

// Metrics scrapes Caddy's Prometheus endpoint and returns a snapshot.
// If Caddy doesn't expose /metrics (default in caddy:2 image without
// explicit configuration), returns an empty snapshot with the
// timestamp set — the frontend renders that as "metrics not enabled".
func (s *Service) Metrics(ctx context.Context) (*MetricsSnapshot, error) {
	s.initMetricsState()
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	raw, err := s.scrapeCaddy(ctx)
	if err != nil {
		return &MetricsSnapshot{
			ScrapedAt:     now,
			StatusBuckets: map[string]uint64{},
			PerHost:       []HostMetric{},
		}, nil
	}
	snap := projectMetrics(raw, now)
	if s.metrics.lastSnapshot != nil {
		applyRates(snap, s.metrics.lastSnapshot, now.Sub(s.metrics.lastScrapeAt))
	}
	s.metrics.lastSnapshot = snap
	s.metrics.lastScrapeAt = now
	return snap, nil
}

func (s *Service) scrapeCaddy(ctx context.Context) ([]promSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	cli := &http.Client{Timeout: 3 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned %d", resp.StatusCode)
	}
	return parseProm(resp.Body)
}

// promSample is one labeled metric line in Caddy's Prometheus output.
type promSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

func parseProm(r io.Reader) ([]promSample, error) {
	out := []promSample{}
	scanner := bufio.NewScanner(r)
	// Increase the buffer so long histogram bucket lines don't trip
	// bufio's default 64K limit.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "name{lbl=\"v\",lbl2=\"v2\"} 123.4"
		brace := strings.Index(line, "{")
		var name, labelStr, valStr string
		if brace > 0 {
			closeB := strings.Index(line, "}")
			if closeB < 0 {
				continue
			}
			name = line[:brace]
			labelStr = line[brace+1 : closeB]
			valStr = strings.TrimSpace(line[closeB+1:])
		} else {
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			name = parts[0]
			valStr = parts[1]
		}
		if sp := strings.Index(valStr, " "); sp > 0 {
			valStr = valStr[:sp]
		}
		f, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		labels := parsePromLabels(labelStr)
		out = append(out, promSample{Name: name, Labels: labels, Value: f})
	}
	return out, scanner.Err()
}

func parsePromLabels(s string) map[string]string {
	m := map[string]string{}
	for _, part := range splitLabels(s) {
		eq := strings.Index(part, "=")
		if eq <= 0 {
			continue
		}
		k := part[:eq]
		v := strings.Trim(part[eq+1:], `"`)
		m[k] = v
	}
	return m
}

// splitLabels handles commas inside quoted label values correctly.
func splitLabels(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '"':
			depth = 1 - depth
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func projectMetrics(samples []promSample, now time.Time) *MetricsSnapshot {
	snap := &MetricsSnapshot{
		ScrapedAt:     now,
		StatusBuckets: map[string]uint64{},
		PerHost:       []HostMetric{},
	}
	perHost := map[string]*HostMetric{}
	// Latency buckets — we collect histogram bucket boundaries to
	// compute p95 at the end.
	allBuckets := map[float64]uint64{}
	hostBuckets := map[string]map[float64]uint64{}

	for _, s := range samples {
		switch s.Name {
		case "caddy_http_requests_total":
			n := uint64(s.Value)
			snap.TotalReqs += n
			code := s.Labels["code"]
			if code != "" {
				bucket := code[:1] + "xx"
				snap.StatusBuckets[bucket] += n
			}
			host := s.Labels["host"]
			if host == "" {
				host = s.Labels["server"]
			}
			if host != "" {
				hm, ok := perHost[host]
				if !ok {
					hm = &HostMetric{Host: host, StatusBuckets: map[string]uint64{}}
					perHost[host] = hm
				}
				hm.Reqs += n
				if code != "" {
					hm.StatusBuckets[code[:1]+"xx"] += n
				}
			}
		case "caddy_http_request_duration_seconds_bucket":
			le, err := strconv.ParseFloat(s.Labels["le"], 64)
			if err != nil {
				continue
			}
			allBuckets[le] += uint64(s.Value)
			host := s.Labels["host"]
			if host == "" {
				host = s.Labels["server"]
			}
			if host != "" {
				if hostBuckets[host] == nil {
					hostBuckets[host] = map[float64]uint64{}
				}
				hostBuckets[host][le] += uint64(s.Value)
			}
		}
	}

	snap.P95LatencyMS = percentileFromBuckets(allBuckets, 0.95) * 1000
	for h, hm := range perHost {
		hm.P95LatencyMS = percentileFromBuckets(hostBuckets[h], 0.95) * 1000
		snap.PerHost = append(snap.PerHost, *hm)
	}
	sort.Slice(snap.PerHost, func(i, j int) bool {
		return snap.PerHost[i].Host < snap.PerHost[j].Host
	})
	return snap
}

func percentileFromBuckets(buckets map[float64]uint64, p float64) float64 {
	if len(buckets) == 0 {
		return 0
	}
	// Prom histogram buckets are cumulative; the +Inf bucket holds
	// the grand total. If +Inf is missing we fall back to the largest
	// observed counter.
	keys := make([]float64, 0, len(buckets))
	var total uint64
	for k, v := range buckets {
		if math.IsInf(k, 0) {
			if v > total {
				total = v
			}
			continue
		}
		keys = append(keys, k)
		if v > total {
			total = v
		}
	}
	if total == 0 {
		return 0
	}
	sort.Float64s(keys)
	target := uint64(float64(total) * p)
	for _, k := range keys {
		if buckets[k] >= target {
			return k
		}
	}
	if len(keys) > 0 {
		return keys[len(keys)-1]
	}
	return 0
}

func applyRates(curr, prev *MetricsSnapshot, dt time.Duration) {
	if dt <= 0 {
		return
	}
	secs := dt.Seconds()
	curr.RPS = float64(curr.TotalReqs-prev.TotalReqs) / secs
	if curr.RPS < 0 {
		curr.RPS = 0
	}
	for i := range curr.PerHost {
		curr.PerHost[i].RPS = perHostRate(prev, curr.PerHost[i].Host, curr.PerHost[i].Reqs, secs)
	}
}

func perHostRate(prev *MetricsSnapshot, host string, currReqs uint64, secs float64) float64 {
	for _, p := range prev.PerHost {
		if p.Host == host {
			if currReqs >= p.Reqs {
				return float64(currReqs-p.Reqs) / secs
			}
			return 0
		}
	}
	return 0
}
