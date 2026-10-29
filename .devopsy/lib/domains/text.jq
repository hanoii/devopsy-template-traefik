# domains' text, from the reports (report.jq): a block per host.
  .[] |
  "\(.host)  [\(if .live then "live" else "pending" end)]",
  (if .tunnel then "  route        Cloudflare tunnel" else "  resolver     \(.resolver // "-")" end),
  "  dns          \(if (.ips | length) == 0 then "does not resolve" else (.ips | join(", ")) + (if .on_server then " (this server)" elif .tunnel and .cf then " (Cloudflare proxy, \(if .status == 0 then "HTTPS fails at its edge" else "HTTP \(.status) through the tunnel" end))" elif .cdn then " (Cloudflare proxy, \(if .status == 0 then "HTTPS fails at its edge" else "HTTP \(.status) through it" end))" else " (not this server)" end) end)",
  (if .method == "dns-cname" and ((.valid | not) or .have != "") then
     "  challenge    \(if .have == "" then "missing" elif (.have | ascii_downcase) == (.want | ascii_downcase) then "ok" else "points to \(.have)" end)" else empty end),
  (if .tunnel then "  certificate  at Cloudflare's edge (Cloudflare tunnel)"
   elif .valid then "  certificate  valid, \(.issuer), expires \(.expire)"
   elif .issuer != "" then "  certificate  not valid for this name (\(.issuer))"
   else "  certificate  none (no TLS answer)" end),
  (if .live then empty else "  next         \(.next)" end),
  ""
