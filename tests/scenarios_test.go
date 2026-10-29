//go:build e2e

package tests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// proxy is the Traefik the scenarios share on the server host, deployed
// once per run as a server's is: `devopsy deploy` in its project, compose project traefik,
// its ports the host's 80, 443 and 127.0.0.1:8080.
const proxy = "/home/devopsy/traefik"

var (
	proxyOnce sync.Once
	proxyErr  error
)

// traefik deploys the shared Traefik the first time a test asks: a
// scenario's first call, which takes the server host (one).
func traefik(t *testing.T) {
	t.Helper()
	one(t, &serverMu)
	proxyOnce.Do(func() {
		if _, proxyErr = newProject(server, "traefik", nil); proxyErr != nil {
			return
		}
		if out, err := shErrOn(server, proxy, nil, "devopsy deploy"); err != nil {
			proxyErr = fmt.Errorf("devopsy deploy: %v\n%s", err, out)
		}
	})
	if proxyErr != nil {
		t.Fatal(proxyErr)
	}
}

// site runs a container routed through the shared Traefik for host (a
// Host() rule, or the rule itself when it has a parenthesis), on the
// traefik network, with labels added under traefik.http. (<name> its
// name); removed when the test ends. image runs args, or traefik/whoami
// without.
func site(t *testing.T, name, host string, labels []string, image string, args ...string) {
	t.Helper()
	rule := fmt.Sprintf("Host(`%s`)", host)
	if strings.Contains(host, "(") {
		rule = host
	}
	cmd := []string{"docker", "run", "-d", "--name", name, "--network", "traefik", "-l", "traefik.enable=true",
		"-l", fmt.Sprintf("traefik.http.routers.%s.rule=%s", name, rule)}
	for _, l := range labels {
		cmd = append(cmd, "-l", fmt.Sprintf("traefik.http.%s", strings.ReplaceAll(l, "<name>", name)))
	}
	if image == "" {
		image = "traefik/whoami:v1.11"
	}
	cmd = append(append(cmd, image), args...)
	shOn(t, server, "/", nil, `"$@" >/dev/null`, cmd...)
	t.Cleanup(func() { _, _ = shErrOn(server, "/", nil, `docker rm -f "$1" >/dev/null`, name) })
	routed(t, name+"@docker")
}

// routed waits for router to be in Traefik's API (it reads the
// containers a moment after they start).
func routed(t *testing.T, router string) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		var routers []struct{ Name, Status string }
		// The API may not answer yet: deploy opens it to the host once
		// Traefik runs (api_access).
		out, err := shErrOn(server, "/", nil, `curl -sf "http://127.0.0.1:8080/api/http/routers?per_page=1000"`)
		if err == nil && json.Unmarshal([]byte(out), &routers) == nil {
			for _, r := range routers {
				if r.Name == router {
					return
				}
			}
		}
		if time.Now().After(end) {
			t.Fatalf("no router %s in Traefik's API", router)
		}
	}
}

// unrouted waits for router to leave Traefik's API (a container stopped).
func unrouted(t *testing.T, router string) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		out, err := shErrOn(server, "/", nil, `curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:8080/api/http/routers/$1"`, router)
		if err == nil && out == "404" {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("router %s still in Traefik's API", router)
		}
	}
}

// api is Traefik's API at path, from the host.
func api(t *testing.T, path string) string {
	t.Helper()
	return shOn(t, server, "/", nil, `curl -sf "http://127.0.0.1:8080$1"`, path)
}

// request is one visitor's request to url on the host's ports, host
// resolved to the host itself: the code, its time and Location, with
// curl's extra arguments.
type answer struct {
	Code     int
	Time     float64
	Location string
}

func request(t *testing.T, url string, extra ...string) answer {
	t.Helper()
	host := regexp.MustCompile(`^https?://([^/:]+)`).FindStringSubmatch(url)[1]
	out := shOn(t, server, "/", nil, `url=$1 host=$2; shift 2
		curl -sk -o /dev/null --max-time 120 --resolve "$host:443:127.0.0.1" --resolve "$host:80:127.0.0.1" \
			-w '%{http_code} %{time_total} %{redirect_url}' "$@" "$url"`, append([]string{url, host}, extra...)...)
	return parseAnswer(out)
}

func parseAnswer(line string) answer {
	var a answer
	fmt.Sscan(line, &a.Code, &a.Time, &a.Location)
	return a
}

// during makes a visitor's request to url every 0.2 s, from a second
// before action (a shell script on the server host) until a second after
// it, and returns their answers.
func during(t *testing.T, url, action string) []answer {
	t.Helper()
	host := regexp.MustCompile(`^https?://([^/:]+)`).FindStringSubmatch(url)[1]
	out := shOn(t, server, "/", nil, `url=$1 host=$2 action=$3
		out=$(mktemp) stop=$(mktemp -u)
		(while [ ! -e "$stop" ]; do
			curl -sk -o /dev/null --max-time 120 --resolve "$host:443:127.0.0.1" 				-w '%{http_code} %{time_total} %{redirect_url}
' "$url" >>"$out" &
			sleep 0.2
		done; wait) &
		loop=$!
		sleep 1
		sh -c "$action" >/dev/null
		sleep 1
		touch "$stop"
		wait "$loop"
		cat "$out"`, url, host, action)
	var answers []answer
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		answers = append(answers, parseAnswer(line))
	}
	return answers
}

