// Package proxies is a Traefik middleware plugin (run by Traefik's Yaegi
// interpreter: standard library only) for sites behind HTTP proxies or CDNs.
//
// Each configured proxy has its ranges and the header it passes the visitor
// in. For a request from a proxy's ranges, the visitor's IP becomes the
// request's remote address, so Traefik builds X-Forwarded-For from it, and
// X-Real-Ip is set to it: applications that trust only Traefik see the real
// client. Any other request loses the proxies' own headers, so they cannot be
// spoofed by reaching the server directly. Traefik already strips
// X-Forwarded-For and X-Real-Ip from untrusted connections, so proxies using
// those must also be in the entrypoint's forwardedHeaders.trustedIPs.
package proxies

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Proxy is one trusted proxy.
type Proxy struct {
	Name string `json:"name,omitempty"`
	// Header carries the visitor's IP, like CF-Connecting-IP. For
	// X-Forwarded-For, the rightmost address that is not a trusted proxy.
	Header     string   `json:"header,omitempty"`
	TrustedIPs []string `json:"trustedIPs,omitempty"`
}

// Config is the middleware configuration.
type Config struct {
	Proxies []Proxy `json:"proxies,omitempty"`
}

// CreateConfig returns the default configuration.
func CreateConfig() *Config {
	return &Config{}
}

type proxy struct {
	name   string
	header string
	nets   []*net.IPNet
}

// Proxies is the middleware.
type Proxies struct {
	next    http.Handler
	proxies []proxy
	// headers are the proxies' own headers (not X-Forwarded-For or
	// X-Real-Ip, which Traefik handles), removed from other requests.
	headers []string
}

// New builds the middleware.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if len(config.Proxies) == 0 {
		return nil, fmt.Errorf("%s: no proxies", name)
	}
	r := &Proxies{next: next}
	seen := map[string]bool{}
	for _, p := range config.Proxies {
		h := http.CanonicalHeaderKey(strings.TrimSpace(p.Header))
		if h == "" || len(p.TrustedIPs) == 0 {
			return nil, fmt.Errorf("%s: proxy %q needs a header and trustedIPs", name, p.Name)
		}
		px := proxy{name: p.Name, header: h}
		for _, cidr := range p.TrustedIPs {
			_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
			if err != nil {
				return nil, fmt.Errorf("%s: proxy %q: %w", name, p.Name, err)
			}
			px.nets = append(px.nets, n)
		}
		r.proxies = append(r.proxies, px)
		if h != "X-Forwarded-For" && h != "X-Real-Ip" && !seen[h] {
			seen[h] = true
			r.headers = append(r.headers, h)
		}
	}
	return r, nil
}

// proxyFor returns the proxy whose ranges contain ip.
func (r *Proxies) proxyFor(ip net.IP) *proxy {
	for i := range r.proxies {
		for _, n := range r.proxies[i].nets {
			if n.Contains(ip) {
				return &r.proxies[i]
			}
		}
	}
	return nil
}

// visitor reads the visitor's IP from p's header.
func (r *Proxies) visitor(p *proxy, req *http.Request) net.IP {
	value := req.Header.Get(p.header)
	if p.header != "X-Forwarded-For" {
		return net.ParseIP(strings.TrimSpace(value))
	}
	// Rightmost address that is not one of our proxies: what the closest
	// trusted proxy saw connecting to it.
	parts := strings.Split(value, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			return nil
		}
		if r.proxyFor(ip) == nil {
			return ip
		}
	}
	return nil
}

func (r *Proxies) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	host, port, err := net.SplitHostPort(req.RemoteAddr)
	peer := net.ParseIP(host)
	var p *proxy
	if err == nil && peer != nil {
		p = r.proxyFor(peer)
	}
	if p != nil {
		if ip := r.visitor(p, req); ip != nil {
			req.RemoteAddr = net.JoinHostPort(ip.String(), port)
			req.Header.Set("X-Real-Ip", ip.String())
			// Traefik appends the (new) remote address: X-Forwarded-For ends up
			// as just the visitor.
			req.Header.Del("X-Forwarded-For")
		}
	} else {
		for _, h := range r.headers {
			req.Header.Del(h)
		}
	}
	r.next.ServeHTTP(rw, req)
}
