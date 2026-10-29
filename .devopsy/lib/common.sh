# Shared by commands/ and lib/'s scripts: sourced, never run. POSIX sh.
#
# Traefik and acme-dns run as 10001, which owns mnt/letsencrypt and
# mnt/acmedns and everything in them, files mode 600: the deploy user
# running these scripts cannot read them. Anything touching them runs in a
# container as 10001 (in_traefik).
# jq comes from the jq service, so the host needs neither jq nor curl.

dir=${DEVOPSY_PROJECT_DIR:?run through devopsy}

die() {
  echo "$(basename "$0"): $*" >&2
  exit 1
}

warn() {
  echo "$(basename "$0"): $*" >&2
}

# check_settings: refuses settings Traefik would take without a word. A
# router naming a resolver Traefik lacks, or one nothing answers, gets no
# certificate, which Traefik only logs.
# usable_resolver VAR VALUE: a DNS-01 resolver needs what answers it.
usable_resolver() {
  case $2 in
    le-dns01-selfhosted)
      case ",${COMPOSE_PROFILES:-}," in *,acmedns,*) ;; *)
        die "$1 is le-dns01-selfhosted: add acmedns to COMPOSE_PROFILES (devopsy setup)" ;;
      esac ;;
    le-dns01-cloudflare)
      [ -n "${PROXY_CLOUDFLARE_DNS_API_TOKEN:-}" ] ||
        die "$1 is le-dns01-cloudflare: set PROXY_CLOUDFLARE_DNS_API_TOKEN (devopsy setup)" ;;
  esac
}
check_settings() {
  case ${PROXY_CERTRESOLVER:-le-http01} in
    le-http01 | le-dns01-selfhosted | le-dns01-cloudflare) ;;
    *) die "PROXY_CERTRESOLVER must be le-http01, le-dns01-selfhosted or le-dns01-cloudflare: $PROXY_CERTRESOLVER" ;;
  esac
  case ${PROXY_WILDCARD_CERTRESOLVER:-auto} in
    auto | le-dns01-selfhosted | le-dns01-cloudflare | none) ;;
    *) die "PROXY_WILDCARD_CERTRESOLVER must be auto, le-dns01-selfhosted, le-dns01-cloudflare or none: $PROXY_WILDCARD_CERTRESOLVER" ;;
  esac
  # Anything but true would leave every site on the server's open ports.
  case ${PROXY_TUNNEL_ONLY:-} in
    '' | true | false) ;;
    *) die "PROXY_TUNNEL_ONLY must be true, false or unset: $PROXY_TUNNEL_ONLY" ;;
  esac
  case ",${COMPOSE_PROFILES:-}," in
    *,acmedns,*)
      [ -n "${PROXY_ACMEDNS_DOMAIN:-}" ] || die "COMPOSE_PROFILES has acmedns: set PROXY_ACMEDNS_DOMAIN in .env" ;;
  esac
  usable_resolver PROXY_CERTRESOLVER "${PROXY_CERTRESOLVER:-}"
  usable_resolver PROXY_WILDCARD_CERTRESOLVER "${PROXY_WILDCARD_CERTRESOLVER:-}"
  case ${PROXY_BASE_DOMAIN:-} in
    *[!a-z0-9.-]* | .* | *. | *..*) die "PROXY_BASE_DOMAIN is not a domain name: $PROXY_BASE_DOMAIN" ;;
  esac
}

# Runs devopsy, showing its messages (the compose notice, container
# creation) only when it fails. stdout passes through.
quiet() {
  _err=$(mktemp)
  set +e
  devopsy "$@" 2>"$_err"
  _rc=$?
  set -e
  [ "$_rc" -eq 0 ] || cat "$_err" >&2
  rm -f "$_err"
  return "$_rc"
}

# in_traefik SCRIPT [ARGS...]: runs a shell script in a one-off Traefik
# container, as 10001 with Traefik's mounts (/letsencrypt, /dynamic), no
# ports published. Works whether Traefik runs or not.
in_traefik() {
  _script=$1
  shift
  quiet run --rm --no-deps -T --entrypoint sh traefik -c "$_script" sh "$@"
}

# jq ARGS...: jq from the jq service, reading stdin.
jq() {
  quiet run --rm --no-deps -T jq "$@"
}

# Writes $2 to $1 when different; returns 1 when unchanged.
write_if_changed() {
  if [ -f "$1" ] && printf '%s' "$2" | cmp -s - "$1"; then
    return 1
  fi
  printf '%s' "$2" >"$1.tmp"
  mv "$1.tmp" "$1"
}

