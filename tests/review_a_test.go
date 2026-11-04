//go:build e2e

package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// joinAndLeave runs a container on a compose-labeled network of its own until Traefik
// joins it, and removes it until Traefik leaves: a project's down, as attach sees it.
func (h host) joinAndLeave(project string) {
	h.t.Helper()
	h.sh("/", nil, `docker network inspect "$1_proxy" >/dev/null 2>&1 || docker network create --label "com.docker.compose.project=$1" "$1_proxy" >/dev/null
		docker run -d --name "$1" --network "$1_proxy" -l "traefik.docker.network=$1_proxy" -l "com.docker.compose.project=$1" busybox:1.37 sleep 100000 >/dev/null`, project)
	h.waitFor("Traefik on "+project, func() bool { return joined(h.proxyNetworks(), project+"_proxy") })
	h.sh("/", nil, `docker rm -f "$1" >/dev/null`, project)
	h.waitFor("Traefik off "+project, func() bool { return !joined(h.proxyNetworks(), project+"_proxy") })
}

func (h host) networkExists(name string) bool {
	h.t.Helper()
	_, err := h.shErr("/", nil, `docker network inspect "$1" >/dev/null 2>&1`, name)
	return err == nil
}

// BUG: the 10 seconds run from the first time Traefik left a network, not the last:
// sweep keeps one entry per leave, and the oldest removes the network while the
// newest still waits. A down, an up and a second down inside the 10 seconds give the
// next up less than the wait that protects it, down to none.
func TestReviewAWaitRunsFromTheLastLeave(t *testing.T) {
	h := traefik(t)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f twice; docker network rm twice_proxy`) })
	h.joinAndLeave("twice")
	time.Sleep(6 * time.Second)
	h.joinAndLeave("twice")
	last := time.Now()
	// No event says the wait is over, hence the sleep.
	time.Sleep(7 * time.Second)
	if !h.networkExists("twice_proxy") {
		t.Errorf("removed %s after Traefik last left it, wanted 10 s", time.Since(last).Round(time.Second))
	}
	h.waitFor("the network removed in the end", func() bool { return !h.networkExists("twice_proxy") })
}

// BUG: what waits to be removed is only in attach's memory: attach restarting inside
// the 10 seconds (its stream ending with netproxy, a release changing lib/attach)
// leaves the network for good, since Traefik is off it and nothing looks at it again.
// Each costs an address pool's subnet.
func TestReviewARemovalSurvivesRestart(t *testing.T) {
	h, _ := ownTraefik(t)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f lost; docker network rm lost_proxy`) })
	h.joinAndLeave("lost")
	h.sh("/", nil, "docker restart -t 1 traefik-netproxy-1 >/dev/null")
	h.waitFor("attach healthy again", func() bool {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{.State.Health.Status}}' traefik-attach-1`)) == "healthy"
	})
	// Well past the 10 seconds, with a pass after them. No event says the wait is over.
	time.Sleep(12 * time.Second)
	h.passed()
	if h.networkExists("lost_proxy") {
		t.Errorf("the network Traefik left is still there after attach restarted")
	}
}

// BUG: a connect that fails is never tried again: only a failed read retries a pass,
// so a site whose connect failed once hangs and answers 504 until any other container
// on the host happens to make an event. On a fake docker, since a real one fails on
// no command: the connect fails until the pass the first second's catch-up causes is
// over, and no event comes after it.
func TestReviewAFailedConnectIsRetried(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	fake := `mkdir /fake && cat >/fake/docker <<'EOF'
#!/bin/sh
case "$1 $2" in
  "ps -aq") case "$*" in *role=proxy*) echo proxy ;; *) echo site ;; esac ;;
  "network ls") echo 'site_proxy=abc="site"' ;;
  "network connect")
    echo x >>/tmp/connects
    [ "$(wc -l </tmp/connects)" -gt 2 ] || { echo 'failed to add interface' >&2; exit 1; }
    ;;
  "inspect -f") case "$3" in
    *Config.Labels*) echo '"site_proxy"|"site"|,site_proxy,' ;;
    *) echo ' ' ;;
    esac ;;
  *) sleep 100 ;;
esac
EOF
chmod +x /fake/docker
PATH=/fake:$PATH ATTACH_PROJECT=self timeout 8 sh /attach
true`
	out := h.sh(dir, nil, `docker run --rm -v "$PWD/.devopsy/lib/attach:/attach:ro" busybox:1.37 sh -c "$1"`, fake)
	if !strings.Contains(out, "connected site_proxy") {
		t.Errorf("a connect that failed was not tried again:\n%s", out)
	}
}

