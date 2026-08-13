#!/usr/bin/env bash
# Bump every github.com/confighub/sdk/* requirement in the repo to one SDK
# release. The SDK modules are tagged in lockstep, so a single version applies
# to all of them; moving them together avoids the mixed-version state that a
# per-module bump would leave behind.
#
# Usage: scripts/update-sdk.sh [version]     # default: latest release of sdk/core
#
# Env:
#   SUMMARY_FILE  write a markdown summary here (for the PR body)
#   SKIP_BUILD=1  skip the `go build ./...` verification
#
# A module whose tidy or build fails after the bump is restored to its previous
# go.mod/go.sum and reported as skipped, so one stale example does not hold back
# the rest.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

version="${1:-}"
if [[ -z "${version}" ]]; then
  version="$(curl -fsSL https://proxy.golang.org/github.com/confighub/sdk/core/@latest |
    sed -n 's/.*"Version":"\([^"]*\)".*/\1/p')"
fi
if [[ ! "${version}" =~ ^v[0-9] ]]; then
  echo "Could not determine an SDK version (got: '${version}')" >&2
  exit 1
fi
echo "==> Target SDK version: ${version}"

updated=()
skipped=()
unchanged=()

while IFS= read -r modfile; do
  dir="$(dirname "${modfile}")"
  name="${dir#./}"
  [[ "${name}" == "." ]] && name="$(basename "${repo_root}") (root module)"

  sdk_mods="$(grep -oE 'github\.com/confighub/sdk/[A-Za-z0-9_./-]+' "${modfile}" | sort -u || true)"
  [[ -n "${sdk_mods}" ]] || continue

  targets=()
  for mod in ${sdk_mods}; do
    targets+=("${mod}@${version}")
  done

  echo "==> ${name}"
  cp "${dir}/go.mod" "${dir}/go.mod.orig"
  if [[ -f "${dir}/go.sum" ]]; then
    cp "${dir}/go.sum" "${dir}/go.sum.orig"
  fi

  failure=""
  (
    cd "${dir}"
    go get "${targets[@]}" && go mod tidy
  ) || failure="go get / go mod tidy"

  if [[ -z "${failure}" && "${SKIP_BUILD:-}" != "1" ]]; then
    (cd "${dir}" && go build ./...) || failure="go build"
  fi

  if [[ -n "${failure}" ]]; then
    echo "    skipped: ${failure} failed"
    mv "${dir}/go.mod.orig" "${dir}/go.mod"
    if [[ -f "${dir}/go.sum.orig" ]]; then
      mv "${dir}/go.sum.orig" "${dir}/go.sum"
    fi
    skipped+=("${name} (${failure} failed)")
    continue
  fi

  if cmp -s "${dir}/go.mod" "${dir}/go.mod.orig"; then
    unchanged+=("${name}")
  else
    updated+=("${name}")
  fi
  rm -f "${dir}/go.mod.orig" "${dir}/go.sum.orig"
done < <(find . -name go.mod -not -path '*/vendor/*' | sort)

summary_lines=()
summary_lines+=("Bumped \`github.com/confighub/sdk/*\` to \`${version}\`.")
summary_lines+=("")
if [[ ${#updated[@]} -gt 0 ]]; then
  summary_lines+=("Updated (${#updated[@]}):")
  for m in "${updated[@]}"; do summary_lines+=("- \`${m}\`"); done
  summary_lines+=("")
fi
if [[ ${#skipped[@]} -gt 0 ]]; then
  summary_lines+=("Left alone — the bump did not build, so these still need a code change (${#skipped[@]}):")
  for m in "${skipped[@]}"; do summary_lines+=("- ${m}"); done
  summary_lines+=("")
fi
if [[ ${#unchanged[@]} -gt 0 ]]; then
  summary_lines+=("Already current (${#unchanged[@]}): ${unchanged[*]}")
fi

printf '%s\n' "" "${summary_lines[@]}"
if [[ -n "${SUMMARY_FILE:-}" ]]; then
  printf '%s\n' "${summary_lines[@]}" > "${SUMMARY_FILE}"
fi