# Replaces KEY=value in .env, or appends it. Writes through the file, never
# replacing it: on a server .env is a link to shared/.env.
env_set() {
  _env=$dir/.env
  [ -e "$_env" ] || : >"$_env"
  _tmp=$(mktemp)
  awk -v key="$1" -v line="$1=$2" '
    index($0, key "=") == 1 { if (!done) print line; done = 1; next }
    { print }
    END { if (!done) print line }
  ' "$_env" >"$_tmp"
  cat "$_tmp" >"$_env"
  rm -f "$_tmp"
}

# The IPv4 of the default route: the public IP on most cloud servers.
detect_ip() {
  ip -4 route get 1.1.1.1 2>/dev/null | awk '{ for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit } }'
}

# Opens Traefik's API and dashboard (entrypoint traefik, :8080) to Traefik
# itself (lib/facts, through `exec`) and to the host (the published port,
# for SSH tunnels), never to projects' containers on the same networks.
# Connections from the host arrive from the gateway of one of Traefik's
# networks, which Docker assigns, so they are read from the running
# container: after every `up`. Until then the API answers 404.
api_access() {
  _id=$(devopsy ps -q traefik 2>/dev/null) && [ -n "$_id" ] || {
    warn "Traefik is not running: API access not updated"
    return 0
  }
  # Docker 29 prints a missing gateway as "invalid IP", older ones as "":
  # keep addresses only. One bad range would make the router invalid.
  _list="          - 127.0.0.1/32
          - ::1/128"
  for _gw in $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.Gateway}},{{.IPv6Gateway}},{{end}}' "$_id" | tr ',' '\n' | tr -d ' '); do
    case $_gw in
      *[!0-9a-f.:]* | '') ;;
      *:*) _list="$_list
          - $_gw/128" ;;
      *.*) _list="$_list
          - $_gw/32" ;;
    esac
  done
  if write_if_changed "$dir/mnt/dynamic/devopsy-api.yaml" "# Written by 'devopsy deploy' and 'devopsy restart'.
http:
  routers:
    devopsy-api:
      rule: 'Path(\`/\`) || PathPrefix(\`/api\`) || PathPrefix(\`/dashboard\`)'
      entryPoints: [traefik]
      service: api@internal
      middlewares: [devopsy-api-local, devopsy-api-root]
  middlewares:
    # / to the dashboard, as Traefik's insecure API does.
    devopsy-api-root:
      redirectRegex:
        regex: '^(https?://[^/]+)/\$'
        replacement: '\${1}/dashboard/'
    devopsy-api-local:
      ipAllowList:
        sourceRange:
$_list
"; then
    echo "$(basename "$0"): API open to Traefik and the host only"
  fi
}

# The tunnel network's IPv4 subnets, comma separated: what its connectors'
# requests come from. Empty until the network exists (a first `up`
# creates it); any other failure of Docker's fails, or Traefik would lose
# the tunnel's trust over a daemon restarting.
tunnel_subnet() {
  if ! _out=$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}},{{end}}' tunnel 2>&1); then
    case $_out in
      *"not found"*) return 0 ;;
      *) die "could not read the tunnel network: $_out" ;;
    esac
  fi
  printf '%s\n' "$_out" | tr ',' '\n' | grep -E '^[0-9.]+/[0-9]+$' | paste -sd, - || true
}

