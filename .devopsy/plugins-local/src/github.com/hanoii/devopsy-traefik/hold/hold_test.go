package hold

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// entrypoint is a fake Traefik entrypoint: down (the hold's answer to a
// probe) until up is set, then status.
type entrypoint struct {
	mu     sync.Mutex
	up     bool
	status int
	seen   []*http.Request
}

func (e *entrypoint) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, req)
	if !e.up {
		rw.Header().Set(Header, "down")
		rw.WriteHeader(http.StatusBadGateway)
		return
	}
	rw.WriteHeader(e.status)
}

func (e *entrypoint) set(up bool, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.up, e.status = up, status
}

func (e *entrypoint) probes() []*http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*http.Request(nil), e.seen...)
}

func newHold(t *testing.T, ep string, probe string, maxWait string) http.Handler {
	t.Helper()
	h, err := New(context.Background(), nil, &Config{
		EntryPoint: ep,
		Probes:     map[string]string{"websecure": probe},
		API:        "http://127.0.0.1:1",
		Middleware: "hold@file",
		MaxWait:    maxWait,
		Interval:   "10ms",
		Memory:     "1m",
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// routed makes the API's last read app.example.com's router, on web and
// websecure.
func routed(t *testing.T) {
	t.Helper()
	reset()
	t.Cleanup(reset)
	remember(map[string][]string{"app.example.com": {"web", "websecure"}}, time.Now(), time.Minute)
}

// fromErrors is the request Traefik's errors middleware makes to the hold.
func fromErrors(original string) *http.Request {
	req := httptest.NewRequest("GET", "http://app.example.com/?url="+strings.ReplaceAll(original, "?", "%3F"), nil)
	req.Header.Set("Cookie", "session=1")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	return req
}

func TestHoldUntilUp(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")

	go func() {
		time.Sleep(100 * time.Millisecond)
		// A site's own answer, whatever it is, ends the hold.
		ep.set(true, http.StatusUnauthorized)
	}()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/node/1?page=2"))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "https://app.example.com/node/1?page=2" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get(Header) != "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", rec.Header())
	}
	probes := ep.probes()
	if len(probes) < 2 {
		t.Fatalf("%d probes", len(probes))
	}
	p := probes[len(probes)-1]
	if p.Method != "HEAD" || p.Host != "app.example.com" || p.URL.RequestURI() != "/node/1?page=2" {
		t.Fatalf("probe: %s %s %s", p.Method, p.Host, p.URL.RequestURI())
	}
	// As the visitor, never as Traefik's own forwarding.
	if p.Header.Get(Header) != "probe" || p.Header.Get("Cookie") != "session=1" || p.Header.Get("X-Forwarded-For") != "" {
		t.Fatalf("probe headers: %v", p.Header)
	}
}

func TestHoldGivesUp(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "50ms")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" ||
		rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "Back in a moment") {
		t.Fatalf("got %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}

	// A 502 the hold never saw (no header) is down too.
	ep.set(true, http.StatusBadGateway)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("plain 502: got %d", rec.Code)
	}
}

func TestHoldAnswersAtOnce(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")

	for name, req := range map[string]*http.Request{
		"another host": fromErrors("https://elsewhere.example.com/"),
		"relative url": fromErrors("/x"),
		"no hold asked": func() *http.Request {
			r := fromErrors("https://app.example.com/")
			r.Header.Set(Header, "no")
			return r
		}(),
	} {
		start := time.Now()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" || time.Since(start) > time.Second {
			t.Fatalf("%s: got %d %v after %s", name, rec.Code, rec.Header(), time.Since(start))
		}
	}
	if n := len(ep.probes()); n != 0 {
		t.Fatalf("%d probes", n)
	}

	// A probe reaching the hold: down, at once.
	req := fromErrors("https://app.example.com/")
	req.Header.Set(Header, "probe")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || rec.Header().Get(Header) != "down" {
		t.Fatalf("probe: got %d %v", rec.Code, rec.Header())
	}
}

func TestHoldVisitorGone(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/").WithContext(ctx))
	if time.Since(start) > 2*time.Second || rec.Header().Get("Location") != "" {
		t.Fatalf("held %s, %v", time.Since(start), rec.Header())
	}
}

func reset() {
	mu.Lock()
	defer mu.Unlock()
	present = nil
	gone = map[string]goneHost{}
	probed = map[string]probeState{}
	unseen = map[string]time.Time{}
}

