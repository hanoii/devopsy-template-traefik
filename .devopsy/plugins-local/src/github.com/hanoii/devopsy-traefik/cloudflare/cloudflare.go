// Package cloudflare is a Traefik middleware plugin (run by Traefik's Yaegi
// interpreter: standard library only) for sites behind Cloudflare's proxy.
//
// For a request from one of TrustedIPs (Cloudflare's ranges), it makes the
// visitor's IP, from the CF-Connecting-IP header Cloudflare always sets and
// overwrites, the request's remote address. Traefik then builds
// X-Forwarded-For from it, and X-Real-Ip is set to it, so applications that
// trust only Traefik see the real client. Requests from anywhere else lose
// any CF-Connecting-IP header, so it cannot be spoofed by reaching the server
// directly.
package cloudflare

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Config is the middleware configuration.
type Config struct {
	// TrustedIPs are Cloudflare's ranges, in CIDR notation.
	TrustedIPs []string `json:"trustedIPs,omitempty"`
}

// CreateConfig returns the default configuration.
func CreateConfig() *Config {
	return &Config{}
}

// RealIP is the middleware.
type RealIP struct {
	next    http.Handler
	trusted []*net.IPNet
}

// New builds the middleware.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if len(config.TrustedIPs) == 0 {
		return nil, fmt.Errorf("%s: trustedIPs is empty", name)
	}
	r := &RealIP{next: next}
	for _, cidr := range config.TrustedIPs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("%s: trustedIPs: %w", name, err)
		}
		r.trusted = append(r.trusted, n)
	}
	return r, nil
}

func (r *RealIP) isTrusted(ip net.IP) bool {
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (r *RealIP) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	host, port, err := net.SplitHostPort(req.RemoteAddr)
	peer := net.ParseIP(host)
	if err == nil && peer != nil && r.isTrusted(peer) {
		visitor := strings.TrimSpace(req.Header.Get("Cf-Connecting-Ip"))
		if ip := net.ParseIP(visitor); ip != nil {
			req.RemoteAddr = net.JoinHostPort(ip.String(), port)
			req.Header.Set("X-Real-Ip", ip.String())
		}
	} else {
		req.Header.Del("Cf-Connecting-Ip")
	}
	r.next.ServeHTTP(rw, req)
}
