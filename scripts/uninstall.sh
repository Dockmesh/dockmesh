#!/usr/bin/env bash
# ============================================================================
#  Dockmesh interactive uninstaller.
#
#  Usage:
#    curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash
#    curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash -s -- --yes
#    curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash -s -- --purge --yes
#
#  Flags:
#    --yes       Accept every default (safe defaults — keeps data dir).
#    --purge     Also remove Dockmesh's files from the data directory
#                (DB, PKI, keys, stacks that hold nothing else).
#                Implies --yes. CANNOT BE UNDONE.
#    --dry-run   Print every action without executing.
#    NO_COLOR=1  Disable ANSI colors.
#
#  What this script NEVER does:
#    - Touches running containers. Stacks deployed through dockmesh keep
#      running after uninstall; you manage them manually from then on.
#    - Removes Docker itself.
#    - Removes files outside the dockmesh data dir, the paths its config
#      names, asset dir, binary dir, and service manager directories.
#    - Removes an agent installed on the same host.
# ============================================================================
set -euo pipefail

export LC_ALL=C

# ---------------------------------------------------------------------------
#  Color / TTY (mirrors install.sh so the two feel like one product).
# ---------------------------------------------------------------------------
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RST=$'\033[0m'
  FG_TITLE=$'\033[38;5;51m'
  FG_ACCENT=$'\033[38;5;44m'
  FG_OK=$'\033[38;5;42m'
  FG_WARN=$'\033[38;5;214m'
  FG_FAIL=$'\033[38;5;196m'
  FG_INFO=$'\033[38;5;38m'
  FG_MUTED=$'\033[38;5;240m'
  CH_OK='✔'; CH_INFO='ℹ'; CH_WARN='!'; CH_FAIL='✘'
else
  BOLD=; DIM=; RST=
  FG_TITLE=; FG_ACCENT=; FG_OK=; FG_WARN=; FG_FAIL=; FG_INFO=; FG_MUTED=
  CH_OK='[ok]'; CH_INFO='[i]'; CH_WARN='[!]'; CH_FAIL='[x]'
fi

say()  { printf '%s\n' "$*" >&2; }
ok()   { printf '   %s%s%s %s\n' "$FG_OK" "$CH_OK" "$RST" "$*" >&2; }
info() { printf '   %s%s%s %s\n' "$FG_INFO" "$CH_INFO" "$RST" "$*" >&2; }
warn() { printf '   %s%s%s %s\n' "$FG_WARN" "$CH_WARN" "$RST" "$*" >&2; }
fail() { printf '   %s%s%s %s\n' "$FG_FAIL" "$CH_FAIL" "$RST" "$*" >&2; }
die()  { printf '\n   %s%s%s %s\n\n' "$FG_FAIL" "$CH_FAIL" "$RST" "$*" >&2; exit 1; }

step() {
  printf '\n%s━━━━  %s%s%s  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n' \
    "$FG_TITLE" "$BOLD" "$*" "$RST" >&2
}

# ---------------------------------------------------------------------------
#  Args
# ---------------------------------------------------------------------------
YES=0
PURGE=0
DRYRUN=0
for arg in "$@"; do
  case "$arg" in
    --yes)     YES=1 ;;
    --purge)   PURGE=1; YES=1 ;;
    --dry-run) DRYRUN=1 ;;
    -h|--help)
      sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) die "unknown flag: $arg" ;;
  esac
done

# ---------------------------------------------------------------------------
#  Root check — most removals need root and the messaging is cleanest
#  if we fail up front rather than after the first "permission denied".
# ---------------------------------------------------------------------------
if [ "$(id -u)" != "0" ]; then
  die "This installer must run as root. Re-run with sudo:
     curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash"
fi

# ---------------------------------------------------------------------------
#  Runner — respects --dry-run and logs every command
# ---------------------------------------------------------------------------
run() {
  if [ "$DRYRUN" = "1" ]; then
    printf '   %s[dry-run]%s %s\n' "$FG_MUTED" "$RST" "$*" >&2
    return 0
  fi
  "$@"
}

# ---------------------------------------------------------------------------
#  Banner
# ---------------------------------------------------------------------------
cat >&2 <<BANNER

${FG_TITLE}${BOLD}  Dockmesh uninstaller${RST}
${DIM}  Interactive — you pick what gets removed.${RST}
BANNER

# ---------------------------------------------------------------------------
#  Env file helpers
#
#  The env file can sit in a directory the service user owns, so it is
#  read as data and never sourced: this script runs as root.
# ---------------------------------------------------------------------------

# env_value FILE KEY prints the last value FILE assigns to KEY, without
# quotes.
env_value() {
  head -c 65536 "$1" 2>/dev/null | grep -E "^[[:space:]]*(export[[:space:]]+)?$2=" | tail -n 1 | sed -E 's/^[^=]*=//' | tr -d "\"'" || true
}

