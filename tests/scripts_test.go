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

// static_env writes Traefik's static settings from what is enabled: the
// entrypoints' middlewares and trust, the plugins, the default
// entrypoints. Without its restart (--no-restart), so nothing starts.
func TestStaticEnv(t *testing.T) {
	dir := project(t, "staticenv", nil)
	t.Cleanup(func() { _, _ = shErr(dir, nil, "docker network rm tunnel") })
	const head = "# Written by 'devopsy deploy' and 'devopsy proxies': Traefik's static settings.\n"
	const hold = "TRAEFIK_EXPERIMENTAL_LOCALPLUGINS_HOLD_MODULENAME=github.com/hanoii/devopsy-traefik/hold\n"
	const proxies = "TRAEFIK_EXPERIMENTAL_LOCALPLUGINS_PROXIES_MODULENAME=github.com/hanoii/devopsy-traefik/proxies\n"
	const both = "TRAEFIK_ENTRYPOINTS_WEB_ASDEFAULT=true\nTRAEFIK_ENTRYPOINTS_WEBSECURE_ASDEFAULT=true\n"
	for _, c := range []struct {
		name  string
		setup string
		env   map[string]string
		want  string
	}{
		{"nothing", "", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=forwarded@file\n" + hold + both},
		{"tunnel only", "", map[string]string{"PROXY_TUNNEL_ONLY": "true"}, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=forwarded@file\n" + hold +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_ASDEFAULT=true\n"},
		{"proxies", `mkdir -p .devopsy/mnt/proxies && echo 'x' >.devopsy/mnt/dynamic/proxies.yaml &&
			echo 'cloudflare' >.devopsy/mnt/proxies/proxies.conf && echo '198.51.100.0/24' >.devopsy/mnt/proxies/forwarded-ips`, nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_FORWARDEDHEADERS_TRUSTEDIPS=198.51.100.0/24\n" +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file,proxies@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=forwarded@file\n" + proxies + hold + both},
		{"proxies commented out", `mkdir -p .devopsy/mnt/proxies && echo 'x' >.devopsy/mnt/dynamic/proxies.yaml &&
			printf '# cloudflare\n\n' >.devopsy/mnt/proxies/proxies.conf`, nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=forwarded@file\n" + hold + both},
		{"tunnel", "docker network create --subnet 10.99.0.0/24 tunnel >/dev/null", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=10.99.0.0/24\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=forwarded@file,tunnel@file\n" + proxies + hold + both},
		// Made again on another subnet: the trust follows it.
		{"tunnel again", "docker network rm tunnel >/dev/null && docker network create --subnet 10.98.0.0/24 tunnel >/dev/null", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=10.98.0.0/24\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=forwarded@file,tunnel@file\n" + proxies + hold + both},
	} {
		sh(t, dir, nil, `rm -rf .devopsy/mnt && mkdir -p .devopsy/mnt/dynamic`)
		if c.setup != "" {
			sh(t, dir, nil, c.setup)
		}
		sh(t, dir, c.env, `devopsy t 'static_env --no-restart'`)
		if got := sh(t, dir, nil, "cat .devopsy/mnt/traefik.env"); got != c.want {
			t.Errorf("%s: traefik.env:\n%s\nwant:\n%s", c.name, got, c.want)
		}
		if got := sh(t, dir, nil, "cat .devopsy/mnt/dynamic/forwarded.yaml"); !strings.Contains(got, "Forwarded: \"\"") {
			t.Errorf("%s: forwarded.yaml:\n%s", c.name, got)
		}
		tunnel, _ := shErr(dir, nil, "cat .devopsy/mnt/dynamic/tunnel.yaml")
		if want := strings.HasPrefix(c.name, "tunnel") && c.name != "tunnel only"; want != strings.Contains(tunnel, "CF-Connecting-IP") {
			t.Errorf("%s: tunnel.yaml:\n%s", c.name, tunnel)
		}
	}

	// Docker failing (not a missing network) fails it, and the trust it had
	// stays: never a tunnel entrypoint trusting nothing, its visitors' IPs
	// lost.
	before := sh(t, dir, nil, "cat .devopsy/mnt/traefik.env")
	out, err := shErr(dir, nil, `devopsy t 'export DOCKER_HOST=unix:///nowhere.sock; static_env --no-restart'`)
	if err == nil || !strings.Contains(out, "could not read the tunnel network") {
		t.Errorf("Docker failing: %v: %s", err, out)
	}
	if after := sh(t, dir, nil, "cat .devopsy/mnt/traefik.env"); after != before {
		t.Errorf("Docker failing changed traefik.env:\n%s", after)
	}
}
