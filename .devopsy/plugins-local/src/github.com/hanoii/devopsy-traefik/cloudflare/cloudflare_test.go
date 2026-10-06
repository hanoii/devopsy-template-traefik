package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealIP(t *testing.T) {
	var got *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { got = req })
	h, err := New(context.Background(), next, &Config{TrustedIPs: []string{"162.158.0.0/15"}}, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Through Cloudflare: the visitor becomes the client.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "162.158.154.69:44321"
	req.Header.Set("Cf-Connecting-Ip", "181.106.103.254")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.RemoteAddr != "181.106.103.254:44321" || got.Header.Get("X-Real-Ip") != "181.106.103.254" {
		t.Fatalf("through Cloudflare: %s, %v", got.RemoteAddr, got.Header)
	}

	// Directly, with a spoofed header: dropped, client unchanged.
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:5555"
	req.Header.Set("Cf-Connecting-Ip", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.RemoteAddr != "198.51.100.7:5555" || got.Header.Get("Cf-Connecting-Ip") != "" {
		t.Fatalf("direct: %s, %v", got.RemoteAddr, got.Header)
	}

	// From Cloudflare without a valid header: unchanged.
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "162.158.154.69:1"
	req.Header.Set("Cf-Connecting-Ip", "not-an-ip")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.RemoteAddr != "162.158.154.69:1" {
		t.Fatalf("invalid header: %s", got.RemoteAddr)
	}

	if _, err := New(context.Background(), next, &Config{}, "test"); err == nil {
		t.Fatal("empty trustedIPs: want an error")
	}
}
