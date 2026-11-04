//go:build e2e

package tests

import (
	"strconv"
	"strings"
	"testing"
)

// Second review round (Fable): what attach and Traefik's dependency on it do
// on the paths round one did not run.

// attach stops promptly: a down, a restart or a release that changes its
// script stops it, and compose waits for it before Traefik's dependents go on.
//
// Fails on the branch (bug): lib/attach runs as PID 1 in /bin/sh with no
// trap, so SIGTERM is ignored and every docker stop of attach takes the whole
// grace period, 10 s, before the SIGKILL.
func TestReview2AttachStopsPromptly(t *testing.T) {
	h := traefik(t)
	attach := "traefik-attach-1"
	state := func(format string) string {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f "$1" "$2"`, format, attach))
	}
	t.Cleanup(func() {
		_, _ = h.shErr("/", nil, `docker start "$1" >/dev/null`, attach)
		h.waitFor("attach healthy again", func() bool { return state("{{.State.Health.Status}}") == "healthy" })
	})
	out := strings.TrimSpace(h.sh("/", nil, `s=$(date +%s%N); docker stop "$1" >/dev/null; e=$(date +%s%N); echo $(( (e - s) / 1000000 ))`, attach))
	ms, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("stop time: %q", out)
	}
	if ms >= 5000 {
		t.Errorf("docker stop of attach took %d ms: the whole grace period, so the script ignores SIGTERM", ms)
	}
}

// Traefik recreated with projects attached keeps its default gateway on the
// traefik network once attach has it back on every project: Docker picks the
// gateway at start, and attach may have connected the created container
// before compose connected the network with gw_priority.
func TestReview2RecreateKeepsGateway(t *testing.T) {
	h := traefik(t)
	h.isolated("ga-iso", "ga-iso.example.com")
	h.isolated("gb-iso", "gb-iso.example.com")
	gateway := strings.TrimSpace(h.sh("/", nil, `docker network inspect -f '{{(index .IPAM.Config 0).Gateway}}' traefik`))
	route := func() string {
		return strings.TrimSpace(h.sh("/", nil, "docker exec traefik-traefik-1 ip route | grep ^default"))
	}
	if r := route(); !strings.Contains(r, gateway) {
		t.Fatalf("before the recreate the default route is not through traefik's gateway %s: %s", gateway, r)
	}
	for i := 0; i < 2; i++ {
		h.sh(proxy, nil, "devopsy up -d --wait --force-recreate traefik")
		h.waitFor("Traefik back on every project", func() bool {
			n := h.proxyNetworks()
			return joined(n, "ga-iso_proxy") && joined(n, "gb-iso_proxy")
		})
		for _, site := range []string{"ga-iso", "gb-iso"} {
			h.waitFor(site+" answers after the recreate", func() bool { return h.request("https://"+site+".example.com/").Code == 200 })
		}
		if r := route(); !strings.Contains(r, gateway) {
			t.Errorf("recreate %d: the default route left traefik's gateway %s: %s", i, gateway, r)
		}
	}
}

// A project taken down and brought up at once, many times: attach's removal
// of the network it just left never lands between compose seeing the network
// and creating the container on it.
func TestReview2DownUpAtOnce(t *testing.T) {
	h := traefik(t)
	h.isolated("du-iso", "du-iso.example.com")
	dir := "/home/devopsy/sites/du-iso"
	for i := 0; i < 10; i++ {
		if out, err := h.shErr(dir, nil, "docker compose -p du-iso down -v && docker compose -p du-iso up -d --wait"); err != nil {
			t.Fatalf("cycle %d: %v\n%s", i, err, out)
		}
		h.waitFor("up at once", func() bool { return h.request("https://du-iso.example.com/").Code == 200 })
	}
}

// A project's down while Traefik alone is stopped, attach running: attach
// takes the gone network off the stopped Traefik, which starts again.
func TestReview2DownWhileTraefikAloneStopped(t *testing.T) {
	h := traefik(t)
	h.isolated("da-iso", "da-iso.example.com")
	h.isolated("db-iso", "db-iso.example.com")
	h.waitFor("Traefik on the sites", func() bool {
		n := h.proxyNetworks()
		return joined(n, "da-iso_proxy") && joined(n, "db-iso_proxy")
	})
	h.sh(proxy, nil, "devopsy stop traefik")
	h.sh("/home/devopsy/sites/da-iso", nil, "docker compose -p da-iso down -v")
	h.waitFor("attach took the gone network off Traefik", func() bool { return !joined(h.proxyNetworks(), "da-iso_proxy") })
	if out, err := h.shErr(proxy, nil, "devopsy start traefik 2>&1"); err != nil {
		t.Fatalf("start does not bring Traefik back: %v\n%s", err, out)
	}
	h.routed("devopsy-api@file")
	h.waitFor("the other project answers", func() bool { return h.request("https://db-iso.example.com/").Code == 200 })
}