# Traefik's static settings that depend on what is enabled, in
# mnt/traefik.env (compose.yaml's env_file): the entrypoints' middlewares
# (a visitor's Forwarded removed on websecure and tunnel, then `devopsy
# proxies` on websecure and the tunnel's on tunnel), the proxies' plugin,
# the forwarded-headers trust, and the default entrypoints. One writer,
# called by deploy and proxies.
#
# The tunnel entrypoint trusts its network's subnet, read from Docker: its
# visitor's IP from CF-Connecting-IP (the `tunnel` middleware, the proxies
# plugin with that one entry, mnt/dynamic/tunnel.yaml) and its
# X-Forwarded-Proto, so applications build https URLs. Nothing else is on
# that network but Traefik and the connectors.
#
# An entrypoint naming a missing middleware breaks every router: a
# middleware's file is written before the env names it, and removed only
# once the running Traefik no longer names it. Static settings need a
# restart: done here when the file changed, unless --no-restart (deploy,
# whose `up` follows).
static_env() {
  _restart=true
  [ "${1:-}" != --no-restart ] || _restart=false

  _env="# Written by 'devopsy deploy' and 'devopsy proxies': Traefik's static settings.
"
  _plugin=false
  # A visitor's Forwarded (RFC 7239), which Traefik never removes, is
  # removed first on every entrypoint that reaches apps: an app trusting
  # Traefik would take its host and proto. Not web, which only redirects:
  # its internal routers are built before the file provider loads, so
  # each start would log them failing.
  write_if_changed "$dir/mnt/dynamic/forwarded.yaml" "# Written by 'devopsy deploy' and 'devopsy proxies': a visitor's Forwarded removed.
http:
  middlewares:
    forwarded:
      headers:
        customRequestHeaders:
          Forwarded: \"\"
" || true
  _websecure=forwarded@file
  _tunnelmw=forwarded@file

  # Proxies: configured and their middleware written (`devopsy proxies`).
  _proxies=false
  if [ -s "$dir/mnt/dynamic/proxies.yaml" ] && [ -f "$dir/mnt/proxies/proxies.conf" ] &&
    grep -qv '^[[:space:]]*\(#\|$\)' "$dir/mnt/proxies/proxies.conf"; then
    _proxies=true
    _plugin=true
    if [ -s "$dir/mnt/proxies/forwarded-ips" ]; then
      _env="${_env}TRAEFIK_ENTRYPOINTS_WEBSECURE_FORWARDEDHEADERS_TRUSTEDIPS=$(cat "$dir/mnt/proxies/forwarded-ips")
"
    fi
    _websecure="$_websecure,proxies@file"
  fi
  _env="${_env}TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=$_websecure
"

  # The tunnel: once its network exists.
  _tunnel=false
  _subnet=$(tunnel_subnet) || exit 1
  if [ -n "$_subnet" ]; then
    _tunnel=true
    _plugin=true
    write_if_changed "$dir/mnt/dynamic/tunnel.yaml" "# Written by 'devopsy deploy' and 'devopsy proxies' from the tunnel network's subnet.
http:
  middlewares:
    tunnel:
      plugin:
        proxies:
          proxies:
            - name: tunnel
              header: CF-Connecting-IP
              trustedIPs:
$(printf '%s\n' "$_subnet" | tr ',' '\n' | sed 's/^/                - /')
" || true
    _env="${_env}TRAEFIK_ENTRYPOINTS_TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=$_subnet
"
    _tunnelmw="$_tunnelmw,tunnel@file"
  fi
  _env="${_env}TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=$_tunnelmw
"

  if [ "$_plugin" = true ]; then
    _env="${_env}TRAEFIK_EXPERIMENTAL_LOCALPLUGINS_PROXIES_MODULENAME=github.com/hanoii/devopsy-traefik/proxies
"
  fi

  # Routers naming no entrypoint: only the tunnel's on a server whose sites
  # are all tunneled, else web and websecure, so a tunnel opens no site
  # that does not name it (PROXY_ENTRYPOINTS in the site templates).
  if [ "${PROXY_TUNNEL_ONLY:-}" = true ]; then
    _env="${_env}TRAEFIK_ENTRYPOINTS_TUNNEL_ASDEFAULT=true
"
  else
    _env="${_env}TRAEFIK_ENTRYPOINTS_WEB_ASDEFAULT=true
TRAEFIK_ENTRYPOINTS_WEBSECURE_ASDEFAULT=true
"
  fi

  if write_if_changed "$dir/mnt/traefik.env" "$_env" && [ "$_restart" = true ]; then
    echo "$(basename "$0"): restarting Traefik for its new static settings"
    devopsy up -d --wait
  fi

  # Middlewares turned off: their files go once Traefik stops naming them.
  if [ "$_proxies" = false ] || [ "$_tunnel" = false ]; then
    _running=
    if _id=$(devopsy ps -q traefik 2>/dev/null) && [ -n "$_id" ]; then
      _running=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$_id" |
        sed -n 's/^TRAEFIK_ENTRYPOINTS_[A-Z]*_HTTP_MIDDLEWARES=//p' | paste -sd, -)
    fi
    if [ "$_proxies" = false ]; then
      case ",$_running," in *,proxies@file,*) ;; *) rm -f "$dir/mnt/dynamic/proxies.yaml" ;; esac
    fi
    if [ "$_tunnel" = false ]; then
      case ",$_running," in *,tunnel@file,*) ;; *) rm -f "$dir/mnt/dynamic/tunnel.yaml" ;; esac
    fi
  fi
}

# Domains `devopsy forget` queued, one per line, without `*.`.
forget_list=$dir/mnt/forget.pending

# Traefik's stores as one JSON object, file name to content: every
# resolver's ACME file and the acme-dns registrations. With private keys:
# only ever piped into jq.
forget_stores() {
  # shellcheck disable=SC2016
  in_traefik '
    printf "{"
    sep=
    for f in acme-le-http01.json acme-le-dns01-selfhosted.json acme-le-dns01-cloudflare.json acme-dns-accounts.json; do
      [ -s "/letsencrypt/$f" ] || continue
      printf "%s\"%s\":" "$sep" "$f"
      cat "/letsencrypt/$f"
      sep=,
    done
    printf "}"' </dev/null
}

