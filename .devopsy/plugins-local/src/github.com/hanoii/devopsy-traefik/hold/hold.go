// Package hold is a Traefik middleware plugin (run by Traefik's Yaegi
// interpreter: standard library only) that holds a visitor's request while a
// site's app stops and starts again, instead of an error.
//
// Only sites that ask are held: their router names the errors middleware
// that leads here (Config.Middleware, hold@file). Two ways in, each its own
// instance:
//
//   - From that errors middleware, on a 502 (the app's port closed while its
//     container still runs, or its address gone): the request arrives as
//     /?url=<the original URL>, with the visitor's headers, and is probed
//     through an entrypoint its host's router is on.
//   - From a catch-all router at priority 1 (Config.EntryPoint set): a request
//     no router took. Held only when its host lost a router naming the
//     middleware moments ago (Config.Memory); any other host gets Traefik's
//     usual 404 at once.
//
// A held request is answered by the plugin itself, never by calling next: it
// probes the original URL through Traefik's own entrypoint every
// Config.Interval, each probe a new request through Traefik's current routers,
// and once one is answered by anything but the hold itself or a 502 it answers
// a redirect to the same URL, which the visitor's browser follows into the
// site. Nothing of a site's response passes through the plugin, so its flushes
// are untouched (Yaegi loses them through a wrapped writer). Past
// Config.MaxWait it answers its page, 503.
//
// Which hosts have such a router, and on which entrypoints, comes from Traefik's API, read when Traefik builds
// the middleware again, which it does on every change of its routers.
package hold

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/tls"
)

// Header marks the hold's own traffic: a probe sends `probe`, and the hold
// answers it with `down`, so a probe that reached the hold (the site still
// down) is never taken for the site. A visitor sending `no` gets the hold's
// answer at once, for checks that want the site's state, not a wait.
const Header = "X-Devopsy-Hold"

