#!/usr/bin/env bash
#
# ConfigHub worker installer — fact collector.
#
# Invoked by `installer wizard` with the package working copy as cwd. The
# installer passes:
#
#   INSTALLER_PACKAGE_DIR          absolute path to this package on disk
#   INSTALLER_NAMESPACE            value of --namespace (informational; not used here)
#   INSTALLER_INPUT_WORKER_SLUG    BridgeWorker slug in the active cub space
#   INSTALLER_INPUT_SPACE          (optional) cub space override; empty means
#                                  use the active context's default space
#
# Side effect: writes confighub-worker-secret.env.secret into bases/default/
# so the kustomize secretGenerator there can read it. The file holds
# CONFIGHUB_WORKER_ID and CONFIGHUB_WORKER_SECRET; never commit it.
#
# Stdout: a YAML map of facts consumed by the function-chain template:
#
#   bridgeWorkerID: <uuid>
#   configHubURL:   https://hub.example.com
#   image:          ghcr.io/confighubai/confighub-worker:vX.Y.Z

set -euo pipefail

err() { echo "collector: $*" >&2; exit 1; }

command -v cub >/dev/null || err "cub CLI not found on PATH"

worker_slug="${INSTALLER_INPUT_WORKER_SLUG:-}"
[ -n "$worker_slug" ] || err "INSTALLER_INPUT_WORKER_SLUG is required (pass --input worker_slug=<slug>)"

# --space is forwarded to every cub call that takes it, but only when the
# user supplied a non-empty value; otherwise cub uses the active context's
# default space.
space_args=()
if [ -n "${INSTALLER_INPUT_SPACE:-}" ]; then
  space_args=(--space "${INSTALLER_INPUT_SPACE}")
fi

# Find or create the BridgeWorker. `cub worker create --allow-exists` is the
# idempotent path: existing workers are kept; new ones are created on the fly.
cub worker create ${space_args[@]+"${space_args[@]}"} --allow-exists --quiet "$worker_slug" >&2

# `cub worker get-envs --no-export` writes plain KEY=value lines for
# CONFIGHUB_WORKER_ID and CONFIGHUB_WORKER_SECRET — exactly the form
# kustomize's envs file expects. Persist it where bases/default's
# secretGenerator looks for it.
secret_file="${INSTALLER_PACKAGE_DIR}/bases/default/confighub-worker-secret.env.secret"
umask 077
cub worker get-envs --no-export ${space_args[@]+"${space_args[@]}"} "$worker_slug" \
  > "$secret_file"
[ -s "$secret_file" ] || err "cub worker get-envs produced no output for $worker_slug"

# Pull the worker ID back out of the secret file for the bridgeWorkerID fact.
worker_id=$(sed -n 's/^CONFIGHUB_WORKER_ID=//p' "$secret_file")
[ -n "$worker_id" ] || err "CONFIGHUB_WORKER_ID missing from cub worker get-envs output"

# Discover the active context's server URL and the server-version-matched image.
configHubURL=$(cub context get -o jq=.coordinate.serverURL --quiet)
image=$(cub worker get-image)
[ -n "$configHubURL" ] || err "could not read CONFIGHUB_URL from cub context"
[ -n "$image" ] || err "cub worker get-image returned empty"

# Emit facts on stdout for facts.yaml.
cat <<EOF
bridgeWorkerID: ${worker_id}
configHubURL: ${configHubURL}
image: ${image}
EOF