// BUG: on a server behind an HTTP proxy, Docker hands containers the client's proxy
// settings, and attach and Traefik both reach their socket proxy over plain HTTP by
// name, so the name must be in noProxy. A noProxy listing dockerproxy worked before
// this branch; dockerproxy.<project>_dockerproxy is no subdomain of it, and netproxy
// is new. Attach is never healthy, so `up` never starts Traefik, and a Traefik that
// did start sees no containers. attach needs no proxy at all.
func TestReviewABehindAProxy(t *testing.T) {
	h, dir := ownTraefik(t)
	h.sh("/", nil, `mkdir -p /tmp/proxied && printf '%s\n' '{"proxies":{"default":{"httpProxy":"http://127.0.0.1:9","httpsProxy":"http://127.0.0.1:9","noProxy":"localhost,127.0.0.1,dockerproxy"}}}' >/tmp/proxied/config.json`)
	proxied := map[string]string{"DOCKER_CONFIG": "/tmp/proxied"}
	if out, err := h.shErr(dir, proxied, "devopsy up -d --wait --force-recreate --no-deps attach 2>&1"); err != nil {
		t.Errorf("attach behind a proxy: %v\n%s", err, out)
	}
	h.sh(dir, nil, "devopsy up -d --wait --force-recreate --no-deps attach")
	h.sh(dir, proxied, "devopsy up -d --wait --force-recreate --no-deps traefik")
	h.isolated("px-iso", "px-iso.example.com")
}

