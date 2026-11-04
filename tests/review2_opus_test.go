//go:build e2e

package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// BUG (fails): a project taken down and up again while Traefik and attach are
// stopped, as `devopsy stop` leaves them. The network comes back with its name and a
// new ID, and Traefik's stored endpoint names the old ID. attach compares names, so
// it does nothing; Traefik cannot start, by `up` or by `deploy` (so neither by a
// release), and every site is down until someone recreates it by hand.
func TestReview2DownUpWhileTraefikStopped(t *testing.T) {
	h, dir := ownTraefik(t)
	h.isolated("su-iso", "su-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "su-iso_proxy") })
	h.sh(dir, nil, "devopsy stop")
	h.sh("/home/devopsy/sites/su-iso", nil, "docker compose -p su-iso down -v && docker compose -p su-iso up -d --wait")
	if out, err := h.shErr(dir, nil, "devopsy up -d --wait 2>&1"); err != nil {
		state := "docker inspect -f '{{.State.Status}} {{.State.Error}}' traefik-traefik-1"
		_, deployErr := h.shErr(dir, nil, "devopsy deploy 2>&1")
		afterDeploy := h.sh("/", nil, state)
		h.shErr(dir, nil, "docker network disconnect su-iso_proxy traefik-traefik-1 && devopsy up -d --wait")
		afterDisconnect := h.sh("/", nil, state)
		t.Fatalf("up does not bring Traefik back: %v\n%s\ndeploy after it: %v, %s\nnetwork disconnect by name, then up: %s", err, out, deployErr, afterDeploy, afterDisconnect)
	}
	h.routed("su-iso@docker")
	h.waitFor("the site answers", func() bool { return h.request("https://su-iso.example.com/").Code == 200 })
}

// BUG (fails): compose's own restart keeps no depends_on (only `restart: true` ones),
// so with a project's network gone while Traefik and attach were stopped, it starts
// Traefik before or without attach's first pass. `devopsy start`, `start traefik`,
// `up`, `up traefik` and the template's restart command all wait, and work.
func TestReview2ComposeRestartAfterGoneNetwork(t *testing.T) {
	h, dir := ownTraefik(t)
	for _, way := range []string{"devopsy -- restart", "devopsy -- restart traefik"} {
		name := "sg" + strings.NewReplacer(" ", "", "-", "").Replace(way) + "-iso"
		h.isolated(name, name+".example.com")
		h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), name+"_proxy") })
		h.sh(dir, nil, "devopsy stop attach traefik")
		h.sh("/home/devopsy/sites/"+name, nil, "docker compose -p "+name+" down -v")
		out, err := h.shErr(dir, nil, way+" 2>&1")
		if err != nil || strings.Contains(out, "not found") {
			t.Errorf("%s does not bring Traefik back: %v\n%s", way, err, out)
		}
		// What attach's next pass leaves makes a later up work.
		h.sh(dir, nil, "devopsy up -d --wait")
	}
}

// BUG (fails): attach's PID 1 ignores SIGTERM, so every stop takes the whole grace
// period: 10 s more on each down, and on a release that recreates it.
func TestReview2AttachStops(t *testing.T) {
	h, dir := ownTraefik(t)
	start := time.Now()
	h.sh(dir, nil, "devopsy stop attach")
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("attach took %s to stop", took)
	}
}

// A release that bumps socket-proxy recreates both proxies: attach loses netproxy
// and its stream during the same `up --wait` that then waits on attach.
func TestReview2SocketProxyBump(t *testing.T) {
	h, dir := ownTraefik(t)
	h.isolated("sb-iso", "sb-iso.example.com")
	for i := 1; i <= 2; i++ {
		// Only x-socket-proxy's healthcheck has this indent.
		out, err := h.shErr(dir, nil, fmt.Sprintf(`sed -i 's/^    interval: 1[0-9]s$/    interval: 1%ds/' .devopsy/compose.yaml; devopsy deploy 2>&1`, i))
		if err != nil {
			t.Fatalf("deploy %d: %v\n%s\nattach:\n%s", i, err, out, h.sh("/", nil, "docker logs traefik-attach-1 2>&1"))
		}
	}
	h.passed()
	if a := h.request("https://sb-iso.example.com/"); a.Code != 200 {
		t.Errorf("the site after the bumps: %+v", a)
	}
}