func TestMemory(t *testing.T) {
	reset()
	defer reset()
	now := time.Now()
	remember(map[string][]string{"a.example.com": {"websecure"}, "b.example.com": {"websecure"}}, now, time.Minute)
	if held("a.example.com", "websecure", now, time.Minute) {
		t.Fatal("a held while routed")
	}
	remember(map[string][]string{"b.example.com": {"websecure"}}, now.Add(time.Second), time.Minute)
	if !held("a.example.com", "websecure", now.Add(2*time.Second), time.Minute) {
		t.Fatal("a not held once its router went")
	}
	for _, host := range []string{"b.example.com", "never.example.com"} {
		if held(host, "websecure", now.Add(2*time.Second), time.Minute) {
			t.Fatalf("%s held", host)
		}
	}
	if held("a.example.com", "websecure", now.Add(2*time.Minute), time.Minute) {
		t.Fatal("a held past memory")
	}
	// Back: not held, and gone again later counts from then.
	remember(map[string][]string{"a.example.com": {"websecure"}, "b.example.com": {"websecure"}}, now.Add(3*time.Second), time.Minute)
	if held("a.example.com", "websecure", now.Add(4*time.Second), time.Minute) {
		t.Fatal("a held while routed again")
	}
	remember(map[string][]string{}, now.Add(10*time.Minute), time.Minute)
	if !held("a.example.com", "websecure", now.Add(10*time.Minute+time.Second), time.Minute) {
		t.Fatal("a not held when gone again")
	}
}

func TestCatchAll(t *testing.T) {
	reset()
	defer reset()
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "websecure", srv.URL, "10s")
	now := time.Now()
	remember(map[string][]string{"app.example.com": {"websecure"}}, now, time.Minute)
	remember(map[string][]string{}, now, time.Minute)

	// Never routed: Traefik's usual 404, no probe.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://scan.example.com/", nil))
	if rec.Code != http.StatusNotFound || len(ep.probes()) != 0 {
		t.Fatalf("unknown host: %d, %d probes", rec.Code, len(ep.probes()))
	}

	// Routed moments ago: held, then sent back to the same URL.
	time.AfterFunc(50*time.Millisecond, func() { ep.set(true, http.StatusOK) })
	req := httptest.NewRequest("POST", "https://APP.example.com:8443/form?x=1", strings.NewReader("a=1"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "https://APP.example.com:8443/form?x=1" {
		t.Fatalf("held host: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRouterHosts(t *testing.T) {
	pages := map[string]string{
		"1": `[{"rule":"Host(` + "`a.example.com`) || Host(\\\"B.example.com.\\\")" + `)","entryPoints":["web","websecure"],"middlewares":["hold@file"]},
			{"rule":"PathPrefix(` + "`/`" + `)","middlewares":["hold@file"]},
			{"rule":"Host(` + "`d.example.com`" + `)","entryPoints":["websecure"]}]`,
		"2": `[{"rule":"HostRegexp(` + "`^.+\\\\.example\\\\.com$`" + `)","middlewares":["hold@file"]},
			{"rule":"Host( ` + "`c.example.com`" + ` ) && PathPrefix(` + "`/api`" + `)","entryPoints":["tunnel"],"middlewares":["auth@file","hold@file"]},
			{"rule":"Host(` + "`c.example.com`" + `)","entryPoints":["websecure"],"middlewares":["hold@file"]}]`,
	}
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		page := req.URL.Query().Get("page")
		next := "2"
		if page == "2" {
			next = "1"
		}
		rw.Header().Set("X-Next-Page", next)
		_, _ = rw.Write([]byte(pages[page]))
	}))
	defer api.Close()
	hosts, err := routerHosts(api.Client(), api.URL, "hold@file")
	if err != nil {
		t.Fatal(err)
	}
	// d's router does not ask to be held; c's two routers add up.
	want := map[string]string{"a.example.com": "web websecure", "b.example.com": "web websecure", "c.example.com": "tunnel websecure"}
	if len(hosts) != len(want) {
		t.Fatalf("hosts: %v", hosts)
	}
	for host, eps := range want {
		if got, ok := hosts[host]; !ok || strings.Join(got, " ") != eps {
			t.Fatalf("hosts: %v", hosts)
		}
	}
}

