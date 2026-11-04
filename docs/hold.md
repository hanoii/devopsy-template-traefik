# Stopped sites

A project that names `hold@file` among its router's middlewares is held
through its app's stops:

```yaml
      - traefik.http.routers.myapp.middlewares=hold@file
```

Its visitors wait instead of getting an error. When the site answers
again, they are redirected (307) to the same URL and get the real page.
The hold covers two gaps: the 502 while the app stops, and the time its
router is gone.

## The 502 at the stop

A stopping app closes its port and finishes the requests in flight.
Traefik keeps its router until Docker reports the container gone or
unhealthy, and a request in that window hits the closed port and gets a
502. `hold@file` is Traefik's errors middleware, and on that 502 it asks
the hold. The hold tries the URL through Traefik at once, then every
second, until a router answers with something other than a 502.

If the first try gets no 502, the visitor is redirected once. That lets
through a container that restarted within a moment, when the 502 came
from its old router and the site is already back. A second such 502 for
the same visitor and URL within 10 seconds gets the "back in a moment"
page at once, so a request that the app itself answers with a 502, one
that crashes it, is sent at most twice and never loops.

## The router gone

While the container is stopped, being recreated or unhealthy, Traefik has
no router for its hosts. A catch-all router at the lowest priority holds
requests for any host whose router named `hold@file` in the last 5
minutes, which it reads from Traefik's API. Other hosts get the usual
404, at once, or within a few seconds right after routers change, while
Traefik's API catches up.

## The page after 90 seconds

A visitor still waiting after 90 seconds gets a "back in a moment" page.
When the router is gone the page comes as a 503. On the 502 path it comes
as a 307 without a `Location`, because the errors middleware keeps one
status, and browsers show the page.

## What is held

- The hold redirects the visitor, and the site's response reaches them
  directly, so streamed responses keep their flushes.
- Only clients that follow redirects are held. Browsers do; WebSockets do
  not.
- A POST is sent again with its body. That is right for a POST the closed
  port refused, which never reached the app. A POST the app had read and
  then lost, cut by its stop past its grace period, is sent twice, because
  the two give the same 502.
- Only a 502 is held. A site's own 503, from a maintenance mode, reaches
  the visitor as it is. An app that answers every request with a 502,
  because its upstream is down, is held like a stop and gets the page
  after 90 seconds.
- An app that is slow to answer after it starts counts as up.
- When the router is gone, only literal `Host()` rules are held. A
  `HostRegexp` router is held only for its 502 at the stop, probed through
  every entrypoint.
- List `hold@file` before any middleware that changes the path
  (`stripPrefix`, `replacePath`). The errors middleware sees the URL as it
  arrives there, and the hold would try the changed URL and send the
  visitor to it.
- A site's `ipAllowList` must allow the server's own `127.0.0.1`.
  Otherwise every try is refused and nothing is held: the visitor gets one
  redirect, then the page.

A project that does not name `hold@file` gets Traefik's 502 and 404 as
before.

## A project's own page

A project can answer its own stops with a router on the app's rule at
`priority=2`, which beats the catch-all. devopsy-template-drupal11's
`alwaysup` does this with a Caddy that waits for the app and shows its own
maintenance page. Give that router the app's certificate resolver label as
well. Leave its service without a `devopsy.data` label and without a
`depends_on` on the app, which would stop it with the app. A sync or an
import then never stops it.
