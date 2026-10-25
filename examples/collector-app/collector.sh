#!/usr/bin/env bash
#
# collector-app — fact collector.
#
# Invoked by `installer setup` with the package working copy as cwd. The
# installer passes:
#
#   INSTALLER_PACKAGE_DIR   absolute path to this package on disk
#   INSTALLER_INPUT_IMAGE   image to run
#
# Side effect: writes collector-app-secret.env.secret into bases/default/
# so the kustomize secretGenerator there can read it. The file holds a
# random APP_TOKEN; never commit it.
#
# Stdout: a YAML map of facts consumed by the function-chain template:
#
#   configHubURL: https://hub.example.com
#   image:        <INSTALLER_INPUT_IMAGE, passed through>

set -euo pipefail

err() { echo "collector: $*" >&2; exit 1; }

command -v cub >/dev/null || err "cub CLI not found on PATH"

image="${INSTALLER_INPUT_IMAGE:-}"
[ -n "$image" ] || err "INSTALLER_INPUT_IMAGE is required (pass --input image=<registry/name:tag>)"

secret_file="${INSTALLER_PACKAGE_DIR}/bases/default/collector-app-secret.env.secret"
umask 077
printf 'APP_TOKEN=%s\n' "$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')" > "$secret_file"

configHubURL=$(cub context get -o jq=.coordinate.serverURL --quiet)
[ -n "$configHubURL" ] || err "could not read the server URL from the cub context"

cat <<EOT
configHubURL: ${configHubURL}
image: ${image}
EOT
