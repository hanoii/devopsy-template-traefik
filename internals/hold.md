# Hold

Holding a site's visitors through its app's stop, rather than a 502 or a 404, for the sites that ask (`hold@file`). Code: `plugins-local/.../hold/`, `hold_middleware` in `lib/common.sh`.

## The plugin

- Holding is a plugin off the response path, for the sites whose routers name `hold@file`: it never calls the next handler and never carries a site's response.
- Each probe is a new request through Traefik's own entrypoint, so through its current routers, and the visitor gets a 307 to the same URL once a router answers, which the browser follows into the site.
- The 307 comes only on that evidence, never on a timeout, and only once a probe of the URL answered down since an interval before the hold (the router gone counts, on a catch-all), so it cannot loop.
- The first probe goes at once, while a stopping app's port is still closed, and an up answer from before the hold is probed again.
- The evidence is the URL's, shared like its probes, since a visitor whose every round took another's answer sees no down of its own.
- A 502 the probe does not meet (a container restarted within a moment, or one the app gives that request itself) is redirected once per visitor (`X-Real-Ip`) and URL within ten intervals (`unseenOnce`), then the page is given: the errors middleware passes nothing of the response, so the hold cannot tell them apart.

## The errors middleware

- The 502 reaches the hold through the errors middleware, per router: compiled code on the response path, which on a match fetches from the hold service (Traefik itself on the loopback `hold` entrypoint, whose `hold` router runs the plugin) and passes everything else through unbuffered.
- The errors middleware gives the client the original status or a fixed rewrite, never the error service's, and copies the error service's headers, so its status rewrite turns 502 into 307 with the hold's `Location`.
- The middleware is per router, never on the entrypoints, since on an entrypoint the rewrite would turn every site's 502 into a 307 and a site could not opt out.
- Only 502 is held: a site's own 503 is its answer.
- A 307 for every method, never a 303 for a POST: a 502 does not tell a POST the closed port refused from one the app read and lost, so the 307 sends both again, as a reload would, where a 303 would turn a form posted during a stop into a GET, the form lost.
- `X-Devopsy-Hold: no` gets the hold's answer at once, with `X-Devopsy-Hold: down` (`domains`).

## The router gone

- A site whose router is gone is held by a catch-all with a memory from the API: Traefik builds every middleware again on each dynamic configuration change, so the plugin's constructor is the event, and a catch-all instance reads `/api/http/routers` then and 0.25, 1 and 3 s later (one reader, a change starting them over).
- The memory keeps the literal `Host()`s of the routers naming `hold@file`, with their entrypoints, since the errors path probes through one of them (a tunneled site has only `tunnel`).
- The memory is a difference between reads (present before, absent now: gone), since the API shows a new configuration about when the constructor runs, sometimes a moment after.
- A request for a host the last read had waits while those reads are due, up to 5 s (a host with only a path router pays it for its 404 right after a change).
- Package-level state survives the rebuilds.
- A host present is never held by the catch-all; one gone is held for 5 minutes, then gets the 404 again, so a removed environment does not say "back in a moment" for long.
- The memory comes from neither polling nor Docker events: the API is the truth about routers, and events would redo the provider's decisions from labels.

## Stopped and paused containers

- Neither `traefik.docker.allowNonRunning=true` (a stopped container's router kept with an empty service) nor `TRAEFIK_PROVIDERS_DOCKER_ALLOWEMPTYSERVICES=true` (an unhealthy or starting one's) is set: neither helps a request already waiting, and with nothing retrying both only turn a 404 into a 503.
- A paused container loses its router at Traefik's next read of the containers, which any start, die or health_status event on the host triggers, and gets it back only at the next such event after the unpause, which a container without a healthcheck never makes by itself.
- A paused container with a healthcheck is `unhealthy` at once and stays so after the unpause until its next check, the event that brings its router back.
- `docker stop` and `down` remove the container from Traefik's list, so only another router can answer for it, a project's own at priority 2 or more.
- No server-wide maintenance page: it answered only for environments down or removed and only on wildcard URLs, never the custom domains visitors use, and a removed environment saying "back soon" forever is worse than a 404.
