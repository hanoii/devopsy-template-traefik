# Stopped sites

A project that names `hold@file` among its router's middlewares is held
through its app's stops:

```yaml
      - traefik.http.routers.myapp.middlewares=hold@file
```

Its visitors wait instead of getting an error, and are sent on to the
same URL (a 307) once the site answers: the real page, late. Two gaps
are covered:

- **The 502 at the stop.** A stopping app closes its port and finishes
  what is in flight, and Traefik keeps its router until Docker reports
  the container gone or unhealthy: a request meanwhile meets the closed
  port. `hold@file` is Traefik's errors middleware: on that 502 it asks
  the hold, which tries the URL through Traefik at once, then every
  second until a router answers it with anything but a 502. A 502 that
  first try does not meet is sent on once: a container restarted within
  a moment (its old router's 502, the site already back) goes through;
  a second such 502 for the same visitor and URL within 10 seconds gets
  the page below at once, so a request the app itself answers 502 (one
  that crashes it) is sent at most twice, never in a loop.
- **The router gone.** While the container is stopped, recreating or
  unhealthy, Traefik has no router for its hosts. A catch-all router at
  the lowest priority holds a request for a host whose router named
  `hold@file` within the last 5 minutes (read from Traefik's API); any
  other host gets the usual 404 (at once, or within a few seconds right
  after a change of routers, while Traefik's API catches up).

After 90 seconds the visitor gets a "back in a moment" page instead:
503 when the router is gone, under a 307 without a `Location` on the
502 path (the errors middleware keeps one status; browsers show the
page). The hold never carries a site's response, so streamed responses
keep their flushes. Only what follows a redirect is held: browsers do,
WebSockets do not. A POST is sent again with its body: right for one
refused by the closed port, which never reached the app; one the app
had read and then lost (cut by its stop past its grace period) is sent
twice, since a 502 does not tell the two apart. Only a 502 is held,
never a site's own 503 (a maintenance mode); an app answering 502 to
every request (its upstream down) is held as a stop, the page after 90
seconds. An app slow to answer after its start counts as up. Literal
`Host()` rules only: a `HostRegexp` router is not held when gone (its
502 is, probed through every entrypoint). Name `hold@file` before any
middleware that changes the path (`stripPrefix`, `replacePath`): the
errors middleware sees the URL as it reaches it, and the hold would
try, and send the visitor to, the changed one. A site's `ipAllowList`
must let the server's own `127.0.0.1` in, or every try is refused and
nothing is held (one redirect, then the page). A project that does not name it
gets Traefik's 502 and 404 as before.

A project can still answer its own stops with a router on the app's rule
at `priority=2`, which beats the catch-all: devopsy-template-drupal11's
`alwaysup` is a Caddy that waits for the app and shows its own
maintenance page. Give such a router the app's certificate resolver
label too, no `devopsy.data` label and no `depends_on` on the app (it
would stop with it): a sync or an import never stops it.