func TestNewChecks(t *testing.T) {
	for name, c := range map[string]*Config{
		"no probes":        {MaxWait: "1s", Interval: "1s", Memory: "1m"},
		"bad probe":        {MaxWait: "1s", Interval: "1s", Memory: "1m", Probes: map[string]string{"websecure": "127.0.0.1:443"}},
		"bad wait":         {MaxWait: "soon", Interval: "1s", Memory: "1m", Probes: map[string]string{"websecure": "https://127.0.0.1:443"}},
		"zero interval":    {MaxWait: "1s", Interval: "0s", Memory: "1m", Probes: map[string]string{"websecure": "https://127.0.0.1:443"}},
		"catch-all no api": {EntryPoint: "websecure", MaxWait: "1s", Interval: "1s", Memory: "1m", Probes: map[string]string{"websecure": "https://127.0.0.1:443"}},
		"catch-all no ep":  {EntryPoint: "tunnel", API: "http://127.0.0.1:8080", MaxWait: "1s", Interval: "1s", Memory: "1m", Probes: map[string]string{"websecure": "https://127.0.0.1:443"}},
	} {
		if _, err := New(context.Background(), nil, c, "test"); err == nil {
			t.Fatalf("%s: no error", name)
		}
	}
}

// A change while the reader waits for its second read is read at once: a
// stop right after a start must be held as soon as Traefik drops the router.
func TestRefreshReadsEveryChange(t *testing.T) {
	reset()
	defer reset()
	defer func(d []time.Duration) { readDelays = d }(readDelays)
	readDelays = []time.Duration{5 * time.Second}
	var apiMu sync.Mutex
	routers := "[{\"rule\":\"Host(`a.example.com`)\",\"entryPoints\":[\"websecure\"],\"middlewares\":[\"hold@file\"]}]"
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		apiMu.Lock()
		defer apiMu.Unlock()
		rw.Header().Set("X-Next-Page", "1")
		_, _ = rw.Write([]byte(routers))
	}))
	defer api.Close()
	within := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("not %s within a second", what)
			}
		}
	}

	refresh(api.URL, "hold@file", time.Minute)
	within("routed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return present["a.example.com"] != nil
	})
	apiMu.Lock()
	routers = "[]"
	apiMu.Unlock()
	refresh(api.URL, "hold@file", time.Minute)
	within("held", func() bool { return held("a.example.com", "websecure", time.Now(), time.Minute) })
}

// A request for a host the last read had, while the routers settle after a
// change: Traefik's API may show the change a moment after New, so the reads
// to come tell whether it is held or Traefik's 404.
func TestCatchAllSettling(t *testing.T) {
	reset()
	defer reset()
	defer func(d []time.Duration) { readDelays = d }(readDelays)
	readDelays = []time.Duration{200 * time.Millisecond, 200 * time.Millisecond}
	ep := &entrypoint{}
	ep.set(true, http.StatusOK)
	srv := httptest.NewServer(ep)
	defer srv.Close()
	var apiMu sync.Mutex
	routed := "[{\"rule\":\"Host(`app.example.com`)\",\"entryPoints\":[\"websecure\"],\"middlewares\":[\"hold@file\"]}]"
	routers := routed
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		apiMu.Lock()
		defer apiMu.Unlock()
		rw.Header().Set("X-Next-Page", "1")
		_, _ = rw.Write([]byte(routers))
	}))
	defer api.Close()
	build := func() http.Handler {
		h, err := New(context.Background(), nil, &Config{EntryPoint: "websecure", Middleware: "hold@file", API: api.URL,
			Probes: map[string]string{"websecure": srv.URL}, MaxWait: "10s", Interval: "10ms", Memory: "1m"}, "test")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	settled := func() {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			done := !settling && present != nil
			mu.Unlock()
			if done {
				return
			}
			if time.Now().After(end) {
				t.Fatal("never settled")
			}
		}
	}
	build()
	settled()

	for name, after := range map[string]string{"router gone": "[]", "path router": routed} {
		// The change: New runs, the API shows it 100 ms later.
		h := build()
		time.AfterFunc(100*time.Millisecond, func() {
			apiMu.Lock()
			routers = after
			apiMu.Unlock()
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "https://app.example.com/x", nil))
		want := http.StatusTemporaryRedirect
		if after == routed {
			want = http.StatusNotFound
		}
		if rec.Code != want {
			t.Fatalf("%s: got %d", name, rec.Code)
		}
		settled()
		apiMu.Lock()
		routers = routed
		apiMu.Unlock()
		build()
		settled()
	}
}

// A 502 from a router back on a new container before any read saw it again
// (an app without a healthcheck answers before it listens): held, through
// the entrypoints it had.
func TestErrorsPathAfterRouterCameBack(t *testing.T) {
	reset()
	defer reset()
	now := time.Now()
	remember(map[string][]string{"app.example.com": {"websecure"}}, now, time.Minute)
	remember(map[string][]string{}, now, time.Minute)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")
	time.AfterFunc(50*time.Millisecond, func() { ep.set(true, http.StatusOK) })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("got %d, want a hold ending in 307", rec.Code)
	}
}

