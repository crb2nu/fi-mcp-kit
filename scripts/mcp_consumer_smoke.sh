#!/usr/bin/env bash
set -euo pipefail

DEFAULT_CONSUMERS=(
  loom-core
  fi-mcp-gateway
  mcp-orchestra
  mcp-sandbox
  diff-surgeon
)

TARGET_MODULES=(
  gitlab.flexinfer.ai/libs/mcp-go
  gitlab.flexinfer.ai/libs/fi-mcp-kit
  gitlab.flexinfer.ai/libs/fi-accel/go/fiaccel
)

usage() {
  cat <<'USAGE'
Usage: scripts/mcp_consumer_smoke.sh [--list|--dry-run|--run] [options]

Inspect or smoke-test MCP core consumers without mutating service repos.

Modes:
  --list                 Print dependency pins and local overrides (default).
  --dry-run              Print the go test commands that --run would execute.
  --run                  Run go test ./... for selected consumers.

Options:
  --services-root PATH   Services directory. Defaults to $SERVICES_ROOT or
                         the workspace services directory.
  --consumer NAME        Limit to one consumer. Repeatable.
  --help                 Show this help.

Environment:
  SERVICES_ROOT          Override the default services directory.
  GOFLAGS                Preserved, with -mod=readonly appended in --run mode.
USAGE
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
mode="list"
services_root="${SERVICES_ROOT:-}"
selected_consumers=()

find_services_root() {
  if [[ -n "${services_root}" ]]; then
    printf '%s\n' "${services_root}"
    return
  fi

  local dir="${repo_root}"
  while [[ "${dir}" != "/" ]]; do
    if [[ -d "${dir}/services" && -d "${dir}/libs" ]]; then
      printf '%s\n' "${dir}/services"
      return
    fi
    dir="$(dirname "${dir}")"
  done

  printf '%s\n' "/Users/cblevins/workspace/services"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --list)
      mode="list"
      shift
      ;;
    --dry-run)
      mode="dry-run"
      shift
      ;;
    --run)
      mode="run"
      shift
      ;;
    --services-root)
      services_root="$2"
      shift 2
      ;;
    --consumer)
      selected_consumers+=("$2")
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

services_root="$(find_services_root)"
if [[ ! -d "${services_root}" ]]; then
  printf 'services root does not exist: %s\n' "${services_root}" >&2
  exit 1
fi

