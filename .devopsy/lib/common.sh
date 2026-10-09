# Shared by commands/ and lib/'s scripts: sourced, never run. POSIX sh.
#
# Traefik and acme-dns run as 10001, which owns mnt/letsencrypt and
# mnt/acmedns, mode 700: the deploy user running these scripts cannot read
# them. Anything touching them runs in a container as 10001 (in_traefik).
# jq comes from the jq service, so the host needs neither jq nor curl.

dir=${DEVOPSY_PROJECT_DIR:?run through devopsy}

die() {
  echo "$(basename "$0"): $*" >&2
  exit 1
}

warn() {
  echo "$(basename "$0"): $*" >&2
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
