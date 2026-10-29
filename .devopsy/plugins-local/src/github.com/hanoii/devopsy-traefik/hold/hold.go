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
		Timeout: 10 * time.Second,
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
// than the hold or a 502 answers.
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
	}
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
	var p *prober
	if h.entryPoint == "" {
		// From the errors middleware: /?url=<original>, with the
		// visitor's Host and headers. Only Traefik reaches it (its
		// entrypoint listens on the container's loopback); the URL must
		// still name the request's host, so a redirect never leads away.
		// Probed through the first entrypoint of its router's with a
		// probe: the router serves it the same on any of them.
		for _, ep := range entryPoints(hostname(req.Host)) {
			if h.probes[ep] != nil {
				p = h.probes[ep]
				break
			}
		}
		u, err := url.Parse(req.URL.Query().Get("url"))
		if p == nil || err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, req.Host) {
			h.giveUp(rw)
			return
		}
		target = u
	} else {
		if !held(hostname(req.Host), time.Now(), h.memory) {
			http.NotFound(rw, req)
			return
		}
		p = h.probes[h.entryPoint]
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
	deadline := time.Now().Add(h.maxWait)
	for {
		select {
		case <-req.Context().Done():
			return
		case <-time.After(h.interval):
		}
		if p.up(req.Context(), target, req.Header) {
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
	gone    = map[string]time.Time{}
	reading bool
	again   bool
)

// Literal hosts only: a HostRegexp names none.
var hostRule = regexp.MustCompile("Host\\(\\s*`([^`]+)`\\s*\\)")

// remember records the hosts routers have now: one that had a router at the
// last read and has none now lost it now.
func remember(hosts map[string][]string, now time.Time, memory time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	for host := range present {
		if _, ok := hosts[host]; !ok {
			gone[host] = now
		}
	}
	for host, at := range gone {
		if _, ok := hosts[host]; ok || now.Sub(at) > memory {
			delete(gone, host)
		}
	}
	present = hosts
}

// held: host lost its router within memory, and has none again yet.
func held(host string, now time.Time, memory time.Duration) bool {
	mu.Lock()
	defer mu.Unlock()
	at, ok := gone[host]
	return ok && now.Sub(at) <= memory
}

// entryPoints: those of host's routers, nil when it has none.
func entryPoints(host string) []string {
	mu.Lock()
	defer mu.Unlock()
	return present[host]
}

// readDelay is when the routers are read again after a change: Traefik's API
// has shown the new routers in New so far, and the second read keeps that
// from mattering.
var readDelay = 2 * time.Second

// refresh reads the routers now and again after readDelay, once at a time:
// several instances are built on every change.
func refresh(api, middleware string, memory time.Duration) {
	mu.Lock()
	if reading {
		again = true
		mu.Unlock()
		return
	}
	reading = true
	mu.Unlock()
	go func() {
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{}}
		for {
			for i := 0; i < 2; i++ {
				if i > 0 {
					time.Sleep(readDelay)
				}
				if hosts, err := routerHosts(client, api, middleware); err == nil {
					remember(hosts, time.Now(), memory)
				}
			}
			mu.Lock()
			if !again {
				reading = false
				mu.Unlock()
				return
			}
			again = false
			mu.Unlock()
		}
	}()
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
