# Stopped sites

A project that names `hold@file` among its router's middlewares is held
through its app's stops:

```yaml
      - traefik.http.routers.myapp.middlewares=hold@file
```

Its visitors wait instead of getting an error. Once the site answers,
they are sent on to the same URL (a 307), and get the real page, late.
The hold covers two gaps: the 502 at the stop, and the time the router is
gone.

## The 502 at the stop

A stopping app closes its port and finishes what is in flight, and
Traefik keeps its router until Docker reports the container gone or
unhealthy, so a request meanwhile meets the closed port. `hold@file` is
Traefik's errors middleware. On that 502 it asks the hold, which tries the
URL through Traefik at once, then every second, until a router answers it
with anything but a 502.

A 502 that the first try does not meet is sent on once, so a container
restarted within a moment (its old router's 502, with the site already
back) goes through. A second such 502 for the same visitor and URL within
10 seconds gets the page below at once. A request the app itself answers
with a 502 (one that crashes it) is therefore sent at most twice, never in
a loop.

## The router gone

While the container is stopped, being recreated or unhealthy, Traefik has
no router for its hosts. A catch-all router at the lowest priority holds a
request for a host whose router named `hold@file` within the last 5
minutes, read from Traefik's API. Any other host gets the usual 404, at
once, or within a few seconds right after a change of routers, while
Traefik's API catches up.

## After 90 seconds

After 90 seconds the visitor gets a "back in a moment" page instead. It is
a 503 when the router is gone. On the 502 path it comes under a 307
without a `Location`, since the errors middleware keeps one status;
browsers show the page.

## What is held

- The hold never carries a site's response, so streamed responses keep
  their flushes.
- Only what follows a redirect is held: browsers do, WebSockets do not.
- A POST is sent again with its body. That is right for one the closed
  port refused, which never reached the app. One the app had read and
  then lost (cut by its stop, past its grace period) is sent twice, since
  a 502 does not tell the two apart.
- Only a 502 is held, never a site's own 503 (a maintenance mode). An app
  that answers 502 to every request (its upstream down) is held as a
  stop, and gets the page after 90 seconds.
- An app slow to answer after its start counts as up.
- Only literal `Host()` rules are held when the router is gone. A
  `HostRegexp` router is not, though its 502 is held, probed through
  every entrypoint.
- Name `hold@file` before any middleware that changes the path
  (`stripPrefix`, `replacePath`). The errors middleware sees the URL as it
  reaches it, so the hold would try the changed URL, and send the visitor
  to it.
- A site's `ipAllowList` must let the server's own `127.0.0.1` in.
  Otherwise every try is refused and nothing is held: one redirect, then
  the page.

A project that does not name `hold@file` gets Traefik's 502 and 404 as
before.

## A project's own page

A project can still answer its own stops with a router on the app's rule
at `priority=2`, which beats the catch-all. devopsy-template-drupal11's
`alwaysup`, for example, is a Caddy that waits for the app and shows its
own maintenance page. Give that router the app's certificate resolver
label too, and give its service no `devopsy.data` label and no
`depends_on` on the app, which would stop it with the app. A sync or an
import then never stops it.