// A probe the site keeps waiting never holds the visitor past maxWait.
func TestHoldBoundedByMaxWait(t *testing.T) {
	routed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	h := newHold(t, "", srv.URL, "100ms")
	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
	if d := time.Since(start); d > time.Second || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d after %s, past a maxWait of 100ms", rec.Code, d)
	}
}

// A host gone from websecure is not held on another entrypoint.
func TestCatchAllOtherEntryPoint(t *testing.T) {
	reset()
	defer reset()
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h, err := New(context.Background(), nil, &Config{EntryPoint: "tunnel", Middleware: "hold@file", API: "http://127.0.0.1:1",
		Probes: map[string]string{"tunnel": srv.URL}, MaxWait: "10s", Interval: "10ms", Memory: "1m"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	remember(map[string][]string{"app.example.com": {"websecure"}}, now, time.Minute)
	remember(map[string][]string{}, now, time.Minute)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.example.com/", nil))
	if rec.Code != http.StatusNotFound || len(ep.probes()) != 0 {
		t.Fatalf("got %d, %d probes", rec.Code, len(ep.probes()))
	}
}

// Visitors held on one URL share its probes: one per interval, not one each.
func TestHoldSharesProbes(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")
	time.AfterFunc(300*time.Millisecond, func() { ep.set(true, http.StatusOK) })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("got %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	// About 30 rounds of 10ms while down, then each visitor's last one.
	if n := len(ep.probes()); n > 80 {
		t.Fatalf("%d probes for 20 visitors over 300ms at 10ms", n)
	}
}

// A host no read knows (a first release, a HostRegexp rule) is held through
// every entrypoint: the one without its router answers from the hold, down.
func TestHoldUnknownHost(t *testing.T) {
	reset()
	defer reset()
	down, site := &entrypoint{}, &entrypoint{}
	downSrv, siteSrv := httptest.NewServer(down), httptest.NewServer(site)
	defer downSrv.Close()
	defer siteSrv.Close()
	h, err := New(context.Background(), nil, &Config{Probes: map[string]string{"websecure": downSrv.URL, "tunnel": siteSrv.URL},
		MaxWait: "10s", Interval: "10ms", Memory: "1m"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, func() { site.set(true, http.StatusOK) })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
	if rec.Code != http.StatusTemporaryRedirect || len(down.probes()) == 0 {
		t.Fatalf("got %d, %d probes through websecure", rec.Code, len(down.probes()))
	}
}

// An app that takes longer than an interval to answer is up, slow, and its
// one probe answers every visitor held on the URL: no render per visitor.
func TestHoldSlowAnswer(t *testing.T) {
	routed(t)
	var smu sync.Mutex
	slow, renders := false, 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		smu.Lock()
		s := slow
		if s {
			renders++
		}
		smu.Unlock()
		if !s {
			rw.Header().Set(Header, "down")
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	// An interval longer than the scheduler's jitter: each visitor wakes
	// once within the interval the answer is good for, as with 1s.
	h, err := New(context.Background(), nil, &Config{Probes: map[string]string{"websecure": srv.URL},
		MaxWait: "3s", Interval: "100ms", Memory: "1m"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, func() { smu.Lock(); slow = true; smu.Unlock() })
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("got %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	smu.Lock()
	defer smu.Unlock()
	if renders > 2 {
		t.Fatalf("%d slow probes for 10 visitors", renders)
	}
}

// A 502 the probe does not meet: a container restarted within a moment
// (the stale router's 502, the site up again by the first probe) or a
// request the app itself answers 502 (a POST that crashes it). The hold
// cannot tell them apart, so it redirects once per visitor and URL; a
// second such 502 within ten intervals gets the page, never a loop the
// browser follows, its POST sent again each time.
func TestHoldRedirectsOnlyAfterDown(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	ep.set(true, http.StatusOK)
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")
	from := func(visitor string) *http.Request {
		r := fromErrors("https://app.example.com/form")
		r.Header.Set("X-Real-Ip", visitor)
		return r
	}
	for i, c := range []struct {
		visitor string
		wait    time.Duration
		code    int
	}{
		{"203.0.113.7", 0, http.StatusTemporaryRedirect},
		{"203.0.113.7", 0, http.StatusServiceUnavailable},
		{"203.0.113.8", 0, http.StatusTemporaryRedirect},
		// Past ten intervals (10ms here): its own once again.
		{"203.0.113.7", 150 * time.Millisecond, http.StatusTemporaryRedirect},
	} {
		time.Sleep(c.wait)
		start := time.Now()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, from(c.visitor))
		if rec.Code != c.code || (c.code == http.StatusServiceUnavailable) != (rec.Header().Get("Location") == "") || time.Since(start) > time.Second {
			t.Fatalf("%d, %s: got %d %v after %s, want %d at once", i, c.visitor, rec.Code, rec.Header(), time.Since(start), c.code)
		}
	}

	// An up answer from before the hold is no answer to it: a stop right
	// after another visitor's redirect is probed, seen down, and held.
	mu.Lock()
	probed["https://app.example.com/"] = probeState{at: time.Now(), up: true}
	mu.Unlock()
	ep.set(false, 0)
	time.AfterFunc(50*time.Millisecond, func() { ep.set(true, http.StatusOK) })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/"))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("got %d, want a hold ending in 307", rec.Code)
	}
}

// Visitors on many URLs at once probe at once, each URL its own probe:
// under Yaegi too (app-stop's review).
func TestHoldDistinctURLs(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")
	time.AfterFunc(300*time.Millisecond, func() { ep.set(true, http.StatusOK) })
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, fromErrors(fmt.Sprintf("https://app.example.com/p%d", i)))
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("p%d: got %d", i, rec.Code)
			}
		}(i)
	}
	wg.Wait()
}

// Many catch-all requests waiting for the reads after a change at once:
// none stays asleep (app-stop's review, after Yaegi's select deadlock).
func TestCatchAllSettlingMany(t *testing.T) {
	reset()
	defer reset()
	defer func(d []time.Duration) { readDelays = d }(readDelays)
	readDelays = []time.Duration{200 * time.Millisecond, 200 * time.Millisecond}
	ep := &entrypoint{}
	ep.set(true, http.StatusOK)
	srv := httptest.NewServer(ep)
	defer srv.Close()
	var apiMu sync.Mutex
	routers := "[{\"rule\":\"Host(`app.example.com`)\",\"entryPoints\":[\"websecure\"],\"middlewares\":[\"hold@file\"]}]"
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		apiMu.Lock()
		defer apiMu.Unlock()
		rw.Header().Set("X-Next-Page", "1")
		_, _ = rw.Write([]byte(routers))
	}))
	defer api.Close()
	build := func() http.Handler {
		h, err := New(context.Background(), nil, &Config{EntryPoint: "websecure", Middleware: "hold@file", API: api.URL,
			Probes: map[string]string{"websecure": srv.URL}, MaxWait: "10s", Interval: "10ms", Memory: "1m"}, "test")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	build()
	time.Sleep(time.Second)
	h := build()
	time.AfterFunc(100*time.Millisecond, func() { apiMu.Lock(); routers = "[]"; apiMu.Unlock() })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("https://app.example.com/x%d", i), nil))
			if rec.Code != http.StatusTemporaryRedirect {
				t.Errorf("x%d: got %d", i, rec.Code)
			}
		}(i)
	}
	wg.Wait()
}

