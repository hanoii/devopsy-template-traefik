//go:build e2e

package tests

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// from runs curl in a container on network (the server host's), as a
// visitor or a proxy there would: its output.
func (h host) from(network string, args ...string) string {
	h.t.Helper()
	return h.sh("/", nil, `net=$1; shift; docker run --rm -q --network "$net" curlimages/curl:8.15.0 -sk --max-time 20 "$@"`,
		append([]string{network}, args...)...)
}

// header is the value whoami echoed for name, empty when it got none.
func header(echo, name string) string {
	m := regexp.MustCompile(`(?mi)^` + regexp.QuoteMeta(name) + `: (.*?)\r?$`).FindStringSubmatch(echo)
	if m == nil {
		return ""
	}
	return m[1]
}

// The tunnel entrypoint trusts its network's connectors: the visitor's
// IP from CF-Connecting-IP and its X-Forwarded-Proto; from any other
// network neither. A visitor's Forwarded never reaches a site, on the
// tunnel or on websecure.
func TestTunnelTrust(t *testing.T) {
	h := traefik(t)
	h.site("tun", "tun.example.com", []string{"routers.<name>.entrypoints=tunnel"}, "")
	h.site("direct", "direct.example.com", nil, "")
	visitor := []string{"-H", "Host: tun.example.com", "-H", "CF-Connecting-IP: 192.0.2.44", "-H", "X-Forwarded-Proto: https",
		"-H", "Forwarded: host=evil.example.com;proto=http", "http://traefik.tunnel:8081/"}

	echo := h.from("tunnel", visitor...)
	if header(echo, "X-Real-Ip") != "192.0.2.44" || header(echo, "X-Forwarded-Proto") != "https" || header(echo, "Forwarded") != "" {
		t.Errorf("through the tunnel:\n%s", echo)
	}
	visitor[len(visitor)-1] = "http://traefik:8081/"
	echo = h.from("traefik", visitor...)
	if header(echo, "X-Real-Ip") == "192.0.2.44" || header(echo, "X-Forwarded-Proto") != "http" || header(echo, "Forwarded") != "" {
		t.Errorf("from the traefik network:\n%s", echo)
	}
	echo = h.sh("/", nil, `curl -sk --resolve direct.example.com:443:127.0.0.1 -H 'Forwarded: host=evil.example.com;proto=http' https://direct.example.com/`)
	if header(echo, "Forwarded") != "" || !strings.Contains(echo, "Host: direct.example.com") {
		t.Errorf("on websecure:\n%s", echo)
	}
	// Sites name no entrypoint: web and websecure, never the tunnel's.
	var r struct{ EntryPoints []string }
	if err := json.Unmarshal([]byte(h.api("/api/http/routers/websecure-direct@docker")), &r); err != nil || strings.Join(r.EntryPoints, ",") != "websecure" {
		t.Errorf("direct's router: %v %v", err, r)
	}
	if out, err := h.shErr("/", nil, `curl -sf http://127.0.0.1:8080/api/http/routers/tunnel-direct@docker`); err == nil {
		t.Errorf("direct is on the tunnel: %s", out)
	}
}

// devopsy proxies add: requests from a proxy's ranges get the visitor
// from its header, and the forwarded headers as Traefik makes them for a
// direct visitor (a proxy naming a header of its own is not trusted for
// X-Forwarded-For: Traefik builds it from that visitor); from anywhere
// else the proxy's headers are dropped.
func TestProxies(t *testing.T) {
	h := traefik(t)
	h.site("proxied", "proxied.example.com", nil, "")
	subnet := strings.TrimSpace(h.sh("/", nil, `docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' traefik`))
	h.sh(proxy, nil, `devopsy proxies add edge X-Edge-Ip "$1"`, subnet)
	// Removed below; here too should the test end before.
	t.Cleanup(func() { _, _ = h.shErr(proxy, nil, "devopsy proxies | grep -q edge && devopsy proxies remove edge") })
	h.routed("proxied@docker")
	request := []string{"--connect-to", "proxied.example.com:443:traefik:443", "-H", "X-Edge-Ip: 192.0.2.55", "-H", "X-Forwarded-For: 192.0.2.66",
		"-H", "X-Forwarded-Host: evil.example.com", "-H", "X-Forwarded-Prefix: /evil", "-H", "X-Forwarded-Proto: https", "https://proxied.example.com/"}

	echo := h.from("traefik", request...)
	if header(echo, "X-Real-Ip") != "192.0.2.55" || header(echo, "X-Forwarded-For") != "192.0.2.55" ||
		header(echo, "X-Forwarded-Host") != "proxied.example.com" || header(echo, "X-Forwarded-Prefix") != "" {
		t.Errorf("from the proxy's range:\n%s", echo)
	}
	echo = h.from("tunnel", request...)
	if ip := header(echo, "X-Real-Ip"); ip == "192.0.2.55" || ip == "192.0.2.66" || strings.Contains(header(echo, "X-Forwarded-For"), "192.0.2.") ||
		header(echo, "X-Forwarded-Host") != "proxied.example.com" || header(echo, "X-Forwarded-Prefix") != "" {
		t.Errorf("from outside its range:\n%s", echo)
	}

	// static_env restarts Traefik only when its settings change, and a
	// middleware's file goes once the running Traefik no longer names it.
	started := func() string {
		return h.sh(proxy, nil, `docker inspect -f '{{.State.StartedAt}}' "$(devopsy ps -q traefik)"`)
	}
	before := started()
	if out := h.sh(proxy, nil, "devopsy t static_env"); strings.Contains(out, "restarting") || started() != before {
		t.Errorf("static_env restarted Traefik with nothing changed: %s", out)
	}
	h.sh(proxy, nil, "devopsy proxies remove edge")
	if started() == before {
		t.Error("removing the last proxy did not restart Traefik")
	}
	if out, err := h.shErr(proxy, nil, "cat .devopsy/mnt/dynamic/proxies.yaml"); err == nil {
		t.Errorf("proxies.yaml stayed:\n%s", out)
	}
	if env := h.sh(proxy, nil, "cat .devopsy/mnt/traefik.env"); strings.Contains(env, "proxies@file") {
		t.Errorf("traefik.env still names proxies:\n%s", env)
	}
}