# asset_dir_from_env FILE prints the asset directory FILE points at, or
# nothing.
asset_dir_from_env() {
  local v
  v="$(env_value "$1" DOCKMESH_BINARY_DIR)"
  [ -n "$v" ] || v="$(env_value "$1" DOCKMESH_INSTALL_SCRIPT)"
  [ -n "$v" ] || return 0
  case "$v" in
    /*) dirname -- "$v" ;;
  esac
}

# clean_path P prints P without trailing slashes.
clean_path() {
  local p="$1"
  while [ "${#p}" -gt 1 ] && [ "${p%/}" != "$p" ]; do
    p="${p%/}"
  done
  printf '%s' "$p"
}

# trusted_env_file FILE succeeds for a regular file, not a symlink, owned
# by the account running this script and writable by nobody else.
trusted_env_file() {
  [ -f "$1" ] && [ ! -L "$1" ] && [ -O "$1" ] || return 1
  [ -z "$(find "$1" -maxdepth 0 \( -perm -0020 -o -perm -0002 \) 2>/dev/null)" ]
}

# ---------------------------------------------------------------------------
#  Purge helpers
#
#  --purge removes only the files Dockmesh writes below its data
#  directories. Anything else stays and is reported with a keep line.
# ---------------------------------------------------------------------------

PURGE_ROOT=""
PURGE_ROOT_CHECKED=0
PURGE_DB_DIR=""
PURGE_DB_NAME="dockmesh.db"
PURGE_STACKS=""
PURGE_FILES=()
PURGE_KEPT=0
AGENT_DIRS=()
# IN_USE holds the host paths containers mount; IN_USE_KNOWN is 1 once
# Docker answered. SERVICE_USERS own what Dockmesh writes.
IN_USE=()
IN_USE_KNOWN=0
SERVICE_USERS="dockmesh root"

# keep P WHY reports a kept path. Names can come from container data, so
# control characters are dropped before they reach the terminal.
keep() {
  PURGE_KEPT=1
  warn "keep    $(printf '%s' "$1" | tr -d '\000-\037\177'): $(printf '%s' "$2" | tr -d '\000-\037\177')"
}

# owner_ok P succeeds when one of SERVICE_USERS owns P.
owner_ok() {
  local o u
  o="$(stat -c %U -- "$1" 2>/dev/null || stat -f %Su -- "$1" 2>/dev/null)" || return 1
  for u in $SERVICE_USERS; do
    [ "$o" = "$u" ] && return 0
  done
  return 1
}

# read_container_mounts fills IN_USE with the bind sources of every
# container, running or not.
read_container_mounts() {
  local ids src
  IN_USE=()
  IN_USE_KNOWN=0
  command -v docker >/dev/null 2>&1 || return 0
  ids="$(docker ps -aq 2>/dev/null)" || return 0
  IN_USE_KNOWN=1
  [ -n "$ids" ] || return 0
  while IFS= read -r src; do
    [ -n "$src" ] && IN_USE+=("$src")
  done < <(docker inspect --format '{{range .Mounts}}{{.Source}}{{"\n"}}{{end}}' $ids 2>/dev/null)
}

# mounted_below DIR prints a container bind source in or below DIR.
mounted_below() {
  local m
  for m in ${IN_USE[@]+"${IN_USE[@]}"}; do
    if under "$m" "$1"; then
      printf '%s' "$m"
      return 0
    fi
  done
  return 1
}

removed() {
  [ "$DRYRUN" = "1" ] || ok "removed $1"
}

# under P DIR succeeds when P is DIR or lies below it.
under() {
  case "$1" in
    "$2"|"$2"/*) return 0 ;;
  esac
  [ "$2" = "/" ]
}

# path_problem P prints why P cannot be purged: not a plain absolute path,
# or a symlink somewhere on the way.
path_problem() {
  local p="$1" q="" c
  case "$p" in
    /*) ;;
    *) printf 'not an absolute path'; return ;;
  esac
  case "$p" in
    *//*|*/./*|*/../*|*/.|*/..) printf 'not a plain path'; return ;;
  esac
  if [[ "$p" == *[!A-Za-z0-9._/-]* ]]; then
    printf 'contains characters other than letters, digits and ._-/'
    return
  fi
  [ "$p" = "/" ] && return
  local IFS=/
  for c in ${p#/}; do
    q="$q/$c"
    if [ -L "$q" ]; then
      printf '%s is a symbolic link' "$q"
      return
    fi
  done
}

