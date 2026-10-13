// Package hold is a Traefik middleware plugin (run by Traefik's Yaegi
// interpreter: standard library only) that holds requests while their
// backend is briefly unavailable: restarting, unhealthy or being recreated.
//
// The first status the backend writes decides. One not in the list passes
// through untouched. One in the list (502, 503 and 504 by default: Traefik's
// own answers for a backend it cannot reach) is discarded, and the request is
// tried again every interval, its body replayed, until maxWait or until the
// client goes away. Then the configured page is answered as a 503, or else
// the last try's response goes through as it is. A response with
// Retry-After is never held: the backend is down on purpose and says so.
// A 503 is retried for any method: nothing was forwarded (Traefik's own,
// for a router without a healthy backend) or the backend refused before
// acting. A 502 or 504 is retried for GET, HEAD and OPTIONS only: the
// backend may have received the request and acted on it before the
// connection broke or the timeout passed, and a replayed POST would act
// twice.
//
// Bodies up to maxBody are kept in memory for the retries; a bigger one gets
// a single try. Instances nest: an instance on a router, with its own page or
// limits, takes over from one on the entrypoint. A request with
// "X-Devopsy-Hold: no" is never held.
package hold

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Config is the middleware configuration.
type Config struct {
	// MaxWait is the longest a request is held, as a Go duration. Keep it
	// under any proxy's or CDN's own timeout (Cloudflare's is 100s).
	MaxWait string `json:"maxWait,omitempty"`
	// Interval is the pause between tries.
	Interval string `json:"interval,omitempty"`
	// Statuses are the backend answers that mean "not ready yet".
	Statuses []int `json:"statuses,omitempty"`
	// MaxBody is the largest request body kept for retries, in bytes.
	MaxBody int `json:"maxBody,omitempty"`
	// Page is HTML answered with a 503 once maxWait is over. Empty: the
	// last response goes through instead.
	Page string `json:"page,omitempty"`
}

// CreateConfig returns the default configuration. Statuses are defaulted in
// New: Traefik decodes a configured list over the default one element by
// element, so [503] would become [503, 503, 504].
func CreateConfig() *Config {
	return &Config{MaxWait: "60s", Interval: "1s", MaxBody: 1 << 20}
}

// skipHeader set to "no" by a client skips holding: a tool looking at a
// site (devopsy-template-traefik's domains) gets the backend's answer at once
// instead of waiting. Anyone can send it, and only gets the raw 502 or
// 503 for it.
const skipHeader = "X-Devopsy-Hold"

// stateKey finds the enclosing instance's state in a request's context.
const stateKey = "github.com/hanoii/devopsy-traefik/hold"

// state lets an inner instance take over from an outer one.
type state struct {
	claimed bool
}

// Hold is the middleware.
type Hold struct {
	next     http.Handler
	maxWait  time.Duration
	interval time.Duration
	statuses map[int]bool
	maxBody  int64
	page     []byte
}

// New builds the middleware.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	h := &Hold{next: next, statuses: map[int]bool{}, maxBody: int64(config.MaxBody)}
	var err error
	if h.maxWait, err = duration(config.MaxWait, "60s"); err != nil {
		return nil, fmt.Errorf("%s: maxWait: %w", name, err)
	}
	if h.interval, err = duration(config.Interval, "1s"); err != nil {
		return nil, fmt.Errorf("%s: interval: %w", name, err)
	}
	if h.interval <= 0 {
		return nil, fmt.Errorf("%s: interval must be positive", name)
	}
	statuses := config.Statuses
	if len(statuses) == 0 {
		statuses = []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	}
	for _, s := range statuses {
		if s < 200 || s > 599 {
			return nil, fmt.Errorf("%s: not a final HTTP status: %d", name, s)
		}
		h.statuses[s] = true
	}
	if h.maxBody < 0 {
		return nil, fmt.Errorf("%s: maxBody must not be negative", name)
	}
	if config.Page != "" {
		h.page = []byte(config.Page)
	}
	return h, nil
}

func duration(s, def string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	return time.ParseDuration(s)
}