// Page is shown when a hold gives up, unless Config.Page replaces it.
const Page = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Back in a moment</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; margin: 0; min-height: 100vh; display: grid; place-items: center; color: #222; background: #f6f6f4; }
main { max-width: 32rem; padding: 2rem; text-align: center; }
@media (prefers-color-scheme: dark) { body { color: #ddd; background: #1c1c1c; } }
</style>
<main>
<h1>Back in a moment</h1>
<p>This site is being updated. Please try again shortly.</p>
</main>
</html>
`

// Config is the middleware configuration.
type Config struct {
	// EntryPoint names the entrypoint of a catch-all router's instance;
	// empty on the errors middleware's.
	EntryPoint string `json:"entryPoint,omitempty"`
	// Middleware is what a site's router names to be held (hold@file).
	Middleware string `json:"middleware,omitempty"`
	// Probes gives, per entrypoint, the address Traefik listens on for it
	// from inside its container (https://127.0.0.1:443).
	Probes map[string]string `json:"probes,omitempty"`
	// API is Traefik's API (http://127.0.0.1:8080), read for the hosts
	// and entrypoints of the routers naming Middleware; catch-all
	// instances only.
	API string `json:"api,omitempty"`
	// MaxWait bounds a hold (90s); Interval is the time between probes
	// (1s); Memory is how long a host that lost its router is held (5m).
	MaxWait  string `json:"maxWait,omitempty"`
	Interval string `json:"interval,omitempty"`
	Memory   string `json:"memory,omitempty"`
	// Page replaces the page shown when a hold gives up.
	Page string `json:"page,omitempty"`
}

// CreateConfig returns the default configuration.
func CreateConfig() *Config {
	return &Config{Middleware: "hold@file", MaxWait: "90s", Interval: "1s", Memory: "5m"}
}

// Hold is the middleware.
type Hold struct {
	name       string
	entryPoint string
	probes     map[string]*prober
	order      []string // the probes' entrypoints, sorted
	maxWait    time.Duration
	interval   time.Duration
	memory     time.Duration
	page       string
}

// prober sends probes to one of Traefik's entrypoints, whatever host the
// probe names: the host goes in the URL (SNI and Host), the dial goes to the
// entrypoint. No proxy: Traefik's own address.
type prober struct {
	scheme string
	client *http.Client
}

func newProber(address string) (*prober, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("probe address %q: want http(s)://host:port", address)
	}
	addr := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &prober{scheme: u.Scheme, client: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			// Traefik's own certificate, in its own container.
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// up probes target as its visitor would (their headers: a site's own auth
// answers the probe as it answers them), with HEAD: true once something other
// than the hold or a 502 answers. Bounded by ctx only: an app that takes long
// to answer is up, slow.
func (p *prober) up(ctx context.Context, target *url.URL, visitor http.Header) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.scheme+"://"+target.Host+target.RequestURI(), nil)
	if err != nil {
		return false
	}
	for k, v := range visitor {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
			"Content-Length", "Content-Type", "Expect", "X-Forwarded-For", "X-Forwarded-Host",
			"X-Forwarded-Port", "X-Forwarded-Proto", "X-Forwarded-Server", "X-Real-Ip":
			continue
		}
		req.Header[k] = v
	}
	req.Header.Set(Header, "probe")
	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.Header.Get(Header) == "" && resp.StatusCode != http.StatusBadGateway
}

// New builds the middleware. Traefik calls it again whenever its routers
// change: a catch-all instance reads them then.
func New(_ context.Context, _ http.Handler, config *Config, name string) (http.Handler, error) {
	h := &Hold{name: name, entryPoint: config.EntryPoint, probes: map[string]*prober{}, page: config.Page}
	if h.page == "" {
		h.page = Page
	}
	durations := map[string]time.Duration{}
	for field, value := range map[string]string{"maxWait": config.MaxWait, "interval": config.Interval, "memory": config.Memory} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("%s: %s: want a duration above 0, like 90s: %q", name, field, value)
		}
		durations[field] = d
	}
	h.maxWait, h.interval, h.memory = durations["maxWait"], durations["interval"], durations["memory"]
	if len(config.Probes) == 0 {
		return nil, fmt.Errorf("%s: no probes", name)
	}
	for ep, address := range config.Probes {
		// Never `h.probes[ep], err = ...`: Yaegi v0.16.1 stores nothing
		// in a map index on the left of a multiple assignment.
		p, err := newProber(address)
		if err != nil {
			return nil, fmt.Errorf("%s: entrypoint %s: %w", name, ep, err)
		}
		h.probes[ep] = p
		h.order = append(h.order, ep)
	}
	sort.Strings(h.order)
	if h.entryPoint != "" {
		if h.probes[h.entryPoint] == nil {
			return nil, fmt.Errorf("%s: no probe for its entrypoint %s", name, h.entryPoint)
		}
		if config.API == "" || config.Middleware == "" {
			return nil, fmt.Errorf("%s: a catch-all needs the api and the middleware", name)
		}
		refresh(config.API, config.Middleware, h.memory)
	}
	return h, nil
}

func (h *Hold) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// A probe that reached the hold: the site is still down.
	if req.Header.Get(Header) == "probe" {
		rw.Header().Set(Header, "down")
		rw.WriteHeader(http.StatusBadGateway)
		return
	}
	var target *url.URL
	var ps []*prober
	if h.entryPoint == "" {
		// From the errors middleware: /?url=<original>, with the
		// visitor's Host and headers. Only Traefik reaches it (its
		// entrypoint listens on the container's loopback); the URL must
		// still name the request's host, so a redirect never leads away.
		u, err := url.Parse(req.URL.Query().Get("url"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, req.Host) {
			h.giveUp(rw)
			return
		}
		target = u
		// Probed through the first entrypoint of its router's with a
		// probe (the router serves it the same on any of them), else,
		// for a host no read knows (a first release, a HostRegexp rule),
		// through every one: one without its router answers the probe
		// from its catch-all, down.
		for _, ep := range routedOn(hostname(req.Host)) {
			if h.probes[ep] != nil {
				ps = []*prober{h.probes[ep]}
				break
			}
		}
		if ps == nil {
			for _, ep := range h.order {
				ps = append(ps, h.probes[ep])
			}
		}
	} else {
		host := hostname(req.Host)
		if req.Header.Get(Header) == "no" {
			// An answer at once, without waiting for the reads.
			if !held(host, h.entryPoint, time.Now(), h.memory) {
				http.NotFound(rw, req)
				return
			}
		} else if !heldNow(req.Context(), host, h.entryPoint, h.memory) {
			http.NotFound(rw, req)
			return
		}
		ps = []*prober{h.probes[h.entryPoint]}
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		if proto := req.Header.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
			scheme = proto
		}
		target = &url.URL{Scheme: scheme, Host: req.Host, Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery}
	}
	if req.Header.Get(Header) == "no" {
		h.giveUp(rw)
		return
	}
	// A 307 once the site was seen down (h.up): a stop's closed port is
	// still closed at the first probe, made at once; the router gone is
	// that evidence on a catch-all. Through the errors middleware a 502 the
	// probe does not meet is either a container restarted within a moment
	// (the stale router's 502, the site up again) or one the app gives that
	// request itself (a POST that crashes it), which the hold cannot tell
	// apart: one 307 per visitor and URL (unseenOnce), then the page, never
	// a loop the browser follows, a POST sent again each time.
	seenDown := h.entryPoint != ""
	since := time.Now()
	deadline := since.Add(h.maxWait)
	for first := true; ; first = false {
		if !first {
			wait := h.interval
			if left := time.Until(deadline); left < wait {
				wait = left
			}
			// Never a select here: under Yaegi v0.16.1 goroutines
			// running the same select at once deadlock in it, never
			// woken by their timer.
			time.Sleep(wait)
		}
		if req.Context().Err() != nil {
			return
		}
		switch h.up(req.Context(), ps, deadline, since, target, req.Header) {
		case answerUnseen:
			if !seenDown && !h.unseenOnce(target, req.Header) {
				h.giveUp(rw)
				return
			}
			fallthrough
		case answerUp:
			rw.Header().Set("Location", target.String())
			rw.Header().Set("Cache-Control", "no-store")
			rw.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if !time.Now().Before(deadline) {
			h.giveUp(rw)
			return
		}
	}
}

// unseenOnce: whether visitor (its X-Real-Ip, which Traefik sets) had no
// 307 to target without the site seen down within ten intervals, and
// records this one.
func (h *Hold) unseenOnce(target *url.URL, visitor http.Header) bool {
	key := target.String() + " " + visitor.Get("X-Real-Ip")
	now := time.Now()
	window := 10 * h.interval
	mu.Lock()
	defer mu.Unlock()
	for k, at := range unseen {
		if now.Sub(at) >= window {
			delete(unseen, k)
		}
	}
	if _, ok := unseen[key]; ok {
		return false
	}
	unseen[key] = now
	return true
}

// What a probe said: nothing yet (another visitor's in flight or cut),
// down, up, or up with no probe of the URL down since an interval before
// the hold.
const (
	answerNone = iota
	answerDown
	answerUp
	answerUnseen
)

// up: whether target answers, probed through ps, shared by every request
// held on it: one probe at a time per URL, its answer good for an
// interval (up, two), so a hundred visitors on a stopped page cost one probe a
// second, and one slow answer (an app warming up) redirects them all. An up
// answer from before since (the hold's start) is probed again: it may be from
// before the stop. Up is unseen when no probe of the URL, this visitor's or
// another's, answered down since an interval before since: the 502 was not
// a stop. A probe waits until the deadline at most.
func (h *Hold) up(ctx context.Context, ps []*prober, deadline, since time.Time, target *url.URL, visitor http.Header) int {
	key := target.String()
	now := time.Now()
	mu.Lock()
	st := probed[key]
	if st.probing {
		mu.Unlock()
		return answerNone
	}
	// Up is good for two intervals, so every visitor held on the URL,
	// each waking once an interval, takes it.
	good := h.interval
	if st.up {
		good = 2 * h.interval
	}
	if now.Sub(st.at) < good && (!st.up || !st.at.Before(since)) {
		mu.Unlock()
		return st.answer(since.Add(-h.interval))
	}
	probed[key] = probeState{probing: true, at: st.at, up: st.up, down: st.down}
	for k, v := range probed {
		if !v.probing && now.Sub(v.at) > time.Minute {
			delete(probed, k)
		}
	}
	mu.Unlock()
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	up := false
	for _, p := range ps {
		if up = p.up(pctx, target, visitor); up {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !up && pctx.Err() != nil {
		// Cut by the visitor leaving or the deadline: no answer.
		probed[key] = st
		return answerNone
	}
	st = probeState{at: time.Now(), up: up, down: st.down}
	if !up {
		st.down = st.at
	}
	probed[key] = st
	return st.answer(since.Add(-h.interval))
}

func (st probeState) answer(downSince time.Time) int {
	switch {
	case !st.up:
		return answerDown
	case st.down.Before(downSince):
		return answerUnseen
	}
	return answerUp
}

// giveUp answers the page, 503. Through the errors middleware the visitor gets
// the status it rewrites the 502 to, with these headers and this page.
func (h *Hold) giveUp(rw http.ResponseWriter) {
	rw.Header().Set(Header, "down")
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Retry-After", strconv.Itoa(30))
	rw.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprint(rw, h.page)
}

func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// The hosts the routers naming the middleware have, with their entrypoints,
// and when hosts lost theirs. Package-level: Traefik builds the middleware
// again on every change, and one interpreter runs the plugin's every
// instance.
var (
	mu      sync.Mutex
	present map[string][]string // nil until the first read
	gone    = map[string]goneHost{}
	// The last probe of each URL held on, shared by its held requests.
	probed = map[string]probeState{}
	// When each visitor and URL last had a 307 without the site seen
	// down (unseenOnce).
	unseen = map[string]time.Time{}
	// The reader's: started once, kicked by every New, reading what the
	// last New gave; settling while reads after a change are due, reads
	// counting those done.
	reader                  bool
	kick                    = make(chan struct{}, 1)
	readAPI, readMiddleware string
	readMemory              time.Duration
	settling                bool
	reads                   int
)

// probeState: a probe of a URL running, or when the last one answered and
// what, and when one last answered down.
type probeState struct {
	probing bool
	at      time.Time
	up      bool
	down    time.Time
}

// goneHost: when a host lost its routers, and their entrypoints.
type goneHost struct {
	at  time.Time
	eps []string
}

// Literal hosts only: a HostRegexp names none.
var hostRule = regexp.MustCompile("Host\\(\\s*[`\"]([^`\"]+)[`\"]\\s*\\)")

// remember records the hosts routers have now: one that had a router at the
// last read and has none now lost it now.
func remember(hosts map[string][]string, now time.Time, memory time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	for host, eps := range present {
		if _, ok := hosts[host]; !ok {
			gone[host] = goneHost{at: now, eps: eps}
		}
	}
	for host, g := range gone {
		if _, ok := hosts[host]; ok || now.Sub(g.at) > memory {
			delete(gone, host)
		}
	}
	present = hosts
	reads++
}

// held: host lost its router on entryPoint within memory, and has none
// again yet.
func held(host, entryPoint string, now time.Time, memory time.Duration) bool {
	mu.Lock()
	defer mu.Unlock()
	g, ok := gone[host]
	return ok && now.Sub(g.at) <= memory && contains(g.eps, entryPoint)
}

// settle waits, up to end, for the next read of the routers while reads
// after a change are due: false once none is, or the visitor gave up.
func settle(ctx context.Context, end time.Time) bool {
	mu.Lock()
	n, due := reads, settling
	mu.Unlock()
	for due && time.Now().Before(end) {
		time.Sleep(50 * time.Millisecond) // no select: as in ServeHTTP
		if ctx.Err() != nil {
			return false
		}
		mu.Lock()
		moved := reads != n || !settling
		mu.Unlock()
		if moved {
			return true
		}
	}
	return false
}

// heldNow is held, waiting while the routers settle after a change for a
// host the last read had: Traefik's API shows a change about when Traefik
// calls New, sometimes a moment after, so a request for a host whose router
// just went may come before the read that tells (measured on 3.7.16).
// Bounded by settleWait and the visitor's wait.
func heldNow(ctx context.Context, host, entryPoint string, memory time.Duration) bool {
	end := time.Now().Add(settleWait)
	for {
		if held(host, entryPoint, time.Now(), memory) {
			return true
		}
		mu.Lock()
		_, routed := present[host]
		mu.Unlock()
		if !routed || !settle(ctx, end) {
			return false
		}
	}
}

// routedOn: the entrypoints of host's routers, or of those it lost within
// memory (its router back on a new container answering 502 before a read
// saw it again). Nil when no read knows it.
func routedOn(host string) []string {
	mu.Lock()
	defer mu.Unlock()
	if eps := present[host]; eps != nil {
		return eps
	}
	return gone[host].eps
}

// readDelays: the reads after a change, after the one at once, each after
// the one before; settleWait bounds a request's wait for them.
var (
	readDelays = []time.Duration{250 * time.Millisecond, 750 * time.Millisecond, 2 * time.Second}
	settleWait = 5 * time.Second
)

// refresh has the routers read at once and again after readDelays. One
// reader: several instances are built on every change, and a change while
// the reads of another are due starts them over.
func refresh(api, middleware string, memory time.Duration) {
	mu.Lock()
	readAPI, readMiddleware, readMemory = api, middleware, memory
	settling = true
	start := !reader
	reader = true
	// Under the lock, so the reader never reads settling over before the
	// kick is there; refreshers send only here, so it never blocks.
	if len(kick) == 0 {
		kick <- struct{}{}
	}
	mu.Unlock()
	if start {
		go read()
	}
}

func read() {
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{}}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	next := len(readDelays)
	for {
		select {
		case <-kick:
			next = 0
		case <-timer.C:
		}
		timer.Stop()
		mu.Lock()
		api, middleware, memory := readAPI, readMiddleware, readMemory
		mu.Unlock()
		if hosts, err := routerHosts(client, api, middleware); err == nil {
			remember(hosts, time.Now(), memory)
		}
		mu.Lock()
		if next < len(readDelays) {
			timer.Reset(readDelays[next])
			next++
			settling = true
		} else {
			settling = len(kick) > 0
		}
		mu.Unlock()
	}
}