# system_dir_problem P prints why P is no place for a data directory.
system_dir_problem() {
  case "$1" in
    /|/bin|/boot|/data|/dev|/etc|/home|/lib|/lib32|/lib64|/libx32|/media|/mnt|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/usr/local|/usr/local/var|/var|/var/lib|/var/lib/docker|/Applications|/Library|/System|/Users|/Volumes|/private)
      printf 'is a system directory' ;;
    /usr/local/var/*) ;;
    /bin/*|/boot/*|/dev/*|/etc/*|/lib/*|/lib32/*|/lib64/*|/libx32/*|/proc/*|/root/*|/run/*|/sbin/*|/sys/*|/usr/*|/var/lib/docker/*|/Library/*|/System/*|/private/*)
      printf 'lies in a system directory' ;;
  esac
}

# purge_refusal P prints why P from the server config is not purged.
purge_refusal() {
  local why
  why="$(path_problem "$1")"
  [ -n "$why" ] || why="$(system_dir_problem "$1")"
  printf '%s' "$why"
}

# infer_data_dir DB_PATH mirrors config.InferDataDir.
infer_data_dir() {
  local d
  d="$(dirname -- "$1")"
  if [ "${d##*/}" = "data" ]; then
    dirname -- "$d"
  else
    printf '%s' "$d"
  fi
}

# resolve_purge_paths ENV_FILE FALLBACK sets the PURGE_* paths the way the
# server resolves them from ENV_FILE (root's /etc/dockmesh/dockmesh.env).
# FALLBACK is the data root found next to a legacy dockmesh.env, used
# when ENV_FILE names none; its own file is not read, as the service
# account can write it.
resolve_purge_paths() {
  local env="$1" dir="" db="" stacks="" v key
  PURGE_FILES=()
  PURGE_ROOT_CHECKED=0
  if [ -e "$env" ] && ! trusted_env_file "$env"; then
    warn "$env is not a regular file that only root owns and can write; its paths are not used"
  elif [ -f "$env" ]; then
    dir="$(env_value "$env" DOCKMESH_DATA_DIR)"
    db="$(env_value "$env" DOCKMESH_DB_PATH)"
    stacks="$(env_value "$env" DOCKMESH_STACKS_ROOT)"
    for key in DOCKMESH_SECRETS_PATH DOCKMESH_SECRETS_KEY_PATH DOCKMESH_AUDIT_GENESIS_PATH; do
      v="$(env_value "$env" "$key")"
      [ -n "$v" ] && PURGE_FILES+=("$(clean_path "$v")")
    done
  fi
  [ -n "$dir" ] || { [ -n "$db" ] && dir="$(infer_data_dir "$db")"; }
  if [ -n "$dir" ]; then
    PURGE_ROOT="$(clean_path "$dir")"
    PURGE_ROOT_CHECKED=1
  else
    PURGE_ROOT="$2"
  fi
  if [ -n "$db" ]; then
    db="$(clean_path "$db")"
    PURGE_DB_DIR="$(dirname -- "$db")"
    PURGE_DB_NAME="${db##*/}"
  else
    PURGE_DB_DIR="$PURGE_ROOT/data"
    PURGE_DB_NAME="dockmesh.db"
  fi
  if [ -n "$stacks" ]; then
    PURGE_STACKS="$(clean_path "$stacks")"
  else
    PURGE_STACKS="$PURGE_ROOT/stacks"
  fi
}

