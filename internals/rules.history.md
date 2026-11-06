History, not rules: AGENTS.md's Rules win where they differ.

- The `jq` and `curl` tools run as 10001, though their images default to root and UID 100: both work as 10001 with a read-only root (jq 1.8.1 and curl 8.15.0, checked).
- Names became `PROXY_*` on 2026-10-26, never `DEVOPSY_*`, which made these look like devopsy's, nor `TRAEFIK_*`. `BASE_DOMAIN` was `WILDCARD_DOMAIN`, which said nothing of the domain every environment's default host goes under. A server's keys were renamed by hand, with no migration.
- Images float on the series: `traefik:v3.7` gets upstream's fixes with every release, since each release pulls its images again.
- On 2026-11-03 the README's reference moved into `docs/`, a doc per topic (a README of 746 lines had become what nobody read past the setup), with a line in Layout on keeping the doc that covers a change in step, and each area's line in the root naming its doc.
- socket-proxy moved from pinned exactly to `:1` on 2026-11-05, the maintainer's choice: fixes arrive by themselves as on the other images, it holds no data, and its README says the tag stays valid as long as there is no breaking change in the deployment. Reading its release notes on every upgrade goes with the pin.
- The docs once described the site templates' own variables (`SITE_HOST_RULE`, `PROXY_DOMAINS`, `PROXY_ENTRYPOINTS`, a site's `PROXY_CERTRESOLVER`) as the way to set a router's rule, entrypoints and resolver; they now name the labels, since a project that is not one of the templates sets those its own way.
