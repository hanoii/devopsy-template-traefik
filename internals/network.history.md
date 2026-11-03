History, not rules: `internals/network.md` wins where they differ.

- The network-scoped name for a sibling: devopsy-template-drupal11's `alwaysup` resolved `app` to another site's app while its own was stopped.
- The socket proxy once allowed `containers/.*`, which also let through `export`, `archive`, `logs` and `attach` of every container.
- Traefik read-only with no capabilities, and no plugins storage for local plugins, was checked on v3.7.13.
- `api_access` was checked on Debian 13 with Docker 29: the host got 200, a container on `traefik` 403. Docker 29 printing a missing gateway as `invalid IP` made the whole router invalid, hence the filter.
- `PROXY_READ_TIMEOUT` exists because Traefik v3's default of 60s cut large uploads, like a registry's layers.
