//go:build e2e

package tests

import (
	"fmt"
	"strings"
	"testing"
)

// Review B of feat/traefik-isolated-network: the tunnel's allow list against
// forged headers, the hold through a site's recreate on its own network, and
// what attach forgets when it restarts.

// holdingCompose is isolatedCompose with an app that closes its port on SIGTERM
// and exits four seconds later, held through its stop (hold@file), as the
// templates' apps are.
func holdingCompose(name, hostname string) string {
	return fmt.Sprintf(`services:
  web:
    image: busybox:1.37
    command:
      - sh
      - -c
      - |
        mkdir -p /www && echo ok >/www/index.html && httpd -f -p 80 -h /www & pid=$$!
        trap 'kill $$pid; sleep 4; exit 0' TERM
        wait $$pid; wait $$pid
    stop_grace_period: 15s
    labels:
      - traefik.enable=true
      - traefik.http.routers.%[1]s.rule=Host(%[2]s)
      - traefik.http.routers.%[1]s.middlewares=hold@file
      - traefik.http.services.%[1]s.loadbalancer.server.port=80
      - traefik.docker.network=%[1]s_proxy
    networks: [private, proxy]
networks:
  private: {internal: true}
  proxy: {name: %[1]s_proxy}
`, name, "`"+hostname+"`")
}

// The tunnel entrypoint lets a request in by the address it comes from, never a
// header: forged X-Forwarded-For, X-Real-Ip and CF-Connecting-IP naming the
// tunnel's gateway or Traefik's loopback get a 403 from the shared network and
// from a project's own; from the tunnel network a routed host answers and an
// unknown one gets Traefik's 404, not the allow list's 403.
func TestReviewBTunnelAllowByAddressOnly(t *testing.T) {
	h := traefik(t)
	h.site("tunb", "tunb.example.com", []string{"routers.<name>.entrypoints=tunnel"}, "")
	h.isolated("tb-iso", "tb-iso.example.com")
	gw := strings.TrimSpace(h.sh("/", nil, `docker network inspect -f '{{(index .IPAM.Config 0).Gateway}}' tunnel`))
	if gw == "" {
		t.Fatal("no gateway on the tunnel network")
	}

	for host, want := range map[string]string{"tunb.example.com": "200", "nothing.example.com": "404"} {
		if out := h.from("tunnel", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Host: "+host, "http://traefik.tunnel:8081/"); out != want {
			t.Errorf("%s from the tunnel network: %s, want %s", host, out, want)
		}
	}
	for _, forged := range []string{gw, "127.0.0.1"} {
		headers := []string{"-H", "Host: tunb.example.com", "-H", "X-Forwarded-For: " + forged, "-H", "X-Real-Ip: " + forged, "-H", "CF-Connecting-IP: " + forged}
		if out := h.from("traefik", append([]string{"-o", "/dev/null", "-w", "%{http_code}"}, append(headers, "http://traefik:8081/")...)...); out != "403" {
			t.Errorf("forged %s from the shared network: %s, want 403", forged, out)
		}
		url := "http://" + h.address("traefik-traefik-1", "tb-iso_proxy") + ":8081/"
		wget := "wget -S -q -T 3 -O /dev/null --header 'Host: tunb.example.com' --header 'X-Forwarded-For: " + forged +
			"' --header 'X-Real-Ip: " + forged + "' --header 'CF-Connecting-IP: " + forged + "' " + url + " 2>&1 || true"
		if out, err := h.inside("tb-iso-web-1", wget); err != nil || !strings.Contains(out, "403") {
			t.Errorf("forged %s from a project's own network: %v\n%s", forged, err, out)
		}
	}
}

// A site on its own network is held through its recreate, as a release does it:
// its port closed, then its router gone, then a new container with a new address.
// Traefik stays on the network throughout, since a container naming it is always
// on it, so attach never disconnects it.
func TestReviewBHoldThroughRecreateOnOwnNetwork(t *testing.T) {
	h := traefik(t)
	h.compose("hr-iso", holdingCompose("hr-iso", "hr-iso.example.com"))
	h.routed("hr-iso@docker")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "hr-iso_proxy") })
	h.waitFor("the site answers", func() bool { return h.request("https://hr-iso.example.com/").Code == 200 })

	id := func() string { return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{.Id}}' hr-iso-web-1`)) }
	before := id()
	heldThrough(t, "https://hr-iso.example.com/", h.during("https://hr-iso.example.com/",
		"cd /home/devopsy/sites/hr-iso && docker compose -p hr-iso up -d --force-recreate web && sleep 2"))
	if id() == before {
		t.Errorf("the same container %s: nothing was recreated", before)
	}
	if !joined(h.proxyNetworks(), "hr-iso_proxy") {
		t.Errorf("Traefik off the site's network after its recreate: %s", h.proxyNetworks())
	}
	if logs := h.sh("/", nil, "docker logs traefik-attach-1 2>&1"); strings.Contains(logs, "disconnected hr-iso_proxy") {
		t.Errorf("attach left the network during the site's recreate:\n%s", logs)
	}
}

// attach remembers the networks Traefik left only in its process: restarted (a
// deploy recreating it, netproxy going away) within the 10 seconds, it never
// removes them, and an empty network of a project whose containers went stays
// until that project's next down. The containers go with compose's rm, which
// leaves the network as a down does while Traefik holds it.
// BUG: the sweep's list does not survive attach's restart.
func TestReviewBAttachRestartForgetsWhatTraefikLeft(t *testing.T) {
	h := traefik(t)
	h.isolated("rl-iso", "rl-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "rl-iso_proxy") })
	h.sh("/home/devopsy/sites/rl-iso", nil, "docker compose -p rl-iso rm -sf")
	h.waitFor("Traefik off the network", func() bool { return !joined(h.proxyNetworks(), "rl-iso_proxy") })
	if strings.TrimSpace(h.sh("/", nil, `docker network ls -q --filter name=^rl-iso_proxy$`)) == "" {
		t.Fatal("the network went with the containers: nothing left for the sweep")
	}
	t.Cleanup(func() { _, _ = h.shErr("/", nil, "docker network rm rl-iso_proxy >/dev/null 2>&1") })

	h.sh("/", nil, "docker restart -t 1 traefik-attach-1")
	h.waitFor("attach healthy again", func() bool {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{.State.Health.Status}}' traefik-attach-1`)) == "healthy"
	})
	h.passed()
	// No event says the 10 seconds are over, hence the sleep (as the lifecycle test).
	h.sh("/", nil, "sleep 12")
	if strings.TrimSpace(h.sh("/", nil, `docker network ls -q --filter name=^rl-iso_proxy$`)) != "" {
		t.Error("the network Traefik left before attach restarted is still there: attach forgot it")
	}
}