// Who reaches what: the API answers the host only, the Docker socket's
// proxy only Traefik and only for what Traefik reads; every service runs
// read-only, without capabilities, as a user of its own.
func TestReach(t *testing.T) {
	h := traefik(t)
	if code := h.from("traefik", "-o", "/dev/null", "-w", "%{http_code}", "http://traefik:8080/api/rawdata"); code != "403" {
		t.Errorf("the API from the traefik network: %s", code)
	}
	if out, err := h.shErr("/", nil, `docker run --rm -q --network traefik curlimages/curl:8.15.0 -s --max-time 5 http://dockerproxy:2375/v1.47/version`); err == nil {
		t.Errorf("the socket's proxy from the traefik network: %s", out)
	}
	id := strings.TrimSpace(h.sh(proxy, nil, "devopsy ps -q traefik"))
	for path, ok := range map[string]bool{"/v1.47/version": true, "/v1.47/containers/json": true, "/v1.47/containers/" + id + "/json": true,
		"/v1.47/containers/" + id + "/logs?stdout=1": false, "/v1.47/containers/" + id + "/export": false, "/v1.47/images/json": false} {
		_, err := h.shErr(proxy, nil, `devopsy exec -T traefik wget -Y off -q -O /dev/null "http://dockerproxy:2375$1"`, path)
		if (err == nil) != ok {
			t.Errorf("the socket's proxy for Traefik, %s: answered %t", path, err == nil)
		}
	}
	for _, c := range strings.Fields(h.sh("/", nil, `docker ps -q --filter label=com.docker.compose.project=traefik`)) {
		var in []struct {
			Name       string
			Config     struct{ User string }
			HostConfig struct {
				ReadonlyRootfs bool
				CapDrop        []string
				SecurityOpt    []string
			}
		}
		if err := json.Unmarshal([]byte(h.sh("/", nil, `docker inspect "$1"`, c)), &in); err != nil {
			t.Fatal(err)
		}
		h := in[0].HostConfig
		if u := in[0].Config.User; !h.ReadonlyRootfs || strings.Join(h.CapDrop, ",") != "ALL" ||
			!strings.Contains(strings.Join(h.SecurityOpt, ","), "no-new-privileges") || u == "" || strings.HasPrefix(u, "0:") || u == "root" {
			t.Errorf("%s: user %q, %+v", in[0].Name, u, h)
		}
	}
}

// PROXY_TUNNEL_ONLY: a router naming no entrypoint is on the tunnel's
// only, never on web or websecure. A Traefik of its own on the scripts
// host, taken down after.
func TestTunnelOnly(t *testing.T) {
	h, dir := project(t, "tunnelonly", map[string]string{"PROXY_TUNNEL_ONLY": "true"})
	h.sh(dir, nil, "devopsy deploy")
	h.sh("/", nil, "docker run -d --name only --network traefik -l traefik.enable=true -l 'traefik.http.routers.only.rule=Host(`only.example.com`)' traefik/whoami:v1.11 >/dev/null")
	t.Cleanup(func() { _, _ = h.shErr("/", nil, "docker rm -f only") })
	out := h.sh("/", nil, `for i in $(seq 120); do curl -sf http://127.0.0.1:8080/api/http/routers/only@docker && exit; sleep 0.25; done; exit 1`)
	var r struct{ EntryPoints []string }
	if err := json.Unmarshal([]byte(out), &r); err != nil || strings.Join(r.EntryPoints, ",") != "tunnel" {
		t.Errorf("only's router: %v %s", err, out)
	}
	if code := h.sh("/", nil, `curl -sk -o /dev/null -w '%{http_code}' --resolve only.example.com:443:127.0.0.1 https://only.example.com/`); code != "404" {
		t.Errorf("on websecure: %s", code)
	}
	if echo := h.sh("/", nil, `docker run --rm -q --network tunnel curlimages/curl:8.15.0 -s -H 'Host: only.example.com' http://traefik.tunnel:8081/`); !strings.Contains(echo, "Host: only.example.com") {
		t.Errorf("through the tunnel:\n%s", echo)
	}
}

