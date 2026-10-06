package realip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, h http.Handler, remote string, headers map[string]string) *http.Request {
	t.Helper()
	var got *http.Request
	h.(*RealIP).next = http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { got = req })
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestRealIP(t *testing.T) {
	h, err := New(context.Background(), nil, &Config{Proxies: []Proxy{
		{Name: "cloudflare", Header: "CF-Connecting-IP", TrustedIPs: []string{"162.158.0.0/15"}},
		{Name: "fastly", Header: "Fastly-Client-IP", TrustedIPs: []string{"151.101.0.0/16"}},
		{Name: "lb", Header: "X-Forwarded-For", TrustedIPs: []string{"10.0.0.0/16"}},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Cloudflare: its header, not Fastly's.
	got := serve(t, h, "162.158.154.69:1", map[string]string{"Cf-Connecting-Ip": "181.106.103.254", "Fastly-Client-IP": "9.9.9.9"})
	if got.RemoteAddr != "181.106.103.254:1" || got.Header.Get("X-Real-Ip") != "181.106.103.254" {
		t.Fatalf("cloudflare: %s %v", got.RemoteAddr, got.Header)
	}

	// Fastly.
	got = serve(t, h, "151.101.1.1:2", map[string]string{"Fastly-Client-Ip": "203.0.113.5"})
	if got.RemoteAddr != "203.0.113.5:2" {
		t.Fatalf("fastly: %s", got.RemoteAddr)
	}

	// Load balancer with X-Forwarded-For: rightmost untrusted, past another
	// trusted hop; the header ends up removed for Traefik to rebuild.
	got = serve(t, h, "10.0.0.7:3", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.20, 10.0.0.9"})
	if got.RemoteAddr != "198.51.100.20:3" || got.Header.Get("X-Forwarded-For") != "" {
		t.Fatalf("lb: %s %v", got.RemoteAddr, got.Header)
	}

	// Direct, with spoofed proxy headers: removed, client unchanged.
	got = serve(t, h, "198.51.100.7:4", map[string]string{"Cf-Connecting-Ip": "1.2.3.4", "Fastly-Client-Ip": "1.2.3.4"})
	if got.RemoteAddr != "198.51.100.7:4" || got.Header.Get("Cf-Connecting-Ip") != "" || got.Header.Get("Fastly-Client-Ip") != "" {
		t.Fatalf("direct: %s %v", got.RemoteAddr, got.Header)
	}

	// Trusted proxy without a usable header: unchanged.
	got = serve(t, h, "162.158.154.69:5", map[string]string{"Cf-Connecting-Ip": "nope"})
	if got.RemoteAddr != "162.158.154.69:5" {
		t.Fatalf("invalid header: %s", got.RemoteAddr)
	}

	for _, bad := range []*Config{{}, {Proxies: []Proxy{{Name: "x", Header: "X"}}}, {Proxies: []Proxy{{Name: "x", Header: "X", TrustedIPs: []string{"nope"}}}}} {
		if _, err := New(context.Background(), nil, bad, "test"); err == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}
