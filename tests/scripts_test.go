//go:build e2e

package tests

import (
	"strings"
	"testing"
)

// check refuses what Traefik would take without a word, the release's
// prepare step: each refusal, and what passes.
func TestCheckSettings(t *testing.T) {
	dir := project(t, "check", nil)
	for _, c := range []struct {
		env  map[string]string
		want string // empty: passes
	}{
		{nil, ""},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-selfhosted", "COMPOSE_PROFILES": "acmedns", "PROXY_ACMEDNS_DOMAIN": "acme.example.com"}, ""},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-cloudflare", "PROXY_CLOUDFLARE_DNS_API_TOKEN": "x"}, ""},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "none", "PROXY_TUNNEL_ONLY": "true", "PROXY_BASE_DOMAIN": "vm1.example.com"}, ""},
		{map[string]string{"PROXY_TUNNEL_ONLY": "false"}, ""},
		{map[string]string{"PROXY_CERTRESOLVER": "letsencrypt"}, "PROXY_CERTRESOLVER must be"},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "le-http01"}, "PROXY_WILDCARD_CERTRESOLVER must be"},
		{map[string]string{"PROXY_TUNNEL_ONLY": "yes"}, "PROXY_TUNNEL_ONLY must be"},
		{map[string]string{"COMPOSE_PROFILES": "acmedns"}, "set PROXY_ACMEDNS_DOMAIN"},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-selfhosted"}, "PROXY_CERTRESOLVER is le-dns01-selfhosted: add acmedns"},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "le-dns01-selfhosted"}, "PROXY_WILDCARD_CERTRESOLVER is le-dns01-selfhosted: add acmedns"},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-cloudflare"}, "PROXY_CERTRESOLVER is le-dns01-cloudflare: set PROXY_CLOUDFLARE_DNS_API_TOKEN"},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "le-dns01-cloudflare"}, "PROXY_WILDCARD_CERTRESOLVER is le-dns01-cloudflare: set"},
		{map[string]string{"PROXY_BASE_DOMAIN": ".example.com"}, "PROXY_BASE_DOMAIN is not a domain name"},
		{map[string]string{"PROXY_BASE_DOMAIN": "example.com."}, "PROXY_BASE_DOMAIN is not a domain name"},
		{map[string]string{"PROXY_BASE_DOMAIN": "vm1..example.com"}, "PROXY_BASE_DOMAIN is not a domain name"},
		{map[string]string{"PROXY_BASE_DOMAIN": "VM1.example.com"}, "PROXY_BASE_DOMAIN is not a domain name"},
	} {
		out, err := shErr(dir, c.env, "devopsy check")
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%v: refused: %s", c.env, out)
		case c.want != "" && err == nil:
			t.Errorf("%v: passed, want %q: %s", c.env, c.want, out)
		case c.want != "" && !strings.Contains(out, c.want):
			t.Errorf("%v: want %q in: %s", c.env, c.want, out)
		}
	}
}

// env_set writes through .env, a link to shared/.env on a server, and
// replaces a key's line in place.
func TestEnvSet(t *testing.T) {
	dir := project(t, "envset", nil)
	sh(t, dir, nil, `mv .devopsy/.env shared.env && ln -s ../shared.env .devopsy/.env`)
	sh(t, dir, nil, `devopsy t 'env_set PROXY_DOCKER_GID 990; env_set PROXY_DOCKER_GID 991; env_set PROXY_ACMEDNS_IP 192.0.2.1'`)
	if out := sh(t, dir, nil, `test -L .devopsy/.env && cat shared.env`); out != "PROXY_LE_ACME_EMAIL=admin@example.com\nPROXY_PUBLIC_IP=203.0.113.10\nPROXY_DOCKER_GID=991\nPROXY_ACMEDNS_IP=192.0.2.1\n" {
		t.Fatalf("shared.env:\n%s", out)
	}
}

// write_if_changed says whether it wrote.
func TestWriteIfChanged(t *testing.T) {
	dir := project(t, "write", nil)
	out := sh(t, dir, nil, `devopsy t 'f=$dir/mnt/x; mkdir -p "$dir/mnt"
		write_if_changed "$f" a && echo wrote; write_if_changed "$f" a || echo same; write_if_changed "$f" b && echo wrote; cat "$f"'`)
	if out != "wrote\nsame\nwrote\nb" {
		t.Fatalf("got %q", out)
	}
}