// routerHosts lists every literal host in the rules of Traefik's HTTP routers
// that name middleware, through the API's pages, with the entrypoints of
// those routers in their order.
func routerHosts(client *http.Client, api, middleware string) (map[string][]string, error) {
	hosts := map[string][]string{}
	// Not `for page := 1; ; {`: Yaegi v0.16.1 keeps a for statement's
	// variable per iteration, so the body's `page = next` never reached
	// the next one, and the loop read the first page forever.
	page := 1
	for {
		resp, err := client.Get(strings.TrimSuffix(api, "/") + "/api/http/routers?per_page=100&page=" + strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		var routers []struct {
			Rule        string   `json:"rule"`
			EntryPoints []string `json:"entryPoints"`
			Middlewares []string `json:"middlewares"`
		}
		err = json.NewDecoder(resp.Body).Decode(&routers)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", api, resp.Status)
		}
		if err != nil {
			return nil, err
		}
		for _, r := range routers {
			if !contains(r.Middlewares, middleware) {
				continue
			}
			for _, m := range hostRule.FindAllStringSubmatch(r.Rule, -1) {
				host := hostname(m[1])
				eps := hosts[host]
				if eps == nil {
					eps = []string{}
				}
				for _, ep := range r.EntryPoints {
					if !contains(eps, ep) {
						eps = append(eps, ep)
					}
				}
				hosts[host] = eps
			}
		}
		// The last page names the first as the next.
		next, _ := strconv.Atoi(resp.Header.Get("X-Next-Page"))
		if next <= page {
			return hosts, nil
		}
		page = next
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
