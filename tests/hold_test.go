//go:build e2e

package tests

import (
	"fmt"
	"strings"
	"testing"
)

// The hold, as app-stop checked it by hand: a site naming hold@file is
// held through its app's stop, and nothing else is.

var hold = []string{"routers.<name>.middlewares=hold@file"}

// closing is a site that closes its port on SIGTERM and exits four
// seconds later, as an app finishing what is in flight does: Traefik
// keeps its router meanwhile, so a request meets the closed port (502).
var closing = []string{"busybox:1.37", "sh", "-c",
	`mkdir -p /www && echo ok >/www/index.html && httpd -f -p 80 -h /www & pid=$!
	trap 'kill $pid; sleep 4; exit 0' TERM
	wait $pid; wait $pid`}

// Router gone: a stopped container has none; the catch-all holds its
// host until it is back.
func TestHoldRouterGone(t *testing.T) {
	h := traefik(t)
	h.site("gone", "gone.example.com", hold, "")
	heldThrough(t, "https://gone.example.com/", h.during("https://gone.example.com/", "docker stop gone && sleep 6 && docker start gone"))
}

// Stops right after starts: every request held, never a 404 (a stop
// while the reader slept, Traefik's API a moment behind).
func TestHoldQuickRounds(t *testing.T) {
	h := traefik(t)
	h.site("rounds", "rounds.example.com", hold, "")
	heldThrough(t, "https://rounds.example.com/", h.during("https://rounds.example.com/",
		"for i in 1 2 3 4 5 6; do docker stop rounds; sleep 0.$i; docker start rounds; sleep $((i % 3)); done; sleep 2"))
}

// The 502 at the stop: the router still there, its port closed.
func TestHoldClosedPort(t *testing.T) {
	h := traefik(t)
	h.site("closing", "closing.example.com", append([]string{"services.<name>.loadbalancer.server.port=80"}, hold...), closing[0], closing[1:]...)
	heldThrough(t, "https://closing.example.com/", h.during("https://closing.example.com/", "docker stop closing && sleep 3 && docker start closing"))
}

// A HostRegexp router is remembered by no read: its 502 while the app
// closes its port is held all the same, probed through every entrypoint
// (once the container is gone its host gets the usual 404: README).
func TestHoldHostRegexp(t *testing.T) {
	h := traefik(t)
	h.site("regexp", "HostRegexp(`^regexp[.]example[.]com$`)", append([]string{"services.<name>.loadbalancer.server.port=80"}, hold...), closing[0], closing[1:]...)
	out := h.sh("/", nil, `docker kill -s TERM regexp >/dev/null && sleep 0.5
		curl -sk -o /dev/null --max-time 60 --resolve regexp.example.com:443:127.0.0.1 -w '%{http_code} %{time_total} %{redirect_url}' https://regexp.example.com/ >/tmp/regexp &
		docker wait regexp >/dev/null && docker start regexp >/dev/null && wait && cat /tmp/regexp`)
	if a := parseAnswer(out); a.Code != 307 || a.Location != "https://regexp.example.com/" || a.Time < 2 {
		t.Errorf("%+v, want held until the start, then a 307 to the same URL", a)
	}
}

