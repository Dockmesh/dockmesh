package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// badUpstream stands in for a site config Caddy refuses: caddyFake
// rejects every /load that contains it.
const badUpstream = "10.6.6.6:80"

// caddyFake keeps the config it last accepted, like Caddy does.
type caddyFake struct {
	mu      sync.Mutex
	running string
	refused int
	down    bool
}

func (f *caddyFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	switch {
	case r.URL.Path == "/id/"+testAdminID:
		_, _ = w.Write([]byte(`{}`))
	case strings.HasPrefix(r.URL.Path, "/id/"):
		http.Error(w, "unknown object ID", http.StatusNotFound)
	case r.URL.Path == "/load":
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), badUpstream) {
			f.refused++
			http.Error(w, "loading config: "+badUpstream, http.StatusBadRequest)
			return
		}
		f.running = string(b)
	case r.URL.Path == "/config/":
		_, _ = w.Write([]byte(f.running))
	default:
		http.NotFound(w, r)
	}
}

func (f *caddyFake) state() (running string, refused int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.refused
}

// recreate gives the fake the bootstrap config of a new container.
func (f *caddyFake) recreate(t *testing.T) {
	t.Helper()
	b, err := bootstrapConfig(testAdminID)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.running = string(b)
	f.mu.Unlock()
}

func newApplyService(t *testing.T) (*Service, *caddyFake, *noticeLog) {
	t.Helper()
	s := newServiceDB(t)
	s.enabled.Store(true)
	id := testAdminID
	s.adminID.Store(&id)
	log := &noticeLog{}
	s.SetNotices(log)
	f := &caddyFake{}
	f.recreate(t)
	useTestAdmin(t, httptest.NewServer(f))
	return s, f, log
}

func mustCreate(t *testing.T, s *Service, host, upstream string) *Route {
	t.Helper()
	r, err := s.CreateRoute(context.Background(), RouteSpec{Host: host, Upstream: upstream, TLSMode: "auto"})
	if err != nil {
		t.Fatalf("create %s: %v", host, err)
	}
	return r
}

func storedRows(t *testing.T, s *Service) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT host, upstream, tls_mode, enabled, options FROM proxy_routes ORDER BY host`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var host, up, tls, opts string
		var on int
		if err := rows.Scan(&host, &up, &tls, &on, &opts); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Join([]string{host, up, tls, string(rune('0' + on)), opts}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func offlineNotice(log *noticeLog) *recordedNotice {
	for i := len(log.got) - 1; i >= 0; i-- {
		if log.got[i].in.Key == noticeKeyOffline {
			return &log.got[i]
		}
	}
	return nil
}

// A change Caddy refuses leaves the database and the running config as
// they were, and doesn't block later changes.
func TestRejectedChangeIsRolledBack(t *testing.T) {
	ctx := context.Background()
	s, f, _ := newApplyService(t)
	a := mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	beforeDB := storedRows(t, s)
	beforeCaddy, _ := f.state()

	steps := []struct {
		name string
		do   func() error
	}{
		{"create", func() error {
			_, err := s.CreateRoute(ctx, RouteSpec{Host: "b.example.com", Upstream: badUpstream, TLSMode: "auto"})
			return err
		}},
		{"update", func() error {
			_, err := s.UpdateRoute(ctx, a.ID, badUpstream, "auto", nil, nil)
			return err
		}},
	}
	for _, st := range steps {
		if err := st.do(); !errors.Is(err, ErrApplyRejected) {
			t.Fatalf("%s: err = %v, want ErrApplyRejected", st.name, err)
		}
		if got := storedRows(t, s); got != beforeDB {
			t.Fatalf("%s: database changed:\n%s\nwant\n%s", st.name, got, beforeDB)
		}
		if got, _ := f.state(); got != beforeCaddy {
			t.Fatalf("%s: running config changed", st.name)
		}
	}

	c := mustCreate(t, s, "c.example.com", "10.0.0.3:80")
	if err := s.SetRouteEnabled(ctx, a.ID, false); err != nil {
		t.Fatalf("disable a: %v", err)
	}
	if err := s.DeleteRoute(ctx, c.ID); err != nil {
		t.Fatalf("delete c: %v", err)
	}
	running, _ := f.state()
	if strings.Contains(running, "a.example.com") || strings.Contains(running, "c.example.com") {
		t.Fatalf("later changes not applied: %s", running)
	}
}

// Caddy's own text never reaches the user: it can quote other sites.
func TestRejectedChangeHidesCaddyText(t *testing.T) {
	s, _, _ := newApplyService(t)
	_, err := s.CreateRoute(context.Background(), RouteSpec{Host: "b.example.com", Upstream: badUpstream, TLSMode: "auto"})
	if err == nil || strings.Contains(err.Error(), "loading config") {
		t.Fatalf("err = %v", err)
	}
}

// makeUnloadable changes a row behind Dockmesh's back into a state Caddy
// refuses, as a row committed while Caddy was unreachable can be.
func makeUnloadable(t *testing.T, s *Service, id int64) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE proxy_routes SET upstream = ? WHERE id = ?`, badUpstream, id); err != nil {
		t.Fatal(err)
	}
}

