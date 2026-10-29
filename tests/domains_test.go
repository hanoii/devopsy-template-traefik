//go:build e2e

package tests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The server's public IP in every case (settings' PROXY_PUBLIC_IP).
const publicIP = "203.0.113.10"

// check is what the check script found for a name: its A record, the
// challenge's CNAME, the certificate, the HTTPS answer.
func check(name string, a string, more map[string]any) map[string]any {
	c := map[string]any{"name": name, "a": map[string]any{"Answer": []any{}}, "challenge": nil, "issuer": "", "expire": "",
		"valid": false, "status": 200, "cf": false, "cferror": "", "held": false}
	if a != "" {
		c["a"] = map[string]any{"Answer": []any{map[string]any{"type": 1, "data": a}}}
	}
	for k, v := range more {
		c[k] = v
	}
	return c
}

// domains' next step for every case it tells apart, from facts and checks
// as lib/facts and the check script write them.
func TestDomainsReport(t *testing.T) {
	h, dir := project(t, "domains", nil)
	routed := func(method string, more map[string]any) map[string]any {
		f := map[string]any{"routed": true, "method": method, "resolver": "le-http01"}
		if method == "tunnel" {
			delete(f, "resolver")
		}
		for k, v := range more {
			f[k] = v
		}
		return f
	}
	cf := func(more map[string]any) map[string]any {
		m := map[string]any{"cf": true}
		for k, v := range more {
			m[k] = v
		}
		return m
	}
	const elsewhere = "198.51.100.7"
	cases := []struct {
		name      string
		fact      map[string]any
		check     map[string]any // nil: not checked
		connected bool
		want      string
	}{
		{"not-routed", map[string]any{"routed": false}, check("", publicIP, nil), false, "Traefik does not route it"},
		{"tunnel-tls", routed("tunnel", map[string]any{"tls": true}), check("", elsewhere, cf(nil)), false, "its routers have TLS"},
		{"tunnel-1033-connected", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"cferror": "1033"})), true, "no connector of the tunnel its record names (1033)"},
		{"tunnel-1033", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"cferror": "1033"})), false, "Cloudflare finds no connector of its tunnel (1033): see the connectors below"},
		{"tunnel-1016", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"cferror": "1016"})), false, "Cloudflare error 1016"},
		{"tunnel-no-dns", routed("tunnel", nil), check("", "", cf(nil)), false, "does not resolve: in the tunnel's Cloudflare account"},
		{"tunnel-no-cf", routed("tunnel", nil), check("", elsewhere, nil), false, "DNS does not go through Cloudflare"},
		{"tunnel-no-https", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"status": 0})), false, "its edge has no certificate for this name. Its free one only covers names one level below the zone (*.example.com), not deeper ones like this: a name one level down"},
		{"tunnel-held", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"status": 307, "held": true})), false, "the tunnel reaches Traefik, but the site's app is down"},
		{"tunnel-502", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"status": 502})), false, "its connector cannot reach Traefik (502)"},
		{"tunnel-404", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"status": 404})), false, "404 through the tunnel"},
		{"tunnel-503", routed("tunnel", nil), check("", elsewhere, cf(map[string]any{"status": 503})), false, "HTTP 503 through the tunnel"},
		{"tunnel-live", routed("tunnel", nil), check("", elsewhere, cf(nil)), false, "live"},
		{"direct-live", routed("http", nil), check("", publicIP, map[string]any{"valid": true}), false, "live"},
		{"cdn-no-https", routed("http", nil), check("", elsewhere, cf(map[string]any{"status": 0})), false, "Set the DNS record to DNS only (grey cloud)"},
		{"cdn-522", routed("http", nil), check("", elsewhere, cf(map[string]any{"status": 522})), false, "Cloudflare cannot reach the server (522): check its DNS origin is " + publicIP},
		{"cdn-525", routed("http", nil), check("", elsewhere, cf(map[string]any{"status": 525})), false, "TLS handshake with the server fails (525)"},
		{"cdn-526", routed("http", nil), check("", elsewhere, cf(map[string]any{"status": 526})), false, "rejects the server's certificate (526)"},
		{"cdn-524", routed("http", nil), check("", elsewhere, cf(map[string]any{"status": 524})), false, "Cloudflare reports an origin error (524)"},
		{"cdn-held", routed("http", nil), check("", elsewhere, cf(map[string]any{"status": 307, "held": true})), false, "Cloudflare reaches Traefik, but the site's app is down"},
		{"cdn-no-certificate", routed("http", nil), check("", elsewhere, cf(nil)), false, "served through Cloudflare (its SSL mode is not Full (strict))"},
		{"cdn-live", routed("http", nil), check("", elsewhere, cf(map[string]any{"valid": true})), false, "live"},
		{"ready", routed("http", nil), check("", elsewhere, map[string]any{"valid": true}), false, "certificate ready: point its DNS at " + publicIP},
		{"cname-unknown", routed("dns-cname", map[string]any{"resolver": "le-dns01-selfhosted"}), check("", elsewhere, nil), false, "its challenge record is not known yet"},
		{"cname-missing", routed("dns-cname", map[string]any{"resolver": "le-dns01-selfhosted", "record": map[string]any{"target": "x1.acme.example.com."}}),
			check("", elsewhere, nil), false, "_acme-challenge.cname-missing.example.com. CNAME x1.acme.example.com."},
		{"cname-in-place", routed("dns-cname", map[string]any{"resolver": "le-dns01-selfhosted", "record": map[string]any{"target": "x1.acme.example.com."}}),
			check("", elsewhere, map[string]any{"challenge": map[string]any{"Answer": []any{map[string]any{"type": 5, "data": "X1.acme.example.com."}}}}), false, "challenge CNAME in place"},
		{"dns-api", routed("dns-api", map[string]any{"resolver": "le-dns01-cloudflare"}), check("", elsewhere, nil), false, "the Cloudflare token must cover this domain's zone"},
		{"http-here", routed("http", nil), check("", publicIP, nil), false, "DNS points here: run with --retry"},
		{"http-elsewhere", routed("http", nil), check("", elsewhere, nil), false, "point its DNS at " + publicIP + " (HTTP-01 needs it)"},
		{"unknown-resolver", routed("", map[string]any{"resolver": "other"}), check("", elsewhere, nil), false, "Traefik does not say how it issues this certificate (resolver other)"},
		{"not-checked", routed("http", nil), nil, false, "not a host name, or not checked"},
	}
	for _, connected := range []bool{false, true} {
		var hosts, checks []any
		var want []string
		for _, c := range cases {
			if c.connected != connected {
				continue
			}
			host := c.name + ".example.com"
			f := map[string]any{"host": host}
			for k, v := range c.fact {
				f[k] = v
			}
			hosts = append(hosts, f)
			if c.check != nil {
				c.check["name"] = host
				checks = append(checks, c.check)
			}
			want = append(want, c.name)
		}
		facts, _ := json.Marshal(map[string]any{"version": 1, "ip": publicIP, "retries": []any{}, "hosts": hosts})
		var lines []string
		for _, c := range checks {
			b, _ := json.Marshal(c)
			lines = append(lines, string(b))
		}
		out := h.sh(dir, nil, fmt.Sprintf(`devopsy t 'printf "%%s\n%%s\n" "$1" "$2" | jq -s --arg ip %s --argjson connected %t "$(cat "$dir/lib/domains/report.jq")"' "$1" "$2"`,
			publicIP, connected), string(facts), strings.Join(lines, "\n"))
		var reports []struct{ Host, Next string }
		if err := json.Unmarshal([]byte(out), &reports); err != nil {
			t.Fatalf("connected=%t: %v\n%s", connected, err, out)
		}
		next := map[string]string{}
		for _, r := range reports {
			next[strings.TrimSuffix(r.Host, ".example.com")] = r.Next
		}
		for _, c := range cases {
			if c.connected != connected {
				continue
			}
			if got := next[c.name]; !strings.Contains(got, c.want) || (c.want == "live") != (got == "live") {
				t.Errorf("%s: next %q, want %q", c.name, got, c.want)
			}
		}
	}
}

