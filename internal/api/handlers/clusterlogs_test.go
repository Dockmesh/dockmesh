package handlers

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestServiceLogsReq(t *testing.T) {
	tests := []struct {
		query  string
		tail   int
		since  string
		follow bool
		bad    bool
	}{
		{query: "", tail: 100, follow: true},
		{query: "tail=0&follow=0", tail: 0},
		{query: "tail=5000&follow=false", tail: 5000},
		{query: "tail=5001", bad: true},
		{query: "tail=-1", bad: true},
		{query: "tail=all", bad: true},
		{query: "since=1700000000", tail: 100, since: "1700000000", follow: true},
		{query: "since=2026-11-20T10:00:00Z", tail: 100, since: "1795168800", follow: true},
		{query: "since=15m", tail: 100, since: "1795168800", follow: true},
		{query: "since=-5m", bad: true},
		{query: "since=2400h", bad: true},
		{query: "since=yesterday", bad: true},
	}
	now := time.Date(2026, 11, 20, 10, 15, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?"+tt.query, nil)
			req, err := serviceLogsReqAt(r, "web", now)
			if (err != nil) != tt.bad {
				t.Fatalf("err %v, want bad=%v", err, tt.bad)
			}
			if tt.bad {
				return
			}
			if req.Service != "web" || req.Tail != tt.tail || req.Since != tt.since || req.Follow != tt.follow {
				t.Fatalf("got %+v", req)
			}
		})
	}
}

func TestServiceLogSlots(t *testing.T) {
	var releases []func()
	for i := range maxServiceLogStreamsCaller {
		rel, ok := takeServiceLogSlot("u:alice")
		if !ok {
			t.Fatalf("stream %d refused", i)
		}
		releases = append(releases, rel)
	}
	if _, ok := takeServiceLogSlot("u:alice"); ok {
		t.Fatal("a caller got more than its share")
	}
	for i := 0; len(releases) < maxServiceLogStreams; i++ {
		rel, ok := takeServiceLogSlot("u:other" + strconv.Itoa(i%8))
		if !ok {
			t.Fatalf("stream %d refused below the total", len(releases))
		}
		releases = append(releases, rel)
	}
	if _, ok := takeServiceLogSlot("u:bob"); ok {
		t.Fatal("more than the total")
	}
	for _, rel := range releases {
		rel()
	}
	if serviceLogSlots.total != 0 || len(serviceLogSlots.perCaller) != 0 {
		t.Fatalf("slots left: %d %v", serviceLogSlots.total, serviceLogSlots.perCaller)
	}
}