// BUG (fails under load): down and up at once. compose's down gives up on the network
// Traefik holds, the next up finds it and creates its container, and attach's pass
// from the down removes the network in between: `network ra-iso_proxy not found`, and
// up fails. 0 of 40 tries with three tests running beside it, on the second try with
// more; TestProjectNetworksLifecycle's three tries pass by the same luck.
func TestReview2DownUpRace(t *testing.T) {
	h := traefik(t)
	h.compose("ra-iso", `services:
  web:
    image: traefik/whoami:v1.11
    stop_grace_period: 1s
    labels:
      - traefik.enable=true
      - traefik.http.routers.ra-iso.rule=Host(`+"`ra-iso.example.com`"+`)
      - traefik.docker.network=ra-iso_proxy
    networks: [proxy]
networks:
  proxy: {name: ra-iso_proxy}
`)
	for i := 0; i < 10; i++ {
		h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "ra-iso_proxy") })
		if out, err := h.shErr("/home/devopsy/sites/ra-iso", nil, "docker compose -p ra-iso down && docker compose -p ra-iso up -d 2>&1"); err != nil {
			t.Fatalf("try %d: %v\n%s", i, err, out)
		}
	}
}

// Only Traefik stopped, attach running, as `devopsy stop traefik`, letsencrypt and
// forget leave it: attach gets to the network between down and up (8 of 8 by hand).
func TestReview2DownUpWhileOnlyTraefikStopped(t *testing.T) {
	h, dir := ownTraefik(t)
	h.isolated("so-iso", "so-iso.example.com")
	for i := 0; i < 2; i++ {
		h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "so-iso_proxy") })
		h.sh(dir, nil, "devopsy stop traefik")
		h.sh("/home/devopsy/sites/so-iso", nil, "docker compose -p so-iso down && docker compose -p so-iso up -d")
		if out, err := h.shErr(dir, nil, "devopsy up -d --wait 2>&1"); err != nil {
			t.Fatalf("try %d: %v\n%s", i, err, out)
		}
	}
}

// crossing sends from the first project to the second through Traefik's container,
// as a site with NET_ADMIN or NET_RAW can, and says whether it arrived.
func crossing(h host) bool {
	h.t.Helper()
	h.isolated("fa-iso", "fa-iso.example.com")
	h.isolated("fb-iso", "fb-iso.example.com")
	h.waitFor("Traefik on both", func() bool {
		n := h.proxyNetworks()
		return joined(n, "fa-iso_proxy") && joined(n, "fb-iso_proxy")
	})
	via := h.address("traefik-traefik-1", "fa-iso_proxy")
	target := h.address("fb-iso-web-1", "fb-iso_proxy")
	h.sh("/", nil, `docker run -d --name fwd-listen --network container:fb-iso-web-1 busybox:1.37 sh -c 'while :; do nc -u -l -p 9999 >>/tmp/got; done' >/dev/null`)
	h.t.Cleanup(func() { _, _ = h.shErr("/", nil, "docker rm -f fwd-listen") })
	// The listener hears its own namespace, or silence below proves nothing.
	h.waitFor("the listener", func() bool {
		h.sh("/", nil, `docker run --rm --network container:fb-iso-web-1 busybox:1.37 sh -c "echo control | nc -u -w1 $1 9999"`, target)
		return strings.Contains(h.sh("/", nil, `docker exec fwd-listen cat /tmp/got || true`), "control")
	})
	h.sh("/", nil, `docker run --rm --cap-add NET_ADMIN --network container:fa-iso-web-1 busybox:1.37 sh -c "ip route add $1/32 via $2; for i in 1 2 3; do echo across | nc -u -w1 $1 9999; done"`, target, via)
	time.Sleep(time.Second) // Silence has nothing to poll for.
	return strings.Contains(h.sh("/", nil, `docker exec fwd-listen cat /tmp/got || true`), "across")
}

// Traefik's container has ip_forward=1 and sits on every project's network. With the
// bridges' traffic through the host's iptables, Docker's own rules stop a packet
// across.
func TestReview2TraefikForwardsNot(t *testing.T) {
	h := traefik(t)
	if crossing(h) {
		t.Errorf("a packet from one project reached another's container through Traefik's")
	}
}

// The same on a host whose bridges skip iptables (no br_netfilter, or
// bridge-nf-call-iptables=0), which this sets in the test host's own namespace.
func TestReview2TraefikForwardsWithoutBridgeFilter(t *testing.T) {
	h := traefik(t)
	nf := "/proc/sys/net/bridge/bridge-nf-call-iptables"
	was, err := docker("exec", h.name, "cat", nf)
	if err != nil {
		t.Skipf("no br_netfilter here: %v", err)
	}
	t.Logf("bridge-nf-call-iptables was %s", was)
	t.Cleanup(func() {
		_, _ = docker("exec", h.name, "sh", "-c", "echo "+strings.TrimSpace(was)+" >"+nf)
	})
	if out, err := docker("exec", h.name, "sh", "-c", "echo 0 >"+nf); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if crossing(h) {
		t.Errorf("a packet from one project reached another's container through Traefik's")
	}
}
