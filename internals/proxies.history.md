History, not rules: `internals/proxies.md` wins where they differ.

- The plugin changing the request's remote address, and Traefik building X-Forwarded-For from it, was verified on v3.7.13.
- Yaegi's lost flushes: it hands a plugin's ResponseWriter wrapper to Go code as ResponseWriter plus Hijacker, never Flusher (yaegi v0.16.1, `stdlib/wrapper-composed.go`; traefik/traefik#10269). Websockets work through a wrapper (checked on v3.7.13).
- Yaegi's loop variable and map assignment (v0.16.1, 2026-10-28): `for page := 1; ; { ... page = next }` read the first page forever.
- Yaegi's unnamed parameters (v0.16.1, 2026-11-03): the hold's `CheckRedirect` was `func(*http.Request, []*http.Request) error`, and Go's client panicked calling it with `reflect.Set: value of type *http.Request is not assignable to type error`. The same function with named or `_` parameters ran; checked under `yaegi run`.
- Yaegi's `select` deadlock (2026-10-29): ten held requests each in `select { case <-ctx.Done(): case <-time.After(d): }` ended in "all goroutines are asleep".
- `TRAEFIK_EXPERIMENTAL_ABORTONPLUGINFAILURE` was checked on v3.7.13 with a broken plugin: skipped, every router went while the container looked healthy.
- The plugins' checksum label came after a release changing only a plugin neither recreated nor restarted Traefik, and the old plugin ran on (seen on a test machine, 2026-10-28, where a `devopsy restart` did not load the new one either).
- The forwarded headers set as for an untrusted peer: a visitor through the tunnel set the host Drupal builds links on, since Cloudflare passes the visitor's own X-Forwarded-* and overwrites only the proto (verified through a real tunnel).
- `forwarded@file`: Traefik never removes `Forwarded`, trusted peer or not (checked on v3.7, 2026-10-28). Drupal reads it by default and answered a 500 when it disagreed with X-Forwarded-Host; Symfony reads it when configured to. Decided over removing it in the plugin alone (requests from proxies only) and over leaving it to each app.
- `forwarded@file` on `web` made every start log the redirect's and ACME's internal routers failing, and for that second they did (seen on a test machine, 2026-10-28). `proxies@file` and `tunnel@file` have no internal routers on their entrypoints.
