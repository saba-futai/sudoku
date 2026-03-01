#!/bin/sh
set -eu

SUDOKU_BIN="${SUDOKU_BIN:-/usr/local/bin/sudoku}"

log() {
  echo "[entrypoint] $*" 1>&2
}

generate_xpv_table() {
  # 2x + 2p + 4v (8 chars)
  printf '%s\n' x x p p v v v v | awk 'BEGIN{srand()} {a[NR]=$0} END{for(i=NR;i>=1;i--){j=int(rand()*i)+1; t=a[i]; a[i]=a[j]; a[j]=t} for(i=1;i<=NR;i++) printf a[i]; printf "\n"}'
}

parse_flag_value() {
  # parse_flag_value -c "$@"
  want="$1"
  shift
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "$want" ]; then
      shift
      echo "${1:-}"
      return 0
    fi
    shift
  done
  echo ""
}

should_skip_init() {
  case " $* " in
    *" -keygen "*) return 0 ;;
    *" -tui "*) return 0 ;;
    *" -rev-dial "*) return 0 ;;
    *" -link "*) return 0 ;;
  esac
  return 1
}

init_server_config() {
  config_path="$1"

  if [ "${SUDOKU_FORCE_INIT:-0}" = "1" ]; then
    rm -f "$config_path"
  fi

  if [ -f "$config_path" ]; then
    return 0
  fi

  mkdir -p "$(dirname "$config_path")"

  local_port="${SUDOKU_PORT:-8080}"
  fallback_address="${SUDOKU_FALLBACK_ADDRESS:-}"
  suspicious_action="${SUDOKU_SUSPICIOUS_ACTION:-silent}"
  aead="${SUDOKU_AEAD:-chacha20-poly1305}"
  ascii="${SUDOKU_ASCII:-prefer_entropy}"
  padding_min="${SUDOKU_PADDING_MIN:-2}"
  padding_max="${SUDOKU_PADDING_MAX:-7}"
  enable_pure_downlink="${SUDOKU_ENABLE_PURE_DOWNLINK:-false}"

  httpmask_enabled="${SUDOKU_HTTP_MASK:-1}"
  httpmask_mode="${SUDOKU_HTTP_MASK_MODE:-auto}"
  httpmask_path_root="${SUDOKU_HTTP_MASK_PATH_ROOT:-}"
  httpmask_multiplex="${SUDOKU_HTTP_MASK_MULTIPLEX:-on}"

  if [ -z "${SUDOKU_CUSTOM_TABLE:-}" ]; then
    custom_table="$(generate_xpv_table)"
  else
    custom_table="${SUDOKU_CUSTOM_TABLE}"
  fi

  keys_path="${SUDOKU_KEYS_PATH:-/etc/sudoku/keys.env}"
  master_public_key=""
  available_private_key=""

  if [ -f "$keys_path" ]; then
    # shellcheck disable=SC1090
    . "$keys_path" || true
    master_public_key="${MASTER_PUBLIC_KEY:-}"
    available_private_key="${AVAILABLE_PRIVATE_KEY:-}"
  fi

  if [ -z "$master_public_key" ] || [ -z "$available_private_key" ]; then
    log "Generating new keypair..."
    out="$("$SUDOKU_BIN" -keygen 2>&1 || true)"
    master_public_key="$(printf '%s\n' "$out" | sed -n 's/.*Master Public Key:[[:space:]]*//p' | tail -n 1)"
    available_private_key="$(printf '%s\n' "$out" | sed -n 's/.*Available Private Key:[[:space:]]*//p' | tail -n 1)"

    if [ -z "$master_public_key" ] || [ -z "$available_private_key" ]; then
      log "Failed to parse keygen output:"
      printf '%s\n' "$out" 1>&2
      exit 1
    fi

    umask 077
    mkdir -p "$(dirname "$keys_path")"
    {
      echo "MASTER_PUBLIC_KEY=$master_public_key"
      echo "AVAILABLE_PRIVATE_KEY=$available_private_key"
    } >"$keys_path"
    umask 022

    log "Keys saved to $keys_path"
    log "Client key (AVAILABLE_PRIVATE_KEY): $available_private_key"
  fi

  if [ "$httpmask_enabled" = "0" ]; then
    httpmask_disable="true"
  else
    httpmask_disable="false"
  fi

  if [ -z "$fallback_address" ] || [ "$suspicious_action" = "silent" ]; then
    fallback_address=""
  fi

  tmp="$(mktemp)"
  cat >"$tmp" <<EOF
{
  "mode": "server",
  "transport": "tcp",
  "local_port": $local_port,
  "fallback_address": "$fallback_address",
  "key": "$master_public_key",
  "aead": "$aead",
  "suspicious_action": "$suspicious_action",
  "padding_min": $padding_min,
  "padding_max": $padding_max,
  "custom_table": "$custom_table",
  "custom_tables": [],
  "ascii": "$ascii",
  "enable_pure_downlink": $enable_pure_downlink,
  "httpmask": {
    "disable": $httpmask_disable,
    "mode": "$httpmask_mode",
    "tls": false,
    "host": "",
    "path_root": "$httpmask_path_root",
    "multiplex": "$httpmask_multiplex"
  }
}
EOF

  mv "$tmp" "$config_path"
  log "Generated server config at $config_path"

  if "$SUDOKU_BIN" -c "$config_path" -test >/dev/null 2>&1; then
    :
  else
    log "Generated config did not pass -test:"
    "$SUDOKU_BIN" -c "$config_path" -test 1>&2 || true
    exit 1
  fi

  if [ -n "${SUDOKU_PUBLIC_HOST:-}" ]; then
    if link="$("$SUDOKU_BIN" -c "$config_path" -export-link -public-host "${SUDOKU_PUBLIC_HOST}" 2>/dev/null | sed -n 's/.*Short link:[[:space:]]*//p' | tail -n 1)"; then
      if [ -n "$link" ]; then
        log "Short link: $link"
      fi
    fi
  fi
}

main() {
  if [ "${1:-}" = "sudoku" ]; then
    shift
  fi

  if [ "$#" -eq 0 ] || [ "${1#-}" != "$1" ]; then
    if should_skip_init "$@"; then
      exec "$SUDOKU_BIN" "$@"
    fi

    config_path="$(parse_flag_value -c "$@")"
    if [ -z "$config_path" ]; then
      config_path="/etc/sudoku/server.config.json"
    fi

    init_server_config "$config_path"
    exec "$SUDOKU_BIN" "$@"
  fi

  exec "$@"
}

main "$@"