// The shared state prunes itself as it goes (entries deleted while ranging
// over the map, which Yaegi must do as Go does): probes older than a
// minute but one running, redirects past their window.
func TestHoldPrunes(t *testing.T) {
	routed(t)
	ep := &entrypoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h := newHold(t, "", srv.URL, "10s")
	old := time.Now().Add(-2 * time.Minute)
	mu.Lock()
	for _, k := range []string{"a", "b", "c", "d"} {
		probed["https://app.example.com/"+k] = probeState{at: old}
		unseen["https://app.example.com/"+k+" 203.0.113.9"] = old
	}
	probed["https://app.example.com/running"] = probeState{probing: true, at: old}
	mu.Unlock()
	time.AfterFunc(50*time.Millisecond, func() { ep.set(true, http.StatusOK) })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, fromErrors("https://app.example.com/new"))
	// A 307 without the site seen down records it, pruning the rest.
	ep.set(true, http.StatusOK)
	r := fromErrors("https://app.example.com/other")
	r.Header.Set("X-Real-Ip", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), r)
	mu.Lock()
	defer mu.Unlock()
	var left []string
	for k := range probed {
		left = append(left, k)
	}
	sort.Strings(left)
	if got := strings.Join(left, " "); got != "https://app.example.com/new https://app.example.com/other https://app.example.com/running" {
		t.Errorf("probes left: %s", got)
	}
	if len(unseen) != 1 {
		t.Errorf("redirects left: %v", unseen)
	}
}