// heldThrough fails unless every answer is the site's 200 or the hold's
// 307 to url, and some were held.
func heldThrough(t *testing.T, url string, answers []answer) {
	t.Helper()
	held := 0
	for _, a := range answers {
		switch {
		case a.Code == 307 && a.Location == url:
			held++
		case a.Code != 200:
			t.Errorf("%+v", a)
		}
	}
	if held == 0 {
		t.Errorf("none held of %d", len(answers))
	}
}

// Traefik as deploy leaves it: every service healthy, every router and
// middleware enabled (the hold's YAML among them, as Traefik reads it),
// no error in its log but ACME's (no certificate can be issued here),
// http redirected, a site routed.
func TestStart(t *testing.T) {
	traefik(t)
	site(t, "start", "start.example.com", nil, "")

	ps := shOn(t, server, proxy, nil, `docker ps --filter label=com.docker.compose.project=traefik --format '{{.Names}} {{.Status}}'`)
	for _, line := range strings.Split(strings.TrimSpace(ps), "\n") {
		if !strings.Contains(line, "(healthy)") {
			t.Errorf("not healthy: %s", line)
		}
	}
	for _, kind := range []string{"routers", "middlewares", "services"} {
		var items []struct{ Name, Status string }
		if err := json.Unmarshal([]byte(api(t, "/api/http/"+kind+"?per_page=1000")), &items); err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, it := range items {
			names[it.Name] = true
			if it.Status != "enabled" {
				t.Errorf("%s %s: %s", kind, it.Name, it.Status)
			}
		}
		want := map[string][]string{
			"routers":     {"hold@file", "hold-websecure@file", "hold-tunnel@file", "devopsy-api@file", "start@docker"},
			"middlewares": {"hold@file", "hold-wait@file", "hold-websecure@file", "hold-tunnel@file", "forwarded@file"},
			"services":    {"hold@file"},
		}[kind]
		for _, n := range want {
			if !names[n] {
				t.Errorf("no %s %s", kind, n)
			}
		}
	}
	logs := shOn(t, server, proxy, nil, `docker logs traefik-traefik-1 2>&1`)
	for _, line := range strings.Split(logs, "\n") {
		if (strings.Contains(line, " ERR ") || strings.Contains(line, "level=error")) && !strings.Contains(strings.ToLower(line), "acme") {
			t.Errorf("Traefik's log: %s", line)
		}
	}
	if a := request(t, "http://start.example.com/x"); a.Code != 301 && a.Code != 308 || a.Location != "https://start.example.com/x" {
		t.Errorf("http: %+v", a)
	}
	if a := request(t, "https://start.example.com/"); a.Code != 200 {
		t.Errorf("https: %+v", a)
	}
}

// lib/facts on a running Traefik's routers: per host whether it is
// routed, its resolver and how its certificate is issued; one JSON
// document whatever a project's rule says.
func TestFacts(t *testing.T) {
	traefik(t)
	site(t, "facts-http", "facts-http.example.com", nil, "")
	site(t, "facts-tunnel", "facts-tunnel.example.com", []string{"routers.<name>.entrypoints=tunnel"}, "")
	// A stray quote in a rule once broke the document for the whole server.
	site(t, "facts-quote", `facts-"quote.example.com`, nil, "")
	shOn(t, server, proxy, nil, `touch .devopsy/mnt/dynamic/devopsy-retry-facts-http.example.com.yaml`)
	t.Cleanup(func() {
		_, _ = shErrOn(server, proxy, nil, `rm -f .devopsy/mnt/dynamic/devopsy-retry-facts-http.example.com.yaml`)
	})

	var facts struct {
		Retries []string
		Hosts   []struct {
			Host, Resolver, Method string
			Routed                 bool
			Entrypoints            []string
		}
	}
	out := shOn(t, server, proxy, nil, `devopsy t '"$dir/lib/facts" --all' 2>/dev/null`)
	if err := json.Unmarshal([]byte(out), &facts); err != nil {
		t.Fatalf("facts --all: %v\n%s", err, out)
	}
	byHost := map[string]int{}
	for i, h := range facts.Hosts {
		byHost[h.Host] = i
	}
	for host, want := range map[string]string{
		"facts-http.example.com":   "routed le-http01 http",
		"facts-tunnel.example.com": "routed  tunnel",
		`facts-"quote.example.com`: "routed le-http01 http",
	} {
		i, ok := byHost[host]
		if !ok {
			t.Errorf("no %s in %s", host, out)
			continue
		}
		h := facts.Hosts[i]
		if got := fmt.Sprintf("%s %s %s", map[bool]string{true: "routed"}[h.Routed], h.Resolver, h.Method); got != want {
			t.Errorf("%s: %q, want %q", host, got, want)
		}
	}
	if len(facts.Retries) != 1 || facts.Retries[0] != "facts-http.example.com" {
		t.Errorf("retries: %v", facts.Retries)
	}

	// Named hosts, one not routed.
	out = shOn(t, server, proxy, nil, `devopsy t '"$dir/lib/facts" FACTS-HTTP.example.com nowhere.example.com' 2>/dev/null`)
	if err := json.Unmarshal([]byte(out), &facts); err != nil || len(facts.Hosts) != 2 ||
		facts.Hosts[0].Host != "facts-http.example.com" || !facts.Hosts[0].Routed || facts.Hosts[1].Routed {
		t.Errorf("facts of two hosts: %v\n%s", err, out)
	}
}
