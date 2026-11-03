History, not rules: `internals/hold.md` wins where they differ.

- Holding as a plugin was decided on 2026-10-28. drupal11's `alwaysup` answered only once Traefik dropped the app's router, and a stopping app closes its port before that (FrankenPHP on SIGTERM; Traefik reads containers on `die` and `health_status` events only), so nearly every stop gave one 502; projects without such a router got the 404.
- The first `hold` plugin (removed 2026-10-13) failed on two measured counts, which this one avoids by construction: it retried its `next`, the chain built for the configuration the request entered, so it never followed a recreate or a dropped router; and it wrapped the writer, which loses flushes under Yaegi.
- A review found a 307 back into the same 502 sending a POST again until the browser's 20 redirects; giving the page at once instead failed the quick restart, which the tests' quick rounds found. Hence the redirect once per visitor and URL, the first probe at once, and an up answer from before the hold probed again.
- Per router, not on the entrypoints, and a 307 rather than a 303 for a POST, were decided with the maintainer.
- The API's timing was measured on 3.7.16: it shows the new configuration about when `New` runs, sometimes a moment after; two reads 1 ms apart disagreed, and a stop got 404s until the next read. Hence a memory of differences and the reads at 0.25, 1 and 3 s.
- Rejected for the memory: polling, and Docker events.
- A paused container's router: Traefik 3.7 filters any container whose status is not `running` (its `keepContainer`, health aside) and ignores pause and unpause events (checked on OrbStack); on Docker 29.9 a paused container with a healthcheck is also `unhealthy` at once.
- A server-wide maintenance page (an nginx at priority 1 for every host under the wildcard domain) was removed: it answered only for environments down or removed, and only on wildcard URLs.