# read_agent_dirs ENV_FILE sets AGENT_DIRS to the data and stacks dirs
# the agent's config names. They are only ever kept, so the file is read
# whoever owns it.
read_agent_dirs() {
  local v d data IFS=:
  AGENT_DIRS=()
  [ -f "$1" ] || return 0
  data="$(env_value "$1" DOCKMESH_DATA_DIR)"
  # The agent's own default (cmd/dockmesh-agent resolveDataDir).
  if [ -z "$data" ]; then
    data=/var/lib/dockmesh-agent
    if [ ! -e /var/lib/dockmesh-agent/agent.crt ] && [ -e /var/lib/dockmesh/agent.crt ]; then
      data=/var/lib/dockmesh
    fi
  fi
  AGENT_DIRS+=("$data/stacks")
  for v in "$data" "$(env_value "$1" DOCKMESH_STACKS_DIR)" "$(env_value "$1" DOCKMESH_EARLIER_STACKS_DIRS)"; do
    for d in $v; do
      case "$d" in
        /*) AGENT_DIRS+=("$(clean_path "$d")") ;;
      esac
    done
  done
}

# agent_installed UNIT BINARY CONFIG_DIR succeeds when any of them exists.
agent_installed() {
  local p
  for p in "$@"; do
    if [ -e "$p" ] || [ -L "$p" ]; then
      return 0
    fi
  done
  return 1
}

# agent_overlaps P succeeds when P is, holds or lies below a directory
# the agent on this host uses.
agent_overlaps() {
  local a
  for a in ${AGENT_DIRS[@]+"${AGENT_DIRS[@]}"}; do
    if under "$1" "$a" || under "$a" "$1"; then
      return 0
    fi
  done
  return 1
}

is_mountpoint() {
  command -v mountpoint >/dev/null 2>&1 && mountpoint -q "$1"
}

# remove_binaries BIN_DIR removes the server binaries; dockmesh-agent
# next to them belongs to the agent and stays.
remove_binaries() {
  local f
  for f in "$1/dockmesh" "$1/dockmesh.bak" "$1/dmctl"; do
    if [ -f "$f" ] || [ -L "$f" ]; then
      run rm -f -- "$f"
      removed "$f"
    fi
  done
}

remove_file() {
  if run rm -f -- "$1"; then
    removed "$1"
    return 0
  fi
  keep "$1" "could not be removed"
  return 1
}

# remove_tree DIR removes a directory that holds only Dockmesh's files,
# without crossing into other filesystems mounted below it.
remove_tree() {
  local rc=0
  case "$(uname -s)" in
    Darwin) run rm -rf -x -- "$1" || rc=$? ;;
    *) run rm -rf --one-file-system -- "$1" || rc=$? ;;
  esac
  if [ "$rc" = "0" ]; then
    removed "$1"
    return 0
  fi
  keep "$1" "could not be removed completely"
  return 1
}

# finish_dir DIR KEPT removes DIR once nothing in it was kept. A mount
# point stays.
finish_dir() {
  if [ "$2" != "0" ]; then
    return 1
  fi
  if is_mountpoint "$1"; then
    keep "$1" "mount point, only its content was removed"
    return 1
  fi
  if run rmdir -- "$1"; then
    removed "$1"
    return 0
  fi
  keep "$1" "not empty"
  return 1
}

# stack_file NAME succeeds for the files the stacks manager writes at the
# top of a stack dir.
stack_file() {
  case "$1" in
    compose.yaml|.env|.env.age|.dockmesh.meta.json) return 0 ;;
  esac
  [[ "$1" =~ ^compose\.[a-z0-9][a-z0-9_-]*\.yaml$ ]]
}

# stack_foreign DIR prints why DIR holds something Dockmesh did not
# write: a bind source of a container, any symlink, device, socket or file
# other than the stack files. Other empty directories don't count.
stack_foreign() {
  local dir="$1" p rel m
  if m="$(mounted_below "$dir")"; then
    printf 'a container mounts %s from it' "${m#"$dir"/}"
    return 0
  fi
  while IFS= read -r -d '' p; do
    rel="${p#"$dir"/}"
    if [ -L "$p" ]; then
      printf 'contains a symbolic link (%s)' "$rel"
      return 0
    fi
    [ -d "$p" ] && continue
    if [ -f "$p" ] && [ "${rel#*/}" = "$rel" ] && stack_file "$rel"; then
      continue
    fi
    printf 'contains files not written by Dockmesh (e.g. %s)' "$rel"
    return 0
  done < <(find -P "$dir" -mindepth 1 -print0 2>/dev/null)
  if ! find -P "$dir" -mindepth 1 >/dev/null 2>&1; then
    printf 'cannot be read completely'
  fi
}

purge_stack_dir() {
  local d="$1" why
  if [ -L "$d" ]; then
    keep "$d" "symbolic link"
    return 1
  fi
  if [ ! -d "$d" ]; then
    keep "$d" "not written by Dockmesh"
    return 1
  fi
  if agent_overlaps "$d"; then
    keep "$d" "used by the agent on this host"
    return 1
  fi
  why="$(stack_foreign "$d")"
  if [ -n "$why" ]; then
    keep "$d" "$why"
    return 1
  fi
  remove_tree "$d"
}

# purge_stacks DIR removes every stack dir in DIR that holds nothing
# foreign, then DIR itself once it is empty.
purge_stacks() {
  local s="$1" e kept=0
  if [ -L "$s" ]; then
    keep "$s" "symbolic link"
    return 1
  fi
  [ -d "$s" ] || return 0
  if agent_overlaps "$s"; then
    keep "$s" "used by the agent on this host"
    return 1
  fi
  if [ "$IN_USE_KNOWN" != "1" ]; then
    keep "$s" "Docker could not say which directories containers mount"
    return 1
  fi
  if ! owner_ok "$s"; then
    keep "$s" "not owned by the dockmesh account"
    return 1
  fi
  while IFS= read -r -d '' e; do
    purge_stack_dir "$e" || kept=1
  done < <(find -P "$s" -mindepth 1 -maxdepth 1 -print0 2>/dev/null)
  finish_dir "$s" "$kept"
}

is_purge_file() {
  local f
  for f in ${PURGE_FILES[@]+"${PURGE_FILES[@]}"}; do
    [ "$f" = "$1" ] && return 0
  done
  return 1
}

