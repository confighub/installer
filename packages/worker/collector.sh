#!/usr/bin/env bash
#
# ConfigHub worker installer — fact collector.
#
# Invoked by `installer wizard` with the package working copy as cwd. The
# installer passes:
#
#   INSTALLER_PACKAGE_DIR          absolute path to this package on disk
#   INSTALLER_NAMESPACE            value of --namespace (informational; not used here)
#   INSTALLER_INPUT_WORKER_SLUG    BridgeWorker slug
#   INSTALLER_INPUT_SPACE          cub space that holds the BridgeWorker
#   INSTALLER_INPUT_IMAGE          worker image to run (yours; ConfigHub ships none)
#
# Side effect: writes confighub-worker-secret.env.secret into bases/default/
# so the kustomize secretGenerator there can read it. The file holds
# CONFIGHUB_WORKER_ID and CONFIGHUB_WORKER_SECRET; never commit it.
#
# Stdout: a YAML map of facts consumed by the function-chain template:
#
#   bridgeWorkerID: <uuid>
#   configHubURL:   https://hub.example.com
#   image:          <INSTALLER_INPUT_IMAGE, passed through>

set -euo pipefail

err() { echo "collector: $*" >&2; exit 1; }

command -v cub >/dev/null || err "cub CLI not found on PATH"

worker_slug="${INSTALLER_INPUT_WORKER_SLUG:-}"
[ -n "$worker_slug" ] || err "INSTALLER_INPUT_WORKER_SLUG is required (pass --input worker_slug=<slug>)"

space="${INSTALLER_INPUT_SPACE:-}"
[ -n "$space" ] || err "INSTALLER_INPUT_SPACE is required (pass --input space=<slug>); cub has no default space"

image="${INSTALLER_INPUT_IMAGE:-}"
[ -n "$image" ] || err "INSTALLER_INPUT_IMAGE is required (pass --input image=<registry/name:tag>); ConfigHub does not publish a worker image"

# Find or create the BridgeWorker. `cub worker create --allow-exists` is the
# idempotent path: existing workers are kept; new ones are created on the fly.
cub worker create --space "$space" --allow-exists --quiet "$worker_slug" >&2

# `cub worker get-envs --no-export` writes plain KEY=value lines for
# CONFIGHUB_WORKER_ID and CONFIGHUB_WORKER_SECRET — exactly the form
# kustomize's envs file expects. Persist it where bases/default's
# secretGenerator looks for it.
secret_file="${INSTALLER_PACKAGE_DIR}/bases/default/confighub-worker-secret.env.secret"
umask 077
cub worker get-envs --no-export --space "$space" "$worker_slug" \
  > "$secret_file"
[ -s "$secret_file" ] || err "cub worker get-envs produced no output for $worker_slug"

# Pull the worker ID back out of the secret file for the bridgeWorkerID fact.
worker_id=$(sed -n 's/^CONFIGHUB_WORKER_ID=//p' "$secret_file")
[ -n "$worker_id" ] || err "CONFIGHUB_WORKER_ID missing from cub worker get-envs output"

# Discover the active context's server URL.
configHubURL=$(cub context get -o jq=.coordinate.serverURL --quiet)
[ -n "$configHubURL" ] || err "could not read CONFIGHUB_URL from cub context"

# Emit facts on stdout for facts.yaml.
cat <<EOF
bridgeWorkerID: ${worker_id}
configHubURL: ${configHubURL}
image: ${image}
EOF
