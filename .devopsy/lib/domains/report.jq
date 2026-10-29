# domains' report, one per host: what Traefik knows and what was checked,
# and the next step. Input: lib/facts's document, then one object per
# checked name (reports() in commands/domains); $ip the server's public
# IP, $connected whether a connector here is healthy.
.[0] as $facts | (.[1:] | map({key: .name, value: .}) | from_entries) as $checks
| def ips($doh): [($doh.Answer // [])[] | select(.type == 1) | .data];
  def cname($doh): [($doh.Answer // [])[] | select(.type == 5) | .data | rtrimstr(".")] | first // "";
  def next($r):
    if ($r.routed | not) then "Traefik does not route it: is the site running, with a router rule for it?"
    elif $r.tunnel then
      if $r.tls then "its routers have TLS (PROXY_CERTRESOLVER in the site's target), so they never answer the tunnel's plain HTTP: unset it there (TLS ends at Cloudflare's edge), then --reload the site"
      elif $r.cferror == "1033" and $connected then "Cloudflare finds no connector of the tunnel its record names (1033): either that tunnel's connectors are not running here (the list below: an instance per Cloudflare account), or the record names another tunnel, or one of another Cloudflare account than the zone's (a zone reaches only its own account's tunnels: release a tunnel instance with a token from the zone's account and point the record at that tunnel)"
      elif $r.cferror == "1033" then "Cloudflare finds no connector of its tunnel (1033): see the connectors below"
      elif $r.cferror != "" then "Cloudflare error \($r.cferror)"
      elif ($r.ips | length) == 0 then "does not resolve: in the tunnel's Cloudflare account, a proxied CNAME to <tunnel id>.cfargotunnel.com (the name, or a wildcard covering it) and a published route sending it to http://traefik.tunnel:8081"
      elif ($r.cf | not) then "DNS does not go through Cloudflare: make its record a proxied CNAME (orange cloud) to <tunnel id>.cfargotunnel.com"
      elif $r.status == 0 then "Cloudflare answers HTTP but not HTTPS: its edge has no certificate for this name. Its free one only covers names one level below the zone (*.example.com), not deeper ones like this: a name one level down, or an edge certificate for it (Advanced Certificate Manager)"
      elif $r.held then "the tunnel reaches Traefik, but the site's app is down: Traefik holds its visitors until it answers"
      elif $r.status == 502 then "the tunnel answers, but its connector cannot reach Traefik (502): its published route must send this host to http://traefik.tunnel:8081"
      elif $r.status == 404 then "404 through the tunnel: unless the site itself answers 404 on /, the tunnel's published routes do not send this host to http://traefik.tunnel:8081"
      elif $r.status >= 500 then "HTTP \($r.status) through the tunnel"
      else "live" end
    elif $r.valid and $r.on_server then "live"
    elif $r.cdn then
      if $r.status == 0 then "Cloudflare answers HTTP but not HTTPS: its edge has no certificate for this name. Its free one only covers names one level below the zone (*.example.com), not deeper ones like this. Set the DNS record to DNS only (grey cloud), or get an edge certificate for it (Advanced Certificate Manager)"
      elif ($r.status | IN(521, 522, 523)) then "Cloudflare cannot reach the server (\($r.status)): check its DNS origin is \($ip) and ports 80 and 443 are open"
      elif $r.status == 525 then "Cloudflare's TLS handshake with the server fails (525): does the server have a certificate for this name?"
      elif $r.status == 526 then "Cloudflare rejects the server's certificate (526): get a valid one first, or set its SSL mode to Full"
      elif $r.status >= 520 and $r.status <= 530 then "Cloudflare reports an origin error (\($r.status))"
      elif $r.held then "Cloudflare reaches Traefik, but the site's app is down: Traefik holds its visitors until it answers"
      elif ($r.valid | not) then "served through Cloudflare (its SSL mode is not Full (strict)), but no certificate here yet: Traefik requests one once the site is routed, check again in a minute, else --retry; get one before switching to Full (strict)"
      else "live" end
    elif $r.valid then "certificate ready: point its DNS at \($ip) (A record) to switch"
    elif $r.method == "dns-cname" then
      if ($r.want // "") == "" then "its challenge record is not known yet (not registered): run with --retry"
      elif ($r.have | ascii_downcase) != ($r.want | ascii_downcase) then "create, where its DNS is hosted: _acme-challenge.\($r.host | ltrimstr("*.")). CNAME \($r.want)."
      else "challenge CNAME in place: run with --retry" end
    elif $r.method == "dns-api" then "run with --retry; the Cloudflare token must cover this domain's zone"
    elif $r.method == "http" then
      if $r.on_server then "DNS points here: run with --retry"
      else "point its DNS at \($ip) (HTTP-01 needs it), then run with --retry" end
    else "Traefik does not say how it issues this certificate (resolver \($r.resolver // "-")): run with --retry" end;
  [$facts.hosts[] | . as $h | ($h.probe // $h.host) as $name | $checks[$name] as $c
   | {host: $h.host, name: $name, routed: $h.routed, resolver: $h.resolver, method: $h.method,
      want: (($h.record // {}).target // "" | rtrimstr(".")), have: cname($c.challenge),
      ips: ips($c.a), valid: $c.valid, issuer: $c.issuer, expire: $c.expire, status: $c.status,
      tunnel: ($h.method == "tunnel"), tls: ($h.tls // false), cf: $c.cf, cferror: ($c.cferror // ""), held: ($c.held // false)}
   | .on_server = (.ips | index($ip) != null)
   | .cdn = ((.on_server | not) and $c.cf and (.tunnel | not))
   | .next = next(.)
   | .live = (.next == "live")
   | .ip = $ip
   | .checked = ($c != null)
   | if .checked then . else .next = "not a host name, or not checked" | .live = false end] as $reports
| $reports