# purge_class NAME IS_ROOT IS_DB prints what NAME is in
# the data root or the database dir: file, tree (a directory only
# Dockmesh writes into), archive (backups or audit archives) or aside
# (what a restore or a failed setup set aside). Empty for anything else.
purge_class() {
  local n="$1" db="$PURGE_DB_NAME"
  case "$n" in
    .pre-restore|*.pre-restore-*|*.failed-*) printf aside; return ;;
    .restore-staged) printf tree; return ;;
    .dockmesh-doctor-probe) printf file; return ;;
  esac
  if [ "$2" = "1" ]; then
    case "$n" in
      dockmesh.env|.check-config-*) printf file; return ;;
      update|agent-release|.setup) printf tree; return ;;
      audit-archive) printf archive; return ;;
    esac
  fi
  if [ "$3" = "1" ]; then
    case "$n" in
      "$db"|"$db"-*|"$db".*) printf file; return ;;
      secrets.env|secrets.env.tmp-*|secrets.age-key|secrets.age-key.tmp-*|audit-genesis.sha256) printf file; return ;;
      agents-ca.crt|agents-ca.key|agents-server.crt|agents-server.key|ui-server.crt|ui-server.key) printf file; return ;;
      .agents-*.tmp-*|.ui-server.*.tmp-*) printf file; return ;;
      scan-tmp|grype-db|git-cache|caddy-admin) printf tree; return ;;
      backups|audit-archive) printf archive; return ;;
    esac
  fi
}

# purge_dir DIR removes Dockmesh's files from the data root or the
# database dir (or both, when they are one), then DIR once it is empty.
purge_dir() {
  local dir="$1" e name class kept=0 is_root=0 is_db=0
  [ "$dir" = "$PURGE_ROOT" ] && is_root=1
  [ "$dir" = "$PURGE_DB_DIR" ] && is_db=1
  while IFS= read -r -d '' e; do
    name="${e##*/}"
    if [ "$e" = "$PURGE_STACKS" ]; then
      purge_stacks "$e" || kept=1
      continue
    fi
    if [ "$e" = "$PURGE_DB_DIR" ] && [ ! -L "$e" ] && [ -d "$e" ]; then
      if agent_overlaps "$e"; then
        keep "$e" "used by the agent on this host"
        kept=1
      else
        purge_dir "$e" || kept=1
      fi
      continue
    fi
    class="$(purge_class "$name" "$is_root" "$is_db")"
    is_purge_file "$e" && class="file"
    if [ -L "$e" ]; then
      if [ "$is_root" = "1" ] && [ "$name" = "dockmesh.env" ]; then
        remove_file "$e" || kept=1
      else
        keep "$e" "symbolic link"
        kept=1
      fi
      continue
    fi
    case "$class" in
      file)
        if [ -f "$e" ]; then
          remove_file "$e" || kept=1
        else
          keep "$e" "not a regular file"
          kept=1
        fi
        ;;
      tree)
        if [ ! -d "$e" ]; then
          keep "$e" "not a directory"
          kept=1
        elif agent_overlaps "$e"; then
          keep "$e" "used by the agent on this host"
          kept=1
        elif ! owner_ok "$e"; then
          keep "$e" "not owned by the dockmesh account"
          kept=1
        else
          remove_tree "$e" || kept=1
        fi
        ;;
      archive)
        if [ -d "$e" ] && [ -z "$(find -P "$e" -mindepth 1 ! -type d -print -quit 2>/dev/null)" ]; then
          remove_tree "$e" || kept=1
        else
          keep "$e" "backup or audit archives; remove them by hand when you no longer need them"
          kept=1
        fi
        ;;
      aside)
        keep "$e" "set aside by a restore or a failed setup; check it and remove it by hand"
        kept=1
        ;;
      *)
        keep "$e" "not written by Dockmesh"
        kept=1
        ;;
    esac
  done < <(find -P "$dir" -mindepth 1 -maxdepth 1 -print0 2>/dev/null)
  finish_dir "$dir" "$kept"
}

# looks_like_data_root DIR succeeds for a directory holding something a
# Dockmesh server creates at the top of its data root.
looks_like_data_root() {
  [ -d "$1/data" ] || [ -d "$1/stacks" ] || [ -d "$1/.setup" ] || [ -d "$1/update" ] ||
    [ -e "$1/dockmesh.env" ] || [ -L "$1/dockmesh.env" ] || [ -e "$1/$PURGE_DB_NAME" ]
}

# agent_holds P succeeds when P is or lies below a directory the agent on
# this host uses.
agent_holds() {
  local a
  for a in ${AGENT_DIRS[@]+"${AGENT_DIRS[@]}"}; do
    under "$1" "$a" && return 0
  done
  return 1
}

