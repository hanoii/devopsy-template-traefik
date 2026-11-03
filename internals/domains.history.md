History, not rules: `internals/domains.md` wins where they differ.

- TLS to the server's own public IP was checked on DigitalOcean.
- Hand-built JSON broke for the whole server on one stray quote in a project's rule, hence only names matching a host name reach the check script.
- A 1033 for both "no connector" and "a record in another account than the tunnel's" was verified: Cloudflare sends no 1014 for a tunnel.
