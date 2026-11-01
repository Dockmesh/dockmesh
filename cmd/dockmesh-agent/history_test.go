package main

import (
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func TestHistoryBuffer(t *testing.T) {
	now := time.Now().Unix()
	var b historyBuffer
	b.host = []agents.MetricsHostSample{{TS: now - int64(historyKeep.Seconds()) - 60}}
	b.add(agents.MetricsHostSample{TS: now}, []agents.MetricsContainerSample{{TS: now, Name: "web"}})
	if len(b.host) != 1 || b.host[0].TS != now {
		t.Fatalf("host buffer = %+v, want only the fresh sample", b.host)
	}

	batch := b.take()
	if len(batch.Host) != 1 || len(batch.Containers) != 1 || len(b.host) != 0 || len(b.ctrs) != 0 {
		t.Fatalf("take = %+v, buffer left %d/%d", batch, len(b.host), len(b.ctrs))
	}
	b.add(agents.MetricsHostSample{TS: now + 30}, nil)
	b.putBack(batch)
	if len(b.host) != 2 || b.host[0].TS != now || b.host[1].TS != now+30 {
		t.Errorf("after putBack host = %+v, want the returned sample first", b.host)
	}

	for range agents.MaxMetricsHostRows + 3 {
		b.host = append(b.host, agents.MetricsHostSample{TS: now})
	}
	if got := len(b.take().Host); got != agents.MaxMetricsHostRows {
		t.Errorf("take returned %d host samples, want the cap %d", got, agents.MaxMetricsHostRows)
	}
}