func (h *Hold) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Inside another instance: take over, so the outer one passes our
	// answers through instead of holding them again.
	if outer, ok := req.Context().Value(stateKey).(*state); ok && outer != nil {
		outer.claimed = true
	}
	if h.maxWait <= 0 || streaming(req) || req.Header.Get(skipHeader) == "no" {
		h.next.ServeHTTP(rw, req)
		return
	}

	// Keep the body for retries, up to maxBody.
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		buf, err := io.ReadAll(io.LimitReader(req.Body, h.maxBody+1))
		if err != nil {
			http.Error(rw, "Bad Request", http.StatusBadRequest)
			return
		}
		if int64(len(buf)) > h.maxBody {
			// Too big to keep: one try, the read part first.
			req.Body = &replayBody{r: io.MultiReader(bytes.NewReader(buf), req.Body), c: req.Body}
			h.next.ServeHTTP(rw, req)
			return
		}
		_ = req.Body.Close()
		body = buf
	}

	st := &state{}
	ctx := context.WithValue(req.Context(), stateKey, st)
	deadline := time.Now().Add(h.maxWait)
	for {
		// Each try gets its own copy of the request: what follows
		// rewrites it in place (Traefik's forwarder adds X-Forwarded-*
		// headers, stripPrefix changes the path), and a second try must
		// not start from the first's leftovers.
		try := req.Clone(ctx)
		if body != nil {
			try.Body = io.NopCloser(bytes.NewReader(body))
			try.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		// Without a page the last try answers whatever the backend says.
		last := time.Now().Add(h.interval).After(deadline)
		if last && h.page == nil {
			h.next.ServeHTTP(rw, try)
			return
		}
		c := &catcher{rw: rw, header: http.Header{}, statuses: h.statuses, st: st, safe: safeMethod(req.Method)}
		h.next.ServeHTTP(c, try)
		if !c.caught {
			return
		}
		if last {
			break
		}
		t := time.NewTimer(h.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			// The client is gone: nobody to answer.
			return
		case <-t.C:
		}
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Retry-After", "30")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusServiceUnavailable)
	_, _ = rw.Write(h.page)
}

// streaming requests skip holding: Yaegi's wrapper for a ResponseWriter
// implemented by a plugin exposes Hijack but not Flush, so a held response
// is only sent as buffers fill. Event streams and gRPC need every flush.
func streaming(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept"), "text/event-stream") ||
		strings.HasPrefix(req.Header.Get("Content-Type"), "application/grpc")
}

// replayBody is a body whose first bytes were already read.
type replayBody struct {
	r io.Reader
	c io.Closer
}

func (b *replayBody) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *replayBody) Close() error { return b.c.Close() }

// catcher decides on the first status, like Traefik's errors middleware: a
// status in the list is discarded with its headers and body, anything else
// goes through untouched.
type catcher struct {
	rw       http.ResponseWriter
	header   http.Header
	statuses map[int]bool
	st       *state
	safe     bool
	decided  bool
	caught   bool
}

// safeMethod is a method a proxy may repeat: it changes nothing (RFC
// 9110, "safe methods").
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// retriable is a status in the list that this request may be tried
// again on: a 503 whatever the method (nothing was forwarded, or the
// backend refused before acting), any other only for a safe method.
func (c *catcher) retriable(code int) bool {
	return c.statuses[code] && (c.safe || code == http.StatusServiceUnavailable)
}

func (c *catcher) Header() http.Header {
	if c.decided && !c.caught {
		return c.rw.Header()
	}
	return c.header
}

// pass copies the headers written so far and lets everything through.
func (c *catcher) pass() {
	c.decided = true
	dst := c.rw.Header()
	for k, v := range c.header {
		dst[k] = v
	}
}

func (c *catcher) WriteHeader(code int) {
	if c.decided {
		if !c.caught {
			c.rw.WriteHeader(code)
		}
		return
	}
	// Informational answers (103 Early Hints) do not decide.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		dst := c.rw.Header()
		for k, v := range c.header {
			dst[k] = v
		}
		c.rw.WriteHeader(code)
		return
	}
	// Let through what an inner instance answers after holding itself, and
	// answers with Retry-After: the backend is down on purpose (a
	// maintenance page) and says so.
	if c.retriable(code) && !c.st.claimed && c.header.Get("Retry-After") == "" {
		c.decided = true
		c.caught = true
		return
	}
	c.pass()
	c.rw.WriteHeader(code)
}

func (c *catcher) Write(b []byte) (int, error) {
	if !c.decided {
		c.WriteHeader(http.StatusOK)
	}
	if c.caught {
		return len(b), nil
	}
	return c.rw.Write(b)
}

// Flush works natively (tests); under Yaegi it is never reached (see
// streaming).
func (c *catcher) Flush() {
	if !c.decided {
		c.WriteHeader(http.StatusOK)
	}
	if c.caught {
		return
	}
	if f, ok := c.rw.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack hands the connection over for protocol upgrades (websockets). The
// proxy hijacks before writing any status: that is a pass.
func (c *catcher) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if c.caught {
		return nil, nil, errors.New("hold: response already discarded")
	}
	if !c.decided {
		c.pass()
	}
	h, ok := c.rw.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hold: %T cannot hijack", c.rw)
	}
	return h.Hijack()
}
