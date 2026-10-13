package hold

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func build(t *testing.T, cfg *Config, next http.Handler) http.Handler {
	t.Helper()
	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func config(mod func(*Config)) *Config {
	c := CreateConfig()
	c.Interval = "10ms"
	if mod != nil {
		mod(c)
	}
	return c
}

// failing answers 503 the first n times, then 200 with the request body.
func failing(n int32, tries *int32) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		i := atomic.AddInt32(tries, 1)
		if i <= n {
			rw.Header().Set("X-Try", "failed")
			rw.WriteHeader(http.StatusServiceUnavailable)
			_, _ = rw.Write([]byte("Service Unavailable"))
			return
		}
		b, _ := io.ReadAll(req.Body)
		rw.Header().Set("X-Try", "ok")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok:" + string(b)))
	})
}

func TestPassThrough(t *testing.T) {
	var tries int32
	h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&tries, 1)
		rw.Header().Set("X-App", "1")
		rw.WriteHeader(http.StatusNotFound)
		_, _ = rw.Write([]byte("nope"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 404 || rec.Body.String() != "nope" || rec.Header().Get("X-App") != "1" || tries != 1 {
		t.Fatalf("got %d %q %v after %d tries", rec.Code, rec.Body.String(), rec.Header(), tries)
	}

	// Implicit 200 from a plain Write.
	h = build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("hi"))
	}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || rec.Body.String() != "hi" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestHoldThenSuccess(t *testing.T) {
	var tries int32
	h := build(t, config(nil), failing(3, &tries))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("payload")))
	if rec.Code != 200 || rec.Body.String() != "ok:payload" || rec.Header().Get("X-Try") != "ok" || tries != 4 {
		t.Fatalf("got %d %q %v after %d tries", rec.Code, rec.Body.String(), rec.Header(), tries)
	}
}

func TestBodyReplayed(t *testing.T) {
	var bodies []string
	h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(b))
		if len(bodies) < 3 {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rw.WriteHeader(http.StatusCreated)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/", strings.NewReader("abc")))
	if rec.Code != 201 || strings.Join(bodies, ",") != "abc,abc,abc" {
		t.Fatalf("got %d, bodies %q", rec.Code, bodies)
	}
}

// Every try starts from the original request: what follows rewrites
// headers and the path in place, as Traefik's forwarder and stripPrefix do.
func TestEachTryFresh(t *testing.T) {
	var seen []string
	h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		seen = append(seen, req.URL.Path+" "+req.Header.Get("X-Forwarded-For"))
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api")
		req.Header.Add("X-Forwarded-For", "10.0.0.1")
		if len(seen) < 3 {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	if rec.Code != 200 || strings.Join(seen, ",") != "/api/x ,/api/x ,/api/x " {
		t.Fatalf("got %d, tries saw %q", rec.Code, seen)
	}
}

func TestMaxWaitWithoutPage(t *testing.T) {
	var tries int32
	h := build(t, config(func(c *Config) { c.MaxWait = "100ms" }), failing(1000, &tries))
	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if d := time.Since(start); d < 80*time.Millisecond || d > time.Second {
		t.Fatalf("held %s", d)
	}
	// The last try's response, as the backend gave it.
	if rec.Code != 503 || rec.Body.String() != "Service Unavailable" || rec.Header().Get("X-Try") != "failed" || tries < 3 {
		t.Fatalf("got %d %q %v after %d tries", rec.Code, rec.Body.String(), rec.Header(), tries)
	}
}

func TestMaxWaitWithPage(t *testing.T) {
	var tries int32
	h := build(t, config(func(c *Config) {
		c.MaxWait = "100ms"
		c.Page = "<h1>Back soon</h1>"
	}), failing(1000, &tries))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || rec.Body.String() != "<h1>Back soon</h1>" || rec.Header().Get("X-Try") != "" ||
		rec.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		rec.Header().Get("Retry-After") != "30" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("got %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

func TestStatuses(t *testing.T) {
	var tries int32
	h := build(t, config(func(c *Config) { c.Statuses = []int{502} }), failing(2, &tries))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || tries != 1 {
		t.Fatalf("503 is not in the list: got %d after %d tries", rec.Code, tries)
	}
}

// A 503 is retried for any method; a 502 or 504 only for a safe one: the
// backend may have acted on a POST before the connection broke.
func TestUnsafeMethods(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
		tries  int32
		code   int
	}{
		{"POST", 503, 3, 200}, {"POST", 502, 1, 502}, {"PUT", 504, 1, 504}, {"DELETE", 502, 1, 502},
		{"GET", 502, 3, 200}, {"HEAD", 504, 3, 200}, {"OPTIONS", 502, 3, 200}, {"GET", 503, 3, 200},
	} {
		var tries int32
		h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			if atomic.AddInt32(&tries, 1) < 3 {
				rw.WriteHeader(tc.status)
				return
			}
			rw.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, "/", strings.NewReader("x")))
		if rec.Code != tc.code || tries != tc.tries {
			t.Errorf("%s on %d: got %d after %d tries, want %d after %d", tc.method, tc.status, rec.Code, tries, tc.code, tc.tries)
		}
	}
}