# forget_edits NAMES: what forgetting the names (one per line) changes in
# Traefik's stores, as tab-separated lines: `removed certificate <name>
# <resolver>`, `removed registration <name> <acme-dns subdomain>`, and
# `file <name> <json>` with
# the new content of each store that changes. A name stands for itself and
# its wildcard; a certificate goes when any of its names matches. The file
# lines hold private keys: only ever piped back into a Traefik container.
forget_edits() {
  forget_stores | jq -r --arg names "$1" '
    ($names | split("\n") | map(select(length > 0) | ltrimstr("*."))) as $n
    | def hit: ltrimstr("*.") as $b | ($n | index($b)) != null;
      def names: [.domain.main] + (.domain.sans // []);
      def certs: if type == "object" and (.Certificates | type) == "array" then .Certificates else [] end;
    to_entries[] | .key as $f | .value as $v
    | if $f == "acme-dns-accounts.json" then
        ($v | with_entries(select(.key | hit | not))) as $new
        | ($v | to_entries[] | select(.key | hit)
            | "removed\tregistration\t\(.key)\t\(.value.subdomain // "")"),
          (if $new != $v then "file\t\($f)\t\($new | tojson)" else empty end)
      else
        ($v | map_values(if (certs | length) > 0
            then .Certificates |= map(select(names | any(hit) | not))
            else . end)) as $new
        | ($v | to_entries[] | .key as $r | .value | certs[]
            | select(names | any(hit)) | "removed\tcertificate\t\(names | join(", "))\t\($r)"),
          (if $new != $v then "file\t\($f)\t\($new | tojson)" else empty end)
      end'
}

# Applies `devopsy forget`'s queue, for callers about to start Traefik
# (deploy, restart, `forget --now`): Traefik reads its stores only at
# startup and writes its own copy back on changes, so they are edited while
# it is stopped. Stops it only when a store has something to forget. Each
# edited store keeps a copy next to it (<file>.forget-<time>, 10001's).
# Forgotten registrations also leave acme-dns's own database, by their
# subdomain: acme-dns reads it on every request, so it keeps running.
forget_apply() {
  [ -s "$forget_list" ] || return 0
  _names=$(sort -u "$forget_list")
  _edits=$(forget_edits "$_names")
  if ! printf '%s\n' "$_edits" | grep -q '^removed'; then
    echo "$(basename "$0"): nothing stored for $(printf '%s' "$_names" | tr '\n' ' ')"
    rm -f "$forget_list"
    return 0
  fi
  echo "$(basename "$0"): stopping Traefik to forget $(printf '%s' "$_names" | tr '\n' ' ')"
  quiet stop traefik
  # Again, now that Traefik no longer writes.
  _edits=$(forget_edits "$_names")
  # shellcheck disable=SC2016
  _done=$(printf '%s\n' "$_edits" | in_traefik '
    umask 077
    tab=$(printf "\t")
    while IFS= read -r line; do
      case $line in
        "file$tab"*)
          rest=${line#"file$tab"}
          name=${rest%%"$tab"*}
          case $name in
            acme-le-http01.json | acme-le-dns01-selfhosted.json | acme-le-dns01-cloudflare.json | acme-dns-accounts.json) ;;
            *) continue ;;
          esac
          cp -p "/letsencrypt/$name" "/letsencrypt/$name.forget-$1"
          # Through the file: it keeps its mode, which Traefik checks.
          printf "%s\n" "${rest#*"$tab"}" >"/letsencrypt/$name"
          ;;
        "removed$tab"*) printf "%s\n" "$line" ;;
      esac
    done' "$(date +%Y%m%d%H%M%S)")
  printf '%s\n' "$_done" | while IFS="$(printf '\t')" read -r _ _kind _name _resolver; do
    case $_kind in
      certificate) echo "$(basename "$0"): forgot the certificate for $_name ($_resolver)" ;;
      registration) echo "$(basename "$0"): forgot the acme-dns registration of $_name" ;;
    esac
  done
  _subs=$(printf '%s\n' "$_done" | awk -F '\t' '$2 == "registration" && $4 ~ /^[0-9a-f-]+$/ { print $4 }')
  if [ -n "$_subs" ]; then
    # shellcheck disable=SC2086
    quiet run --rm --no-deps -T sqlite python3 -c '
import os, sqlite3, sys
path = "/acmedns/acme-dns.db"
if os.path.exists(path):
    db = sqlite3.connect(path, timeout=30)
    for sub in sys.argv[1:]:
        db.execute("DELETE FROM txt WHERE Subdomain = ?", (sub,))
        db.execute("DELETE FROM records WHERE Subdomain = ?", (sub,))
    db.commit()' $_subs </dev/null ||
      warn "could not remove the registrations from acme-dns's database"
  fi
  rm -f "$forget_list"
}