// A new container gets every site Caddy can load; the unloadable one is
// left out and named in the notice.
func TestUnloadableRowAfterRecreateTakesOnlyThatSiteOffline(t *testing.T) {
	s, f, log := newApplyService(t)
	mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	b := mustCreate(t, s, "b.example.com", "10.0.0.2:80")
	makeUnloadable(t, s, b.ID)

	f.recreate(t)
	if err := s.SyncFromDB(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	running, _ := f.state()
	if !strings.Contains(running, "a.example.com") || strings.Contains(running, "b.example.com") {
		t.Fatalf("running config: %s", running)
	}
	n := offlineNotice(log)
	if n == nil || !n.active {
		t.Fatalf("offline notice = %+v", n)
	}
	if body := n.in.Body.In("en"); !strings.Contains(body, "- b.example.com") || strings.Contains(body, "a.example.com") {
		t.Fatalf("notice body = %q", body)
	}
	if n.in.Link != "/proxy/b.example.com" {
		t.Fatalf("notice link = %q", n.in.Link)
	}
}

// A container resumed from its autosave keeps serving what it has; a
// failing sync only raises the notice.
func TestUnloadableRowAfterRestartKeepsResumedConfig(t *testing.T) {
	s, f, log := newApplyService(t)
	mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	b := mustCreate(t, s, "b.example.com", "10.0.0.2:80")
	makeUnloadable(t, s, b.ID)
	resumed, _ := f.state()

	if err := s.SyncFromDB(context.Background()); !isRejected(err) {
		t.Fatalf("sync: %v, want a rejection", err)
	}
	if running, _ := f.state(); running != resumed {
		t.Fatal("the resumed config was replaced")
	}
	if n := offlineNotice(log); n == nil || !n.active || !strings.Contains(n.in.Body.In("en"), "b.example.com") {
		t.Fatalf("offline notice = %+v", n)
	}
}

// Other sites can still be changed while one stored row can't be loaded,
// and fixing that row brings it back and clears the notice.
func TestOtherSitesStillChangeWithUnloadableRow(t *testing.T) {
	ctx := context.Background()
	s, f, log := newApplyService(t)
	mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	b := mustCreate(t, s, "b.example.com", "10.0.0.2:80")
	makeUnloadable(t, s, b.ID)

	mustCreate(t, s, "c.example.com", "10.0.0.3:80")
	running, _ := f.state()
	if !strings.Contains(running, "c.example.com") || strings.Contains(running, "b.example.com") {
		t.Fatalf("running config: %s", running)
	}
	if n := offlineNotice(log); n == nil || !n.active {
		t.Fatalf("offline notice = %+v", n)
	}

	if _, err := s.UpdateRoute(ctx, b.ID, "10.0.0.2:80", "auto", nil, nil); err != nil {
		t.Fatalf("fix b: %v", err)
	}
	running, _ = f.state()
	if !strings.Contains(running, "b.example.com") {
		t.Fatalf("fixed site not served: %s", running)
	}
	if n := offlineNotice(log); n == nil || n.active {
		t.Fatalf("offline notice still active: %+v", n)
	}
}

// The watchdog doesn't push a config Caddy already refused every
// interval; it tries again once something changed.
func TestReconcileSkipsRefusedConfig(t *testing.T) {
	ctx := context.Background()
	s, f, _ := newApplyService(t)
	b := mustCreate(t, s, "b.example.com", "10.0.0.2:80")
	makeUnloadable(t, s, b.ID)

	for range 3 {
		_ = s.reconcileConfig(ctx)
	}
	if _, refused := f.state(); refused != 1 {
		t.Fatalf("refused loads = %d, want 1", refused)
	}
}

// With Caddy not answering, a change is saved and applied later.
func TestChangeWhileCaddyDownIsSavedAndAppliedLater(t *testing.T) {
	ctx := context.Background()
	s, f, _ := newApplyService(t)
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	r := mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	if got, err := s.getRoute(ctx, r.ID); err != nil || got.applied() {
		t.Fatalf("route = %+v, %v; want saved and not applied", got, err)
	}

	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	if err := s.reconcileConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if running, _ := f.state(); !strings.Contains(running, "a.example.com") {
		t.Fatalf("running config: %s", running)
	}
	if got, _ := s.getRoute(ctx, r.ID); !got.applied() {
		t.Fatal("route not marked applied after the push")
	}
}

// Rows from before applied_hash that Caddy runs count as loaded in their
// current state.
func TestBackfilledRowsCountAsApplied(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newApplyService(t)
	r := mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	if _, err := s.db.Exec(`UPDATE proxy_routes SET applied_hash = ?`, backfillHash); err != nil {
		t.Fatal(err)
	}
	if err := s.resolveBackfill(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.getRoute(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.appliedHash != stateHash(got) {
		t.Fatalf("applied_hash = %q, want the hash of the current state", got.appliedHash)
	}
}

func TestServesRoutes(t *testing.T) {
	boot, err := bootstrapConfig(testAdminID)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		running string
		want    bool
	}{
		{"bootstrap", string(boot), false},
		{"no routes", `{"apps":{"http":{"servers":{"dockmesh_http":{"routes":[]}}}}}`, false},
		{"routes", `{"apps":{"http":{"servers":{"dockmesh":{"routes":[{"handle":[]}]}}}}}`, true},
		{"unreadable", `{`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := servesRoutes([]byte(tc.running)); got != tc.want {
				t.Fatalf("servesRoutes = %v, want %v", got, tc.want)
			}
		})
	}
}

// An install upgraded while one row is a change Caddy refused (saved
// by an older version): that row doesn't count as loaded,
// so it no longer blocks the other sites.
func TestBackfillLeavesOutRowCaddyRefused(t *testing.T) {
	ctx := context.Background()
	s, f, log := newApplyService(t)
	a := mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	b := mustCreate(t, s, "b.example.com", "10.0.0.2:80")
	makeUnloadable(t, s, b.ID)
	if _, err := s.db.Exec(`UPDATE proxy_routes SET applied_hash = ?`, backfillHash); err != nil {
		t.Fatal(err)
	}

	if err := s.SyncFromDB(ctx); !isRejected(err) {
		t.Fatalf("sync: %v, want a rejection", err)
	}
	ra, err := s.getRoute(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := s.getRoute(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ra.applied() || rb.applied() || rb.appliedHash == backfillHash {
		t.Fatalf("applied: a=%v b=%v (%q)", ra.applied(), rb.applied(), rb.appliedHash)
	}
	if n := offlineNotice(log); n == nil || !n.active || !strings.Contains(n.in.Body.In("en"), "b.example.com") {
		t.Fatalf("offline notice = %+v", n)
	}

	mustCreate(t, s, "c.example.com", "10.0.0.3:80")
	if running, _ := f.state(); !strings.Contains(running, "c.example.com") || !strings.Contains(running, "a.example.com") {
		t.Fatalf("running config: %s", running)
	}
}

// A site left out would be answered by an overlapping wildcard site under
// that site's rules, so the change is refused instead.
func TestNoFallbackWhenWildcardCoversLeftOutSite(t *testing.T) {
	s, f, _ := newApplyService(t)
	mustCreate(t, s, "*.example.com", "10.0.0.1:80")
	b := mustCreate(t, s, "secure.example.com", "10.0.0.2:80")
	makeUnloadable(t, s, b.ID)
	before, _ := f.state()

	_, err := s.CreateRoute(context.Background(), RouteSpec{Host: "other.example.org", Upstream: "10.0.0.3:80", TLSMode: "auto"})
	if !errors.Is(err, ErrApplyRejected) {
		t.Fatalf("err = %v, want ErrApplyRejected", err)
	}
	if running, _ := f.state(); running != before {
		t.Fatal("running config changed")
	}
}

// Caddy's text stays out of errors, which reach API responses.
func TestRejectionErrorHidesCaddyText(t *testing.T) {
	err := error(&loadRejectedError{status: 400, body: "route 3: secret.example.com"})
	if strings.Contains(err.Error(), "secret") || !errors.Is(err, ErrConfigRejected) {
		t.Fatalf("err = %q", err)
	}
}

// A marked row changed while Caddy doesn't answer no longer counts as
// loaded, and the markers stay until Caddy runs a config to check them
// against.
func TestBackfillMarkerOnUnappliedChange(t *testing.T) {
	ctx := context.Background()
	s, f, _ := newApplyService(t)
	a := mustCreate(t, s, "a.example.com", "10.0.0.1:80")
	b := mustCreate(t, s, "b.example.com", "10.0.0.2:80")
	if _, err := s.db.Exec(`UPDATE proxy_routes SET applied_hash = ?`, backfillHash); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	if _, err := s.UpdateRoute(ctx, a.ID, "10.0.0.9:80", "auto", nil, nil); err != nil {
		t.Fatal(err)
	}
	ra, _ := s.getRoute(ctx, a.ID)
	rb, _ := s.getRoute(ctx, b.ID)
	if ra.applied() || rb.appliedHash != backfillHash {
		t.Fatalf("a applied=%v (%q), b=%q", ra.applied(), ra.appliedHash, rb.appliedHash)
	}
}