// What is not held answers at once: a host never routed, a site without
// hold@file (its 404 and its 502 as before), a visitor asking not to be
// held; and a 502 the probe does not meet (a POST its app answers 502)
// is sent on once, then gets the page.
func TestHoldNot(t *testing.T) {
	h := traefik(t)
	h.site("plain", "plain.example.com", nil, "")
	h.site("plaindead", "plaindead.example.com", []string{"services.<name>.loadbalancer.server.port=81"}, "")
	h.site("dead", "dead.example.com", append([]string{"services.<name>.loadbalancer.server.port=81"}, hold...), "")
	h.site("postfail", "postfail.example.com", append([]string{"services.<name>.loadbalancer.server.port=80"}, hold...), "caddy:2-alpine", "sh", "-c",
		`printf '%s\n' ':80 {' '@post method POST' 'respond @post 502' 'respond 200' '}' >/tmp/Caddyfile && exec caddy run --config /tmp/Caddyfile`)
	h.site("stopped", "stopped.example.com", hold, "")
	h.sh("/", nil, "docker stop plain stopped >/dev/null")
	h.unrouted("plain@docker")
	h.unrouted("stopped@docker")

	for _, c := range []struct {
		name  string
		url   string
		extra []string
		code  int
		max   float64 // seconds
	}{
		{"never routed", "https://nowhere.example.com/", nil, 404, 1},
		// Within the reads due after a change of routers (settleWait).
		{"without hold@file, its router gone", "https://plain.example.com/", nil, 404, 6},
		{"without hold@file, its port closed", "https://plaindead.example.com/", nil, 502, 1},
		{"not held, its port closed", "https://dead.example.com/", []string{"-H", "X-Devopsy-Hold: no"}, 307, 1},
		{"not held, its router gone", "https://stopped.example.com/", []string{"-H", "X-Devopsy-Hold: no"}, 503, 6},
	} {
		a := h.request(c.url, c.extra...)
		if a.Code != c.code || a.Time > c.max || a.Location != "" {
			t.Errorf("%s: %+v, want %d within %gs and no Location", c.name, a, c.code, c.max)
		}
	}
	if a := h.request("https://postfail.example.com/form"); a.Code != 200 {
		t.Errorf("postfail's GET: %+v", a)
	}
	// As a browser follows it: the POST once again, then the page.
	if out := h.sh("/", nil, `curl -sk -L -d a=1 -o /tmp/postfail --resolve postfail.example.com:443:127.0.0.1 \
		-w '%{http_code} %{num_redirects} %{time_total} [%{redirect_url}]' https://postfail.example.com/form && grep -q 'Back in a moment' /tmp/postfail && echo ' page'`); !strings.HasPrefix(out, "307 1 0.") || !strings.HasSuffix(strings.TrimSpace(out), "[] page") {
		t.Errorf("a POST its app answers 502, followed: %s", out)
	}
	// The page, and the hold's mark domains reads.
	out := h.sh("/", nil, `curl -sk -D - --resolve dead.example.com:443:127.0.0.1 -H 'X-Devopsy-Hold: no' https://dead.example.com/`)
	if !strings.Contains(strings.ToLower(out), "x-devopsy-hold: down") || !strings.Contains(out, "Back in a moment") {
		t.Errorf("the page:\n%s", out)
	}
}

// What a site answers passes the hold's errors middleware as it comes: a
// streamed response in its pieces, a WebSocket's upgrade.
func TestHoldPassesThrough(t *testing.T) {
	h := traefik(t)
	h.site("stream", "stream.example.com", append([]string{"services.<name>.loadbalancer.server.port=80"}, hold...), "busybox:1.37", "sh", "-c",
		`mkdir -p /www/cgi-bin && printf '#!/bin/sh\necho "Content-Type: text/plain"\necho\necho one\nsleep 2\necho two\n' >/www/cgi-bin/stream &&
		chmod 755 /www/cgi-bin/stream && exec httpd -f -p 80 -h /www`)
	h.site("ws", "ws.example.com", hold, "")
	// Seconds from the request to each line.
	out := h.sh("/", nil, `start=$(date +%s%N); curl -skN --resolve stream.example.com:443:127.0.0.1 https://stream.example.com/cgi-bin/stream |
		while IFS= read -r l; do echo "$(( ($(date +%s%N) - start) / 100000000 )) $l"; done`)
	var first, second int
	var a, b string
	if n, _ := fmt.Sscan(out, &first, &a, &second, &b); n != 4 || a != "one" || b != "two" || first > 10 || second-first < 15 {
		t.Errorf("the stream, tenths of a second per line:\n%s", out)
	}
	out, _ = h.shErr("/", nil, `curl -sk -i -N --http1.1 --max-time 3 --resolve ws.example.com:443:127.0.0.1 -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
		-H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' https://ws.example.com/echo`)
	if !strings.HasPrefix(out, "HTTP/1.1 101") {
		t.Errorf("the WebSocket's upgrade:\n%s", out)
	}
}