// lib/facts reads Traefik's API from inside its container, which gets the
// Docker client's proxy settings when it is created; busybox's wget
// ignores no_proxy, so a proxy there must change nothing (wget -Y off).
// A Traefik of its own, created behind a dead proxy, on the scripts host.
func TestFactsBehindAProxy(t *testing.T) {
	h, dir := project(t, "proxied", nil)
	h.sh("/", nil, `mkdir -p /tmp/proxied && printf '%s\n' '{"proxies":{"default":{"httpProxy":"http://127.0.0.1:9","httpsProxy":"http://127.0.0.1:9","noProxy":""}}}' >/tmp/proxied/config.json`)
	proxied := map[string]string{"DOCKER_CONFIG": "/tmp/proxied"}
	// Deployed first without it: the proxy reaches builds too (devopsy's
	// util image).
	h.sh(dir, nil, "devopsy deploy")
	h.sh(dir, proxied, "devopsy up -d --wait --force-recreate traefik")
	if env := h.sh(dir, nil, "devopsy exec -T traefik env"); !strings.Contains(env, "HTTP_PROXY=http://127.0.0.1:9") {
		t.Fatalf("Traefik not behind the proxy:\n%s", env)
	}
	h.sh("/", nil, `for i in $(seq 120); do curl -sf -o /dev/null http://127.0.0.1:8080/api/http/routers/devopsy-api@file && exit; sleep 0.25; done; exit 1`)
	if out := h.sh(dir, proxied, `devopsy t '"$dir/lib/facts" --all' 2>&1`); !strings.Contains(out, `"hosts"`) {
		t.Fatalf("facts behind a dead proxy:\n%s", out)
	}
}

// A Traefik with acme-dns and a base domain, of its own on the scripts
// host: the wildcard's router answers its one reserved name, nothing
// else; acme-dns answers its zone; every ACME store is 0600, and Traefik
// skips one that is not; deploy stops acme-dns once its profile is off.
func TestAcmeDNSAndWildcard(t *testing.T) {
	h, dir := project(t, "acmedns", map[string]string{"COMPOSE_PROFILES": "acmedns", "PROXY_ACMEDNS_DOMAIN": "acme.example.com",
		"PROXY_ACMEDNS_PORT": "15353", "PROXY_BASE_DOMAIN": "vm1.example.com"})
	if out := h.sh(dir, nil, "devopsy deploy"); !strings.Contains(out, "public wildcard *.vm1.example.com through le-dns01-selfhosted") {
		t.Errorf("deploy:\n%s", out)
	}
	h.sh("/", nil, `for i in $(seq 120); do curl -sf -o /dev/null http://127.0.0.1:8080/api/http/routers/devopsy-public-wildcard@file && exit; sleep 0.25; done; exit 1`)
	for host, want := range map[string]string{"devopsy-wildcard.vm1.example.com": "418", "app.vm1.example.com": "404"} {
		if code := h.sh("/", nil, `curl -sk -o /dev/null -w '%{http_code}' --resolve "$1:443:127.0.0.1" "https://$1/"`, host); code != want {
			t.Errorf("%s: %s, want %s", host, code, want)
		}
	}
	ip := strings.TrimSpace(h.sh(dir, nil, "sed -n 's/^PROXY_ACMEDNS_IP=//p' .devopsy/.env"))
	if soa := h.sh("/", nil, `dig +short -p 15353 "@$1" acme.example.com SOA`, ip); !strings.Contains(soa, "ns-acme.example.com.") {
		t.Errorf("acme-dns at %s: %q", ip, soa)
	}
	if modes := h.sh(dir, nil, `stat -c '%a %n' .devopsy/mnt/letsencrypt/acme-*.json .devopsy/mnt/acmedns/acme-dns.db`); strings.Count(modes, "600 ") != 4 {
		t.Errorf("the stores' modes:\n%s", modes)
	}
	// One too open: Traefik says so and skips that resolver, healthy.
	h.sh(dir, nil, `devopsy t 'in_traefik "chmod 644 /letsencrypt/acme-le-http01.json"' && id=$(devopsy ps -q traefik) && docker restart "$id" >/dev/null &&
		for i in $(seq 120); do docker logs "$id" 2>&1 | grep -q 'too open' && exit; sleep 0.25; done; exit 1`)
	h.sh(dir, nil, `devopsy t 'in_traefik "chmod 600 /letsencrypt/acme-le-http01.json"'`)

	h.sh(dir, nil, "sed -i '/^COMPOSE_PROFILES=/d' .devopsy/.env && devopsy deploy")
	if ps := h.sh(dir, nil, "devopsy ps --format '{{.Service}}'"); strings.Contains(ps, "acmedns") {
		t.Errorf("acme-dns still running:\n%s", ps)
	}
}
