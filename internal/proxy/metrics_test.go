package proxy

import (
	"strings"
	"testing"
	"time"
)

// Status classes come from the duration histogram, which carries the
// code label in Caddy's per-host metrics; requests_total has none.
func TestProjectMetricsPerHost(t *testing.T) {
	const scrape = `caddy_http_requests_total{handler="reverse_proxy",host="a.test",server="dockmesh_http"} 12
caddy_http_requests_total{handler="reverse_proxy",host="b.test",server="dockmesh_http"} 3
caddy_http_request_duration_seconds_bucket{code="200",handler="reverse_proxy",host="a.test",method="GET",server="dockmesh_http",le="0.005"} 10
caddy_http_request_duration_seconds_bucket{code="200",handler="reverse_proxy",host="a.test",method="GET",server="dockmesh_http",le="+Inf"} 10
caddy_http_request_duration_seconds_count{code="200",handler="reverse_proxy",host="a.test",method="GET",server="dockmesh_http"} 10
caddy_http_request_duration_seconds_count{code="502",handler="reverse_proxy",host="a.test",method="GET",server="dockmesh_http"} 2
caddy_http_request_duration_seconds_count{code="404",handler="reverse_proxy",host="b.test",method="GET",server="dockmesh_http"} 3
`
	samples, err := parseProm(strings.NewReader(scrape))
	if err != nil {
		t.Fatal(err)
	}
	snap := projectMetrics(samples, time.Now())
	if snap.TotalReqs != 15 || snap.StatusBuckets["2xx"] != 10 || snap.StatusBuckets["5xx"] != 2 || snap.StatusBuckets["4xx"] != 3 {
		t.Fatalf("totals: %d requests, buckets %v", snap.TotalReqs, snap.StatusBuckets)
	}
	if len(snap.PerHost) != 2 {
		t.Fatalf("per host = %+v", snap.PerHost)
	}
	a, b := snap.PerHost[0], snap.PerHost[1]
	if a.Host != "a.test" || a.Reqs != 12 || a.StatusBuckets["2xx"] != 10 || a.StatusBuckets["5xx"] != 2 {
		t.Errorf("a.test = %+v", a)
	}
	if b.Host != "b.test" || b.Reqs != 3 || b.StatusBuckets["4xx"] != 3 {
		t.Errorf("b.test = %+v", b)
	}
}