// BUG: tunnel-allow@file and the tunnel's trust keep the subnet deploy last read. A
// plain `devopsy up` after a `down` (commands/restart is one) makes the tunnel network
// again on whatever subnet is free, and another project may hold the old one: its
// containers then pass the allow list and are trusted for CF-Connecting-IP, while the
// connectors get a 403.
func TestReviewAStaleTunnelSubnet(t *testing.T) {
	h, dir := ownTraefik(t)
	subnet := func() string {
		return strings.TrimSpace(h.sh("/", nil, `docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' tunnel`))
	}
	old := subnet()
	h.sh(dir, nil, "devopsy down")
	var taken string
	t.Cleanup(func() {
		_, _ = h.shErr("/", nil, `docker rm -f stale; for n in $(docker network ls -q --filter name=^stale); do docker network rm "$n"; done`)
	})
	for i := 0; i < 20 && taken == ""; i++ {
		n := fmt.Sprintf("stale%d", i)
		h.sh("/", nil, `docker network create --label "com.docker.compose.project=$1" "$1_proxy" >/dev/null`, n)
		if strings.TrimSpace(h.sh("/", nil, `docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' "$1_proxy"`, n)) == old {
			taken = n
		}
	}
	if taken == "" {
		t.Skipf("no new network took %s", old)
	}
	h.sh("/", nil, `docker run -d --name stale --network "$1_proxy" -l "traefik.docker.network=$1_proxy" -l "com.docker.compose.project=$1" traefik/whoami:v1.11 >/dev/null`, taken)
	h.sh(dir, nil, "devopsy up -d --wait")
	// From inside: the host's address may have moved with the networks, and
	// api_access has not run. This says Traefik has read its dynamic files.
	h.waitFor("Traefik's routers", func() bool {
		_, err := h.shErr("/", nil, `docker exec traefik-traefik-1 wget -Y off -qO /dev/null http://127.0.0.1:8080/api/http/routers/devopsy-api@file`)
		return err == nil
	})
	t.Logf("the API from the host after the plain up: %s", h.sh("/", nil, `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8080/api/version || true`))
	h.waitFor("Traefik on the project's network", func() bool { return joined(h.proxyNetworks(), taken+"_proxy") })
	if subnet() == old {
		t.Fatalf("the tunnel network is on %s again", old)
	}
	code := h.from(taken+"_proxy", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Host: nothing.example.com", "http://"+h.address("traefik-traefik-1", taken+"_proxy")+":8081/")
	if code != "403" {
		t.Errorf("a project's container on the tunnel's old subnet got %s on the tunnel's entrypoint, want 403", code)
	}
	if code := h.from("tunnel", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Host: nothing.example.com", "http://traefik.tunnel:8081/"); code == "403" {
		t.Errorf("a connector on the tunnel network gets a 403")
	}
}

// A running project with a database moves by a real release, is rolled back and
// released forward, and a failed release brings the moved one back: what README's
// "Moving a running project works by a release" promises, through devopsy rather
// than plain compose. The site answers and the database keeps its name each time.
func TestReviewAMoveByRelease(t *testing.T) {
	h := traefik(t)
	dir := "/home/devopsy/moving"
	const target = "@devopsy@localhost:prod"
	t.Cleanup(func() { _, _ = h.shErr(dir, nil, "devopsy "+target+" --destroy --yes") })
	shared := `services:
  web:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.moving.rule=Host(` + "`moving.example.com`" + `)
    networks: [default, traefik]
  db:
    image: busybox:1.37
    command: [sleep, "100000"]
networks:
  traefik: {name: traefik, external: true}
`
	moved := `services:
  web:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.moving.rule=Host(` + "`moving.example.com`" + `)
      - traefik.docker.network=${COMPOSE_PROJECT_NAME}_proxy
    networks: [private, proxy]
  db:
    image: busybox:1.37
    command: [sleep, "100000"]
    networks: [private]
networks:
  private: {internal: true}
  proxy: {name: "${COMPOSE_PROJECT_NAME}_proxy"}
`
	h.sh("/", nil, `mkdir -p "$1/.devopsy" && printf '%s\n' 'project: moving' 'defaults:' '  mode: image' '  release: up -d --wait' '  rollback: up -d --wait' 'environments:' '  prod: {}' >"$1/.devopsy/config.yaml"`, dir)
	check := func(step string) {
		t.Helper()
		h.waitFor(step+": the site", func() bool { return h.request("https://moving.example.com/").Code == 200 })
		if out, err := h.inside("moving-prod-web-1", "nslookup -type=A db 127.0.0.11"); err != nil || !strings.Contains(out, "Name:") {
			t.Errorf("%s: db has no name: %v\n%s", step, err, out)
		}
	}
	release := func(file string) (string, error) {
		h.sh("/", nil, `printf '%s' "$2" >"$1/.devopsy/compose.yaml"`, dir, file)
		return h.shErr(dir, nil, "devopsy "+target+" --release --yes 2>&1")
	}
	for _, step := range []struct {
		name, file string
		roll       bool
	}{
		{"on the shared network", shared, false},
		{"released onto its own", moved, false},
		{"rolled back", "", true},
		{"released forward", moved, false},
	} {
		var out string
		var err error
		if step.roll {
			out, err = h.shErr(dir, nil, "devopsy "+target+" --rollback --yes 2>&1")
		} else {
			out, err = release(step.file)
		}
		if err != nil {
			t.Fatalf("%s: %v\n%s", step.name, err, out)
		}
		check(step.name)
	}

	// A release that fails, on an image with no such tag: the previous one, on its
	// own network, comes back with Traefik on that network.
	broken := strings.Replace(moved, "traefik/whoami:v1.11", "busybox:no-such-tag", 1)
	if out, err := release(broken); err == nil {
		t.Errorf("the broken release passed:\n%s", out)
	}
	check("after a failed release")
}

// The other side of TestReviewABehindAProxy: with the names as they are now in
// noProxy, both attach and Traefik work behind the proxy, so the names are all it takes.
func TestReviewABehindAProxyNamesListed(t *testing.T) {
	h, dir := ownTraefik(t)
	h.sh("/", nil, `mkdir -p /tmp/proxied && printf '%s\n' '{"proxies":{"default":{"httpProxy":"http://127.0.0.1:9","httpsProxy":"http://127.0.0.1:9","noProxy":"localhost,127.0.0.1,dockerproxy.traefik_dockerproxy,netproxy"}}}' >/tmp/proxied/config.json`)
	proxied := map[string]string{"DOCKER_CONFIG": "/tmp/proxied"}
	h.sh(dir, proxied, "devopsy up -d --wait --force-recreate --no-deps attach")
	h.sh(dir, proxied, "devopsy up -d --wait --force-recreate --no-deps traefik")
	h.isolated("pl-iso", "pl-iso.example.com")
}

// Traefik released as to a server, so under its compose project there, traefik-prod,
// not the tests' traefik: the network-scoped names, attach's project and its script
// through current. A site routes and Traefik joins its network, a connector gets
// through the tunnel's entrypoint, a release changing lib/attach reaches attach, and
// a rollback brings back the script and goes on working.
func TestReviewAReleasedTraefik(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	const target = "@devopsy@localhost:prod"
	t.Cleanup(func() { _, _ = h.shErr(dir, nil, "devopsy "+target+" --destroy --yes") })
	vars := map[string]string{"PROXY_LE_ACME_EMAIL": "admin@example.com", "PROXY_PUBLIC_IP": "203.0.113.10"}
	h.sh(dir, vars, "devopsy "+target+" --vars set PROXY_LE_ACME_EMAIL PROXY_PUBLIC_IP")
	h.sh(dir, nil, "devopsy "+target+" --release --yes")
	h.routed("devopsy-api@file")
	onSite := func(network string) bool {
		return joined(h.networksOf("traefik-prod-traefik-1"), network)
	}
	h.isolated("rl-iso", "rl-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return onSite("rl-iso_proxy") })
	h.waitFor("the site", func() bool { return h.request("https://rl-iso.example.com/").Code == 200 })
	if code := h.from("tunnel", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Host: nothing.example.com", "http://traefik.tunnel:8081/"); code != "404" {
		t.Errorf("a connector on the tunnel's entrypoint: %s, want 404", code)
	}

	script := func() string { return h.sh("/", nil, `docker exec traefik-prod-attach-1 tail -n 1 /attach`) }
	h.sh(dir, nil, `printf '# changed\n' >>.devopsy/lib/attach`)
	h.sh(dir, nil, "devopsy "+target+" --release --yes")
	if got := script(); !strings.Contains(got, "# changed") {
		t.Errorf("attach after a release changing its script: %s", got)
	}
	h.isolated("rm-iso", "rm-iso.example.com")
	h.waitFor("Traefik on a site after the release", func() bool { return onSite("rm-iso_proxy") })

	h.sh(dir, nil, "devopsy "+target+" --rollback --yes")
	if got := script(); strings.Contains(got, "# changed") {
		t.Errorf("attach after the rollback: %s", got)
	}
	h.isolated("rn-iso", "rn-iso.example.com")
	h.waitFor("Traefik on a site after the rollback", func() bool { return onSite("rn-iso_proxy") })
	h.waitFor("every site after the rollback", func() bool {
		return h.request("https://rl-iso.example.com/").Code == 200 && h.request("https://rn-iso.example.com/").Code == 200
	})
}

// sysctls turns IPv4 forwarding off on Traefik, the one container on every project's
// network. With two projects on IPv6 networks, IPv6 is off there too: a route through
// Traefik (NET_ADMIN only to add it) reaches nothing of the other's.
func TestReviewAIPv6ThroughTraefik(t *testing.T) {
	h, dir := ownTraefik(t)
	t.Cleanup(func() {
		_, _ = h.shErr("/", nil, `docker rm -f six-a six-b; docker network rm six-a_proxy six-b_proxy`)
	})
	for i, n := range []string{"six-a", "six-b"} {
		h.sh("/", nil, `docker network create --ipv6 --subnet "fd00:$2::/64" --label "com.docker.compose.project=$1" "$1_proxy" >/dev/null
			docker run -d --name "$1" --cap-add NET_ADMIN --network "$1_proxy" -l "traefik.docker.network=$1_proxy" -l "com.docker.compose.project=$1" traefik/whoami:v1.11 >/dev/null`, n, fmt.Sprintf("%d", 100+i))
	}
	h.sh(dir, nil, "devopsy up -d --wait --force-recreate --no-deps traefik")
	h.waitFor("Traefik on both", func() bool {
		n := h.proxyNetworks()
		return joined(n, "six-a_proxy") && joined(n, "six-b_proxy")
	})
	if out := strings.TrimSpace(h.sh("/", nil, `docker exec traefik-traefik-1 cat /proc/sys/net/ipv6/conf/all/forwarding`)); out != "0" {
		t.Errorf("Traefik forwards IPv6: %s", out)
	}
	six := func(c, network string) string {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f "{{(index .NetworkSettings.Networks \"$2\").GlobalIPv6Address}}" "$1"`, c, network))
	}
	b, via := six("six-b", "six-b_proxy"), six("traefik-traefik-1", "six-a_proxy")
	script := "ip -6 route add " + b + "/128 via " + via + " && wget -q -T 5 -O /dev/null http://[" + b + "]/"
	if out, err := h.inside("six-a", script); err == nil {
		t.Errorf("reached the other project's %s through Traefik: %s", b, out)
	}
}