// X-Devopsy-Hold: no gets the backend's first answer, held or not.
func TestSkipHeader(t *testing.T) {
	var tries int32
	h := build(t, config(nil), failing(3, &tries))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Devopsy-Hold", "no")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 503 || tries != 1 {
		t.Fatalf("got %d after %d tries", rec.Code, tries)
	}
}

func TestOversizedBodySingleTry(t *testing.T) {
	var tries int32
	h := build(t, config(func(c *Config) { c.MaxBody = 4 }), failing(1, &tries))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("too long")))
	if rec.Code != 503 || tries != 1 {
		t.Fatalf("got %d after %d tries", rec.Code, tries)
	}

	// The whole body still reaches the backend.
	tries = 0
	h = build(t, config(func(c *Config) { c.MaxBody = 4 }), failing(0, &tries))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader("too long")))
	if rec.Code != 200 || rec.Body.String() != "ok:too long" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestContextCancelStops(t *testing.T) {
	var tries int32
	h := build(t, config(nil), failing(1000, &tries))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if d := time.Since(start); d > time.Second {
		t.Fatalf("held %s after the client left", d)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("answered a client that left: %q", rec.Body.String())
	}
}

func TestRetryAfterPasses(t *testing.T) {
	var tries int32
	h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&tries, 1)
		rw.Header().Set("Retry-After", "30")
		rw.WriteHeader(http.StatusServiceUnavailable)
		_, _ = rw.Write([]byte("maintenance"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || rec.Body.String() != "maintenance" || tries != 1 {
		t.Fatalf("got %d %q after %d tries", rec.Code, rec.Body.String(), tries)
	}
}

func TestNested(t *testing.T) {
	var tries int32
	inner := build(t, config(func(c *Config) {
		c.MaxWait = "50ms"
		c.Page = "inner page"
	}), failing(1000, &tries))
	outer := build(t, config(func(c *Config) { c.MaxWait = "5s" }), inner)
	start := time.Now()
	rec := httptest.NewRecorder()
	outer.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the outer instance held the inner one's page: %s", d)
	}
	if rec.Code != 503 || rec.Body.String() != "inner page" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestDisabled(t *testing.T) {
	var tries int32
	h := build(t, config(func(c *Config) { c.MaxWait = "0" }), failing(1, &tries))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 || tries != 1 {
		t.Fatalf("got %d after %d tries", rec.Code, tries)
	}
}

func TestBadConfig(t *testing.T) {
	for _, c := range []*Config{
		config(func(c *Config) { c.MaxWait = "soon" }),
		config(func(c *Config) { c.Interval = "0s" }),
		config(func(c *Config) { c.Statuses = []int{101} }),
		config(func(c *Config) { c.MaxBody = -1 }),
	} {
		if _, err := New(context.Background(), http.NotFoundHandler(), c, "test"); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

// Compiled only: under Yaegi (yaegi test), the catcher reaches the backend
// without Flush, which is why streaming requests skip holding.
func TestFlushPassesThrough(t *testing.T) {
	flusher := true
	h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("chunk"))
		f, ok := rw.(http.Flusher)
		if !ok {
			flusher = false
			return
		}
		f.Flush()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !flusher {
		t.Skip("no Flusher: Yaegi")
	}
	if !rec.Flushed || rec.Body.String() != "chunk" {
		t.Fatalf("flushed %v, %q", rec.Flushed, rec.Body.String())
	}
}

// A websocket-style upgrade: the backend hijacks before writing a status.
func TestHijackPassThrough(t *testing.T) {
	h := build(t, config(nil), http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		conn, brw, err := rw.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		line, _ := brw.ReadString('\n')
		_, _ = brw.WriteString("echo " + line)
		_ = brw.Flush()
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\n"))
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("got %v %v", resp, err)
	}
	_, _ = conn.Write([]byte("hello\n"))
	line, _ := r.ReadString('\n')
	if line != "echo hello\n" {
		t.Fatalf("got %q", line)
	}
}