# purge_root removes Dockmesh's files from PURGE_ROOT, and from the
# database and stacks dirs when the config puts them elsewhere.
purge_root() {
  local root="$PURGE_ROOT" why="" f p
  if [ "$PURGE_ROOT_CHECKED" = "1" ]; then
    why="$(purge_refusal "$root")"
  else
    why="$(path_problem "$root")"
  fi
  if [ -n "$why" ]; then
    keep "$root" "$why; not purged, remove Dockmesh's files there by hand"
    return 1
  fi
  if [ -d "$root" ]; then
    if agent_holds "$root"; then
      keep "$root" "used by the agent on this host"
      return 1
    fi
    for f in agent.crt agent.key agent.crt.next ca.crt agent.url enroll-token; do
      if [ -e "$root/$f" ]; then
        keep "$root" "holds agent files; used by the agent on this host"
        return 1
      fi
    done
  fi
  why="$(purge_refusal "$PURGE_DB_DIR")"
  if [ -n "$why" ]; then
    { [ -e "$PURGE_DB_DIR" ] || [ -L "$PURGE_DB_DIR" ]; } && keep "$PURGE_DB_DIR" "$why"
    PURGE_DB_DIR=""
  fi
  why="$(purge_refusal "$PURGE_STACKS")"
  if [ -n "$why" ]; then
    { [ -e "$PURGE_STACKS" ] || [ -L "$PURGE_STACKS" ]; } && keep "$PURGE_STACKS" "$why"
    PURGE_STACKS=""
  fi
  for f in ${PURGE_FILES[@]+"${PURGE_FILES[@]}"}; do
    under "$f" "$root" && continue
    [ -n "$PURGE_DB_DIR" ] && [ "$(dirname -- "$f")" = "$PURGE_DB_DIR" ] && continue
    [ -e "$f" ] || [ -L "$f" ] || continue
    why="$(purge_refusal "$f")"
    if [ -n "$why" ]; then
      keep "$f" "$why"
    elif [ -L "$f" ] || [ ! -f "$f" ]; then
      keep "$f" "not a regular file"
    else
      remove_file "$f" || true
    fi
  done
  if [ -n "$PURGE_DB_DIR" ] && [ "$(dirname -- "$PURGE_DB_DIR")" != "$root" ] && [ "$PURGE_DB_DIR" != "$root" ] && [ -d "$PURGE_DB_DIR" ]; then
    if agent_overlaps "$PURGE_DB_DIR"; then
      keep "$PURGE_DB_DIR" "used by the agent on this host"
    elif [ ! -e "$PURGE_DB_DIR/$PURGE_DB_NAME" ] || ! owner_ok "$PURGE_DB_DIR"; then
      keep "$PURGE_DB_DIR" "holds no Dockmesh database owned by the dockmesh account"
    else
      purge_dir "$PURGE_DB_DIR" || true
    fi
  fi
  [ "$PURGE_STACKS" = "$root" ] && PURGE_STACKS=""
  if [ -n "$PURGE_STACKS" ] && [ "$(dirname -- "$PURGE_STACKS")" != "$root" ] && [ "$(dirname -- "$PURGE_STACKS")" != "$PURGE_DB_DIR" ]; then
    purge_stacks "$PURGE_STACKS" || true
  fi
  if [ ! -d "$root" ]; then
    return 0
  fi
  if ! looks_like_data_root "$root"; then
    keep "$root" "does not look like a Dockmesh data directory"
    return 1
  fi
  purge_dir "$root"
}

# ---------------------------------------------------------------------------
#  Detect install layout
# ---------------------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *) die "unsupported OS: $OS" ;;
esac

BIN_DIR="${DOCKMESH_INSTALL_DIR:-/usr/local/bin}"
BIN_PATH="$BIN_DIR/dockmesh"

DATA_DIR_DEFAULT="/var/lib/dockmesh"
[ "$OS" = "darwin" ] && DATA_DIR_DEFAULT="/usr/local/var/dockmesh"

DATA_DIR="$DATA_DIR_DEFAULT"
DATA_ENV_FILE=""
# The detection list covers the platform defaults plus /data (a common
# operator override on VMs where /data is a separate, expandable
# volume — install.sh / dockmesh init both accept --data-dir /data).
# Without /data here, an operator who ran with that flag would see
# "skipping for safety" and the install would survive uninstall.
for candidate in \
  "/var/lib/dockmesh/dockmesh.env" \
  "/usr/local/var/dockmesh/dockmesh.env" \
  "/opt/dockmesh/dockmesh.env" \
  "/data/dockmesh.env"
do
  if [ -f "$candidate" ]; then
    DATA_DIR="$(dirname "$candidate")"
    DATA_ENV_FILE="$candidate"
    break
  fi
done
resolve_purge_paths /etc/dockmesh/dockmesh.env "$DATA_DIR"
DATA_DIR="$PURGE_ROOT"

AGENT_INSTALLED=0
if agent_installed /etc/systemd/system/dockmesh-agent.service /usr/local/bin/dockmesh-agent /etc/dockmesh-agent; then
  AGENT_INSTALLED=1
fi
read_agent_dirs /etc/dockmesh-agent/agent.env

