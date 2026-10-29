//go:build e2e

package tests

import (
	"strings"
	"testing"
)

// target is the deploy user's own host as a server: what a release to it
// goes through is a release to any server (an upload over SSH, the
// activation, shared/, the run step).
const target = "@devopsy@localhost:prod"

// The template released as to a server, on a scripts host: what only a
// release does. check refuses a setting before the switch, the previous
// release staying current; mnt's entries go to Traefik's user on every
// release; the proxy role is the host's one; a site's import gets the
// base domain Traefik exports; --reload applies a changed setting.
func TestRelease(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	t.Cleanup(func() { _, _ = h.shErr(dir, nil, "devopsy "+target+" --destroy --yes") })
	vars := map[string]string{"PROXY_LE_ACME_EMAIL": "admin@example.com", "PROXY_PUBLIC_IP": "203.0.113.10", "PROXY_BASE_DOMAIN": "vm1.example.com"}
	h.sh(dir, vars, "devopsy "+target+" --vars set PROXY_LE_ACME_EMAIL PROXY_PUBLIC_IP PROXY_BASE_DOMAIN")
	h.sh(dir, nil, "devopsy "+target+" --release --yes")
	base := "/home/devopsy/traefik/prod"
	first := strings.TrimSpace(h.sh(base, nil, "basename $(readlink -f current)/.."))

	// mnt's entries are Traefik's user's; a file put there by hand (as
	// root) is given to it too, by the next activation (the --reload
	// below).
	if owners := h.sh(base, nil, `stat -c '%u:%g %n' shared/mnt/letsencrypt shared/mnt/acmedns`); strings.Count(owners, "10001:10001 ") != 2 {
		t.Errorf("mnt's owners after a release:\n%s", owners)
	}
	if out, err := docker("exec", h.name, "touch", base+"/shared/mnt/letsencrypt/by-hand.json"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	// A setting check refuses: the release fails before its switch.
	h.sh(dir, map[string]string{"PROXY_CERTRESOLVER": "letsencrypt"}, "devopsy "+target+" --vars set PROXY_CERTRESOLVER")
	if out, err := h.shErr(dir, nil, "devopsy "+target+" --release --yes"); err == nil || !strings.Contains(out, "PROXY_CERTRESOLVER must be") {
		t.Errorf("a release with a bad resolver: %v\n%s", err, out)
	}
	if now := strings.TrimSpace(h.sh(base, nil, "basename $(readlink -f current)/..")); now != first {
		t.Errorf("current moved to %s from %s", now, first)
	}
	if ps := h.sh(dir, nil, "devopsy "+target+" ps --format '{{.Service}} {{.State}}'"); !strings.Contains(ps, "traefik running") {
		t.Errorf("Traefik not running after the refusal:\n%s", ps)
	}
	h.sh(dir, nil, "devopsy "+target+" --vars unset PROXY_CERTRESOLVER")

	// A second proxy on the host is refused.
	other := project2(t, h, "traefik2")
	if out, err := h.shErr(other, vars, "devopsy "+target+" --vars set PROXY_LE_ACME_EMAIL && devopsy "+target+" --release --yes"); err == nil || !strings.Contains(out, "role proxy is held by traefik-prod") {
		t.Errorf("a second proxy: %v\n%s", err, out)
	}

	// A site importing the base domain gets Traefik's at its release (its
	// own URL the site's env capability builds from it).
	site := "/home/devopsy/site"
	h.sh("/", nil, `mkdir -p "$1/.devopsy" && cd "$1/.devopsy" && printf '%s\n' 'project: site' 'defaults:' '  mode: image' '  release: up -d --wait' 'environments:' '  prod: {}' >config.yaml &&
		printf '%s\n' 'services:' '  web:' '    image: traefik/whoami:v1.11' '    labels:' '      - devopsy.import.PROXY_BASE_DOMAIN=proxy/BASE_DOMAIN' '    networks: [traefik]' 'networks:' '  traefik:' '    external: true' >compose.yaml`, site)
	t.Cleanup(func() { _, _ = h.shErr(site, nil, "devopsy "+target+" --destroy --yes") })
	h.sh(site, nil, "devopsy "+target+" --release --yes")
	if env := h.sh("/home/devopsy/site/prod", nil, "cat current/../devopsy-target.env"); !strings.Contains(env, "PROXY_BASE_DOMAIN='vm1.example.com'") {
		t.Errorf("the site's imports:\n%s", env)
	}

	// --reload applies a changed setting (reload: deploy).
	h.sh(dir, map[string]string{"PROXY_READ_TIMEOUT": "7s"}, "devopsy "+target+" --vars set PROXY_READ_TIMEOUT && devopsy "+target+" --reload")
	if env := h.sh(dir, nil, "devopsy "+target+" exec -T traefik env"); !strings.Contains(env, "READTIMEOUT=7s") {
		t.Errorf("Traefik's environment after --reload:\n%s", env)
	}
	if owner := h.sh(base, nil, `stat -c '%u:%g' shared/mnt/letsencrypt/by-hand.json`); strings.TrimSpace(owner) != "10001:10001" {
		t.Errorf("a file put in by hand: %s", owner)
	}
}

// project2 is another copy of the template on h, beside a test's own.
func project2(t *testing.T, h host, name string) string {
	t.Helper()
	dir, err := newProject(h.name, name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