// domains' text: a block per host, the next step only when not live.
func TestDomainsText(t *testing.T) {
	h, dir := project(t, "domainstext", nil)
	reports := `[{"host":"app.example.com","live":true,"tunnel":true,"ips":["198.51.100.7"],"on_server":false,"cf":true,"cdn":false,"status":200,"method":"tunnel","valid":false,"have":"","want":"","issuer":"","next":"live"},
		{"host":"shop.example.com","live":false,"tunnel":false,"resolver":"le-dns01-selfhosted","ips":[],"on_server":false,"cf":false,"cdn":false,"status":0,"method":"dns-cname","valid":false,"have":"","want":"x1.acme.example.com","issuer":"","next":"create it"}]`
	got := h.sh(dir, nil, `devopsy t 'printf "%s\n" "$1" | jq -r "$(cat "$dir/lib/domains/text.jq")"' "$1"`, reports)
	want := `app.example.com  [live]
  route        Cloudflare tunnel
  dns          198.51.100.7 (Cloudflare proxy, HTTP 200 through the tunnel)
  certificate  at Cloudflare's edge (Cloudflare tunnel)

shop.example.com  [pending]
  resolver     le-dns01-selfhosted
  dns          does not resolve
  challenge    missing
  certificate  none (no TLS answer)
  next         create it

`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