# The asset directory is removed with rm -rf, so it comes only from root's
# environment or the default. The env files never choose it: the service
# user can write the data-dir copy, and migrate-layout copies its content
# into /etc.
ASSET_DIR="${DOCKMESH_ASSET_DIR:-$(dirname "$BIN_DIR")/share/dockmesh}"
case "$ASSET_DIR" in
  /|//*|*/..|*/../*|*/.|*/./*) die "DOCKMESH_ASSET_DIR must be a plain absolute path other than /" ;;
  /*) ;;
  *) die "DOCKMESH_ASSET_DIR must be an absolute path" ;;
esac
ASSET_DIR_DIFFERS=0
for f in "$DATA_ENV_FILE" /etc/dockmesh/dockmesh.env; do
  [ -n "$f" ] && [ -f "$f" ] || continue
  named="$(asset_dir_from_env "$f")"
  if [ -n "$named" ] && [ "$named" != "$ASSET_DIR" ]; then
    ASSET_DIR_DIFFERS=1
  fi
done

# Service file / label per platform.
SYSTEMD_UNIT="/etc/systemd/system/dockmesh.service"
LAUNCHD_PLIST="/Library/LaunchDaemons/dev.dockmesh.service.plist"
LAUNCHD_LABEL="system/dev.dockmesh.service"

# ---------------------------------------------------------------------------
#  Show plan
# ---------------------------------------------------------------------------
step "Detected install"
ok "OS                  $OS"
ok "Binary              $BIN_PATH"
ok "Data directory      $DATA_DIR"
ok "Asset directory     $ASSET_DIR"
if [ "$ASSET_DIR_DIFFERS" = "1" ]; then
  warn "dockmesh.env names a different asset directory; it is not removed. Check DOCKMESH_BINARY_DIR there and remove that folder by hand, or re-run with: sudo DOCKMESH_ASSET_DIR=<folder> bash uninstall.sh"
fi
case "$OS" in
  linux)  ok "Service unit        $SYSTEMD_UNIT" ;;
  darwin) ok "LaunchDaemon plist  $LAUNCHD_PLIST" ;;
esac
if [ "$AGENT_INSTALLED" = "1" ]; then
  info "An agent is installed on this host. It is not removed. Remove it with: sudo dockmesh-agent uninstall"
fi

# ---------------------------------------------------------------------------
#  Interactive choices (skipped when --yes)
# ---------------------------------------------------------------------------
ask() {
  # ask "prompt" "default [Y/n]"  → echoes 1 / 0
  local prompt="$1" default="$2"
  if [ "$YES" = "1" ]; then
    case "$default" in
      y|Y) echo 1 ;;
      *) echo 0 ;;
    esac
    return
  fi
  local hint
  case "$default" in
    y|Y) hint="[Y/n]" ;;
    *)   hint="[y/N]" ;;
  esac
  printf '   %s %s: ' "$prompt" "$hint" >&2
  local ans=""
  read -r ans </dev/tty || ans=""
  case "${ans,,}" in
    y|yes) echo 1 ;;
    n|no)  echo 0 ;;
    '')    case "$default" in y|Y) echo 1 ;; *) echo 0 ;; esac ;;
    *)     echo 0 ;;
  esac
}

step "Choose what to remove"

DO_STOP=$(ask "Stop the running service?"                  "y")
DO_UNIT=$(ask "Remove service unit / plist?"               "y")
DO_BIN=$(ask  "Remove the dockmesh binary?"                "y")
DO_ASSET=$(ask "Remove agent installer + bundled binaries?" "y")

if [ "$PURGE" = "1" ]; then
  DO_DATA=1
else
  DO_DATA=$(ask "Remove data directory (DB, stacks, age-keys)? ${FG_WARN}DESTRUCTIVE — cannot be undone${RST}" "n")
fi
# A running server would write into the files being removed.
[ "$DO_DATA" = "1" ] && DO_STOP=1

DO_USER=0
if [ "$OS" = "linux" ]; then
  if id dockmesh >/dev/null 2>&1; then
    DO_USER=$(ask "Remove the 'dockmesh' service user?" "y")
  fi
fi

# ---------------------------------------------------------------------------
#  Running-stack warning — we leave containers alone, but the operator
#  should know that before we tear down the management layer.
# ---------------------------------------------------------------------------
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  MANAGED_COUNT=$(docker ps --filter "label=com.docker.compose.project" --format '{{ .ID }}' 2>/dev/null | wc -l | tr -d ' ')
  if [ "${MANAGED_COUNT:-0}" -gt 0 ]; then
    step "Containers still running"
    warn "$MANAGED_COUNT container(s) with compose-project labels are running on this host."
    warn "The uninstaller will NOT stop them — they keep running after dockmesh is gone."
    warn "Manage them with plain 'docker compose' from their stack directories going forward."
  fi
fi

# ---------------------------------------------------------------------------
#  Execute
# ---------------------------------------------------------------------------
step "Executing"

# Stop service.
if [ "$DO_STOP" = "1" ]; then
  case "$OS" in
    linux)
      if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files dockmesh.service >/dev/null 2>&1; then
        run systemctl disable --now dockmesh.service 2>/dev/null || true
        ok "stopped + disabled dockmesh.service"
      else
        info "no systemd unit to stop"
      fi
      ;;
    darwin)
      if [ -f "$LAUNCHD_PLIST" ]; then
        run launchctl bootout "$LAUNCHD_LABEL" 2>/dev/null || true
        ok "unloaded $LAUNCHD_LABEL"
      else
        info "no LaunchDaemon plist to stop"
      fi
      ;;
  esac
  # Kill stragglers — someone may have started 'dockmesh serve' manually.
  # Anchored so dockmesh-agent and dmctl from the same directory survive.
  SERVER_PROC="^$BIN_DIR/dockmesh( |\$)"
  if pgrep -f "$SERVER_PROC" >/dev/null 2>&1; then
    run pkill -f "$SERVER_PROC" 2>/dev/null || true
    ok "killed lingering dockmesh processes"
  fi
fi

# Remove service unit / plist.
if [ "$DO_UNIT" = "1" ]; then
  case "$OS" in
    linux)
      if [ -f "$SYSTEMD_UNIT" ]; then
        run rm -f "$SYSTEMD_UNIT"
        run systemctl daemon-reload 2>/dev/null || true
        ok "removed $SYSTEMD_UNIT"
      fi
      ;;
    darwin)
      if [ -f "$LAUNCHD_PLIST" ]; then
        run rm -f "$LAUNCHD_PLIST"
        ok "removed $LAUNCHD_PLIST"
      fi
      ;;
  esac
fi

# Remove binary.
if [ "$DO_BIN" = "1" ]; then
  remove_binaries "$BIN_DIR"
fi

# Remove asset directory.
if [ "$DO_ASSET" = "1" ]; then
  if [ -d "$ASSET_DIR" ]; then
    run rm -rf "$ASSET_DIR"
    ok "removed $ASSET_DIR"
  fi
fi

# Setup mode keeps its state in the default data dir, so after the wizard
# chose another one both hold Dockmesh's files.
if [ "$DO_DATA" = "1" ]; then
  # Nothing running as the service account may swap a checked directory
  # for a link while root removes files below it.
  if [ "$OS" = "linux" ] && id dockmesh >/dev/null 2>&1; then
    run pkill -KILL -u dockmesh 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      pgrep -u dockmesh >/dev/null 2>&1 || break
      sleep 1
    done
  fi
  read_container_mounts
  purge_root || true
  if [ "$DATA_DIR_DEFAULT" != "$DATA_DIR" ] && [ -d "$DATA_DIR_DEFAULT" ]; then
    resolve_purge_paths "" "$DATA_DIR_DEFAULT"
    purge_root || true
  fi
  if [ -d /etc/dockmesh ]; then
    run rm -rf /etc/dockmesh
    ok "removed /etc/dockmesh (config)"
  fi
fi

# Remove service user. Two fixes for prior bugs:
#   1. systemctl stop sends SIGTERM and waits up to ~10s for the
#      process to exit. Doing userdel right after sometimes raced —
#      userdel refuses while any process owns the UID. We pkill -u
#      dockmesh first to clear any straggler before deleting.
#   2. The previous version emitted `ok "removed user"` unconditionally,
#      even when userdel had failed and warn already fired — claiming
#      success when none happened. Now: only emit ok when userdel
#      actually returns 0.
if [ "$DO_USER" = "1" ] && [ "$OS" = "linux" ]; then
  if id dockmesh >/dev/null 2>&1; then
    run pkill -KILL -u dockmesh 2>/dev/null || true
    if run userdel dockmesh 2>/dev/null; then
      ok "removed 'dockmesh' system user"
    else
      warn "userdel dockmesh failed — try manually: sudo pkill -KILL -u dockmesh && sudo userdel dockmesh"
    fi
  fi
fi

# ---------------------------------------------------------------------------
#  Summary
# ---------------------------------------------------------------------------
step "Done"
if [ "$DO_DATA" = "1" ]; then
  say "   $FG_WARN${BOLD}Dockmesh's data is removed.${RST} The database, stacks, deployment history,"
  say "   audit log and age-encryption keys are gone. If you had encrypted backups"
  say "   stored elsewhere, you can no longer decrypt them — the age key was inside"
  say "   the data dir."
  if [ "$PURGE_KEPT" = "1" ]; then
    say ""
    say "   The paths listed as ${BOLD}keep${RST} above hold files Dockmesh did not write, or"
    say "   archives, and were left in place. Check them and remove them by hand."
  fi
else
  say "   Data directory preserved at ${BOLD}$DATA_DIR${RST}."
  say "   Reinstall later with:"
  say "     curl -fsSL https://get.dockmesh.dev | sudo bash"
  say "   …and your DB + stacks + keys will be picked back up."
fi
say ""
say "   Running containers were $([ "${MANAGED_COUNT:-0}" -gt 0 ] && echo left alone || echo "not found")."
say ""