consumers=("${DEFAULT_CONSUMERS[@]}")
if [[ ${#selected_consumers[@]} -gt 0 ]]; then
  consumers=("${selected_consumers[@]}")
fi

require_go_mod() {
  local consumer="$1"
  local consumer_dir="${services_root}/${consumer}"
  if [[ ! -f "${consumer_dir}/go.mod" ]]; then
    printf 'missing go.mod for %s at %s\n' "${consumer}" "${consumer_dir}" >&2
    return 1
  fi
}

go_mod_value() {
  local go_mod="$1"
  local kind="$2"
  awk -v kind="${kind}" '$1 == kind { print $2; exit }' "${go_mod}"
}

required_version() {
  local go_mod="$1"
  local module="$2"
  awk -v module="${module}" '$1 == module { print $2; found = 1; exit } END { if (!found) print "-" }' "${go_mod}"
}

replace_target() {
  local go_mod="$1"
  local module="$2"
  awk -v module="${module}" '
    $1 == "replace" && $2 == module && $3 == "=>" {
      print $4
      found = 1
      exit
    }
    END { if (!found) print "-" }
  ' "${go_mod}"
}

workspace_overrides() {
  local consumer_dir="$1"
  local go_work="${consumer_dir}/go.work"
  if [[ ! -f "${go_work}" ]]; then
    printf '%s\n' "-"
    return
  fi

  local matches
  matches="$(grep -E 'libs/(mcp-go|fi-mcp-kit|fi-accel)' "${go_work}" | sed 's/^[[:space:]]*//' | awk 'BEGIN { sep = "" } { printf "%s%s", sep, $0; sep = ", " }')"
  if [[ -z "${matches}" ]]; then
    printf '%s\n' "go.work present; no MCP core library use entries"
    return
  fi
  printf 'go.work: %s\n' "${matches}"
}

print_list() {
  printf 'services_root=%s\n' "${services_root}"
  printf 'consumer\tgo\tmcp-go\tfi-mcp-kit\tfi-accel\treplaces\tworkspace\tsmoke\n'

  local consumer consumer_dir go_mod module go_version mcp_go fi_mcp_kit fi_accel replaces workspace
  for consumer in "${consumers[@]}"; do
    require_go_mod "${consumer}"
    consumer_dir="${services_root}/${consumer}"
    go_mod="${consumer_dir}/go.mod"
    module="$(go_mod_value "${go_mod}" module)"
    go_version="$(go_mod_value "${go_mod}" go)"
    mcp_go="$(required_version "${go_mod}" gitlab.flexinfer.ai/libs/mcp-go)"
    fi_mcp_kit="$(required_version "${go_mod}" gitlab.flexinfer.ai/libs/fi-mcp-kit)"
    fi_accel="$(required_version "${go_mod}" gitlab.flexinfer.ai/libs/fi-accel/go/fiaccel)"
    replaces=""
    local target replacement
    for target in "${TARGET_MODULES[@]}"; do
      replacement="$(replace_target "${go_mod}" "${target}")"
      if [[ "${replacement}" != "-" ]]; then
        if [[ -n "${replaces}" ]]; then
          replaces="${replaces}; "
        fi
        replaces="${replaces}${target}=>${replacement}"
      fi
    done
    if [[ -z "${replaces}" ]]; then
      replaces="-"
    fi
    workspace="$(workspace_overrides "${consumer_dir}")"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t(cd %s && go test ./...)\n' \
      "${consumer}" "${go_version}" "${mcp_go}" "${fi_mcp_kit}" "${fi_accel}" \
      "${replaces}" "${workspace}" "${consumer_dir}"
    if [[ "${module}" == "" ]]; then
      printf 'empty module path for %s\n' "${consumer}" >&2
      return 1
    fi
  done
}

append_readonly_mod_flag() {
  local flags="${GOFLAGS:-}"
  if [[ " ${flags} " == *" -mod="* ]]; then
    printf '%s\n' "${flags}"
    return
  fi
  if [[ -z "${flags}" ]]; then
    printf '%s\n' "-mod=readonly"
    return
  fi
  printf '%s\n' "${flags} -mod=readonly"
}

git_status_snapshot() {
  local consumer_dir="$1"
  if git -C "${consumer_dir}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git -C "${consumer_dir}" status --porcelain=v1 --untracked-files=all
  fi
}

run_smoke() {
  local readonly_goflags
  readonly_goflags="$(append_readonly_mod_flag)"

  local consumer consumer_dir before after status
  status=0
  for consumer in "${consumers[@]}"; do
    require_go_mod "${consumer}"
    consumer_dir="${services_root}/${consumer}"
    printf '==> %s\n' "${consumer}"
    printf '    cd %s && GOFLAGS=%q go test ./...\n' "${consumer_dir}" "${readonly_goflags}"

    if [[ "${mode}" == "dry-run" ]]; then
      continue
    fi

    before="$(git_status_snapshot "${consumer_dir}")"
    if (cd "${consumer_dir}" && GOFLAGS="${readonly_goflags}" go test ./...); then
      :
    else
      status=1
    fi
    after="$(git_status_snapshot "${consumer_dir}")"
    if [[ "${before}" != "${after}" ]]; then
      printf 'service repo status changed after smoke: %s\n' "${consumer_dir}" >&2
      status=1
    fi
  done
  return "${status}"
}

case "${mode}" in
  list)
    print_list
    ;;
  dry-run|run)
    print_list
    printf '\n'
    run_smoke
    ;;
  *)
    printf 'internal error: unsupported mode %s\n' "${mode}" >&2
    exit 2
    ;;
esac
