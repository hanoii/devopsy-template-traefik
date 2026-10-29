package proxies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, h http.Handler, remote string, headers map[string]string) *http.Request {
	t.Helper()
	var got *http.Request
	h.(*Proxies).next = http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { got = req })
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestProxies(t *testing.T) {
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

	// A proxy passes its visitor's own X-Forwarded-Host, -Port and -Prefix,
	// which an entrypoint trusting it keeps: the host and port become the
	// request's, the others go.
	got = serve(t, h, "162.158.154.69:6", map[string]string{"Cf-Connecting-Ip": "181.106.103.254",
		"X-Forwarded-Proto": "https", "X-Forwarded-Host": "evil.example", "X-Forwarded-Port": "8443",
		"X-Forwarded-Prefix": "/x"})
	if got.Header.Get("X-Forwarded-Host") != "example.com" || got.Header.Get("X-Forwarded-Port") != "443" ||
		got.Header.Get("X-Forwarded-Prefix") != "" {
		t.Fatalf("spoofed forwarded headers: %v", got.Header)
	}
	got = serve(t, h, "162.158.154.69:7", map[string]string{"X-Forwarded-Proto": "http", "X-Forwarded-Port": "8443"})
	if got.Header.Get("X-Forwarded-Port") != "80" {
		t.Fatalf("http port: %v", got.Header)
	}
	// As Traefik sets them: a websocket over TLS is wss, and a Host with a
	// port gives that port.
	got = serve(t, h, "162.158.154.69:8", map[string]string{"X-Forwarded-Proto": "wss"})
	if got.Header.Get("X-Forwarded-Port") != "443" {
		t.Fatalf("wss port: %v", got.Header)
	}
	got = serve(t, h, "162.158.154.69:9", map[string]string{"Host": "example.com:8443", "X-Forwarded-Proto": "https"})
	if got.Header.Get("X-Forwarded-Port") != "8443" || got.Header.Get("X-Forwarded-Host") != "example.com:8443" {
		t.Fatalf("host's port: %v", got.Header)
	}
	// The rest Traefik removes from an untrusted peer goes, X-Real-Ip
	// included when the proxy's header gives no visitor.
	got = serve(t, h, "162.158.154.69:10", map[string]string{"Cf-Connecting-Ip": "nope", "X-Real-Ip": "1.2.3.4",
		"X-Forwarded-Uri": "/admin", "X-Forwarded-Method": "POST", "X-Forwarded-Tls-Client-Cert": "x",
		"X-Forwarded-Tls-Client-Cert-Info": "x"})
	for _, k := range []string{"X-Real-Ip", "X-Forwarded-Uri", "X-Forwarded-Method", "X-Forwarded-Tls-Client-Cert", "X-Forwarded-Tls-Client-Cert-Info"} {
		if got.Header.Get(k) != "" {
			t.Fatalf("%s kept: %v", k, got.Header)
		}
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
