#!/usr/bin/env bash
#
# upload-reconcile.sh — end-to-end smoke for `installer upload` against a
# live ConfigHub server, driven by the worker package so the test
# exercises BOTH the standard Unit pathway and the AppConfig pathway
# (render-configmap Invocation + AppConfig Unit + placeholder + Upsert
# link).
#
# Flow:
#
#   setup --pull   — pulls worker, runs collector (writes facts), renders
#   pin image      — edits facts.yaml to a known release tag, re-renders
#                    via `installer render` (bypasses setup's collector,
#                    which would overwrite facts back to :latest)
#   make Target    — creates a Target in its own Space so the upload can
#                    bind Units to it via cross-Space <space>/<target>
#   upload         — first upload: Units, the AppConfig set, inferred Links,
#                    and the installer record Unit, with the well-known Space
#                    labels, --space-label/--space-annotation,
#                    --unit-label/--unit-annotation, and --target
#   metadata check — Space labels/annotations, Unit ownership and --unit-*
#                    pairs, the cross-Space TargetID annotation and binding,
#                    and that the AppConfig and record Units are untargeted
#   plan (clean)   — No changes
#   AppConfig edit — edits the .env carrier, re-renders, uploads; the change
#                    lands and is rendered into the placeholder
#   re-run         — an unchanged upload writes nothing
#   add w/o target — a new Unit binds to the Target the Space recorded;
#                    re-passing --environment updates only Environment
#   edit + upload  — a rendered Deployment edit is planned and merged
#   drop manifest  — the Unit is emptied, never deleted: refused without
#                    --yes and a terminal, refused while it carries a
#                    DestroyGate
#   recover        — setup in a fresh work-dir re-enters from the record
#
# Unlike package-and-deps.sh, this test does NOT delete the destination
# Space on exit — the resulting Space is left for manual inspection.
# Clean up afterward with:
#
#   cub space delete --recursive <space-slug>
#
# (Never deletes the `default` Space.)
#
# Full output of every `installer` and `cub` invocation is written to a
# per-step log file under the work-dir so debugging a failure does not
# require re-running the (slow) flow.
#
# Configuration via env vars:
#   INSTALLER_UPLOAD_SPACE  — override the destination Space slug.
#                              Default: installer-test-upload-<YYYYMMDD-HHMMSS>.
#   INSTALLER_UPLOAD_IMAGE  — override the pinned worker image.
#                              Default: ghcr.io/confighubai/confighub-worker:v0.1.44.
#                              Local servers report :latest from
#                              `cub worker get-image` (no release running),
#                              so the test pins to a known release for a
#                              stable rendered image string.
#   INSTALLER_UPLOAD_KEEP_WD=0
#                            — also delete the local work-dir on success
#                              (default 1: keep work-dir for inspection).
#   INSTALLER_UPLOAD_VERBOSE=1
#                            — also mirror every step's output to stderr
#                              while running. Without it, only the
#                              per-step log filenames + summary appear
#                              on stderr.
#
# Requirements:
#   - go and a working build environment
#   - kustomize on PATH
#   - cub on PATH, authenticated against the server you want to test

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK_TMP=
SPACE=${INSTALLER_UPLOAD_SPACE:-installer-test-upload-$(date -u +%Y%m%d-%H%M%S)}
KEEP_WD=${INSTALLER_UPLOAD_KEEP_WD:-1}
VERBOSE=${INSTALLER_UPLOAD_VERBOSE:-0}
WORKER_SLUG=installer-test-worker
PINNED_IMAGE=${INSTALLER_UPLOAD_IMAGE:-ghcr.io/confighubai/confighub-worker:v0.1.44}
# A Target lives in its own Space so the first upload can bind Units to it
# via the cross-Space <space>/<target> --target syntax and record the
# resolved TargetID as $SPACE's "TargetID" annotation.
TARGET_SPACE="${SPACE}-targets"
TARGET_SLUG=installer-test-target
TARGET_WORKER_SLUG=installer-test-target-worker

log()    { printf '\n=== %s ===\n' "$*" >&2; }
note()   { printf '    %s\n' "$*" >&2; }
fail()   { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

# jq helpers: read a single field from a Space/Unit, stripping the quotes
# jq prints around string values (prints "null"/empty when the field is
# absent). Used by the metadata assertions below.
space_jq() { cub space get "$SPACE" -o "jq=$1" 2>/dev/null | tr -d '"'; }
unit_jq()  { cub unit get --space "$SPACE" "$1" -o "jq=$2" 2>/dev/null | tr -d '"'; }

# run <log-name> <cmd> [args...]
# Runs the command, redirecting both stdout and stderr to
# $WORK_TMP/<log-name>.log. With INSTALLER_UPLOAD_VERBOSE=1 mirrors the
# log to the terminal via tee as well. Returns the command's exit code.
run() {
  local logname=$1
  shift
  local logpath="$WORK_TMP/$logname.log"
  note "→ $logname.log: $*"
  if [[ "$VERBOSE" = "1" ]]; then
    "$@" 2>&1 | tee "$logpath"
    return ${PIPESTATUS[0]}
  fi
  "$@" >"$logpath" 2>&1
}

# Guard: refuse to operate on the default Space, ever — even if the
# user overrides INSTALLER_UPLOAD_SPACE.
if [[ "$SPACE" = "default" ]]; then
  fail "INSTALLER_UPLOAD_SPACE must not be 'default'"
fi

cleanup_msg() {
  printf '\n----- inspection state preserved -----\n'
  printf 'Space:    %s\n' "$SPACE"
  printf 'Work-dir: %s\n' "$WORK_TMP"
  printf '\nClean up the Space (and everything in it — Units, Invocations, Links,\nthe BridgeWorker entity, ChangeSets) plus the Target Space with:\n  cub space delete --recursive %s\n  cub space delete --recursive %s\n' "$SPACE" "$TARGET_SPACE"
  if [[ "$KEEP_WD" = "1" ]]; then
    printf 'Clean up the work-dir with:\n  rm -rf %s\n' "$WORK_TMP"
  fi
  printf '\n'
}

cleanup_on_exit() {
  rc=$?
  set +e
  if [[ "$KEEP_WD" != "1" && $rc = 0 && -n "$WORK_TMP" ]]; then
    rm -rf "$WORK_TMP"
  fi
  if [[ -n "$WORK_TMP" ]]; then
    cleanup_msg
  fi
  exit $rc
}
trap cleanup_on_exit EXIT

# 1. Preflight.
log "preflight"
command -v cub >/dev/null 2>&1 || fail "cub not on PATH"
cub space list >/dev/null 2>&1 || fail "cub auth not configured (run \`cub auth login\`)"
command -v kustomize >/dev/null 2>&1 || fail "kustomize not on PATH"
note "cub OK, kustomize OK"
note "destination Space: $SPACE"
note "BridgeWorker slug: $WORKER_SLUG (created in the same Space for clean teardown)"
note "pinned image:      $PINNED_IMAGE"

# Refuse to clobber an existing Space (so re-runs don't accidentally
# pile state on top of an old one). The user can either pick a new
# slug via INSTALLER_UPLOAD_SPACE or delete the old one first.
if cub space list 2>/dev/null | awk '{print $1}' | grep -qx "$SPACE"; then
  fail "Space '$SPACE' already exists. Delete it first (cub space delete --recursive $SPACE) or set INSTALLER_UPLOAD_SPACE to a fresh slug."
fi

# 2. Build.
log "build installer"
( cd "$REPO_ROOT" && go build -o bin/installer ./cmd/installer )
BIN="$REPO_ROOT/bin/installer"

# 3. Pre-create the destination Space so the worker collector can
#    create the BridgeWorker entity in it (--input space=$SPACE).
#    Upload's auto-Space-create would otherwise race with the collector,
#    which runs during setup BEFORE upload.
log "pre-create destination Space"
cub space create --quiet "$SPACE" >/dev/null

# 4. setup --pull against the local worker package. The worker package
#    declares an AppConfig/Env configMapGenerator (confighub-worker-env),
#    so upload will exercise the AppConfig pathway in addition to the
#    standard Unit pathway. The collector runs cub against the active
#    context to create the BridgeWorker and populate facts.
WORK_TMP=$(mktemp -d -t installer-upload-e2e.XXXXXX)
log "setup --pull (worker package, exercises AppConfig)"
run setup "$BIN" setup --pull "$REPO_ROOT/packages/worker" \
  --work-dir "$WORK_TMP" \
  --non-interactive \
  --namespace "$SPACE" \
  --input worker_slug="$WORKER_SLUG" \
  --input space="$SPACE" || fail "setup failed (see $WORK_TMP/setup.log)"

[[ -d "$WORK_TMP/out/manifests" ]] || fail "expected $WORK_TMP/out/manifests/"
[[ -f "$WORK_TMP/out/record/facts.yaml" ]] || fail "expected facts.yaml after setup (worker collector should have run)"

# The worker package's secretGenerator emits the worker secret to
# out/secrets/ (sensitive — never uploaded as a Unit).
[[ -d "$WORK_TMP/out/secrets" ]] || fail "expected $WORK_TMP/out/secrets/ (rendered Secret routed off the upload path)"

# 5. Pin the image to a known release tag by editing facts.yaml and
#    re-rendering via `installer render` (NOT setup, which would re-run
#    the collector and revert image back to whatever the server
#    reports — :latest, locally). This both demonstrates that .Facts
#    is a supported override point and gives us a stable image string
#    to assert against downstream.
log "pin worker image to $PINNED_IMAGE (edit facts.yaml + installer render)"
note "collector-reported image was:"
grep -E '^[[:space:]]+image:' "$WORK_TMP/out/record/facts.yaml" | sed 's/^/      /'
# Use python for a safe in-place YAML update — preserves other fact keys.
python3 - "$WORK_TMP/out/record/facts.yaml" "$PINNED_IMAGE" <<'PY'
import sys, yaml
p, image = sys.argv[1], sys.argv[2]
with open(p) as f:
    doc = yaml.safe_load(f)
doc.setdefault('spec', {}).setdefault('values', {})['image'] = image
with open(p, 'w') as f:
    yaml.safe_dump(doc, f, sort_keys=False)
PY

run render-pinned "$BIN" render --work-dir "$WORK_TMP" || fail "render after facts edit failed (see $WORK_TMP/render-pinned.log)"

# Assert the rendered Deployment now references the pinned image.
DEP_FILE=$(ls "$WORK_TMP/out/manifests"/deployment-*.yaml 2>/dev/null | head -1)
[[ -n "$DEP_FILE" ]] || fail "no rendered Deployment manifest"
grep -q "image: $PINNED_IMAGE" "$DEP_FILE" \
  || fail "rendered Deployment $(basename "$DEP_FILE") does not reference pinned image $PINNED_IMAGE"
note "rendered Deployment now references $PINNED_IMAGE"

# The AppConfig ConfigMap (confighub-worker-env) should be present in
# rendered manifests — upload will split it into a render-configmap
# Invocation + AppConfig Unit + placeholder.
appcfg_cm=$(grep -l "installer.confighub.com/toolchain: AppConfig/Env" "$WORK_TMP/out/manifests"/*.yaml | head -1)
[[ -n "$appcfg_cm" ]] || fail "expected at least one AppConfig-tagged ConfigMap among rendered manifests"
note "AppConfig carrier ConfigMap: $(basename "$appcfg_cm")"

# Confirm the BridgeWorker entity was created by the collector during setup.
cub worker list --space "$SPACE" 2>/dev/null | awk '{print $1}' | grep -qx "$WORKER_SLUG" \
  || fail "collector did not create BridgeWorker '$WORKER_SLUG' in Space $SPACE"

# 5c. Create the cross-Space Target the first upload will bind Units to.
#     It needs no live cluster — upload only binds Units (sets TargetID),
#     it never applies — so a backing worker entity that merely *declares*
#     Kubernetes/YAML support (no running process) plus a default
#     Kubernetes Target with empty parameters is enough. Resolving its
#     UUID up front lets later steps assert the recorded "TargetID"
#     annotation and the per-Unit bindings.
log "create cross-Space Target ($TARGET_SPACE/$TARGET_SLUG)"
cub space create --quiet "$TARGET_SPACE" >/dev/null || fail "failed to create Target Space $TARGET_SPACE"
# A Target needs a BridgeWorker that advertises the ConfigType. We don't
# run a worker here (no cluster), so declare the supported ConfigType on
# the worker entity directly via --from-stdin — enough for target-create
# validation and for binding Units (upload binds, it never applies).
printf '%s' '{"ProvidedInfo":{"BridgeWorkerInfo":{"SupportedConfigTypes":[{"ProviderType":"Kubernetes","ToolchainType":"Kubernetes/YAML"}]}}}' \
  | cub worker create --space "$TARGET_SPACE" --from-stdin "$TARGET_WORKER_SLUG" >/dev/null \
  || fail "failed to create Target worker $TARGET_SPACE/$TARGET_WORKER_SLUG"
cub target create --space "$TARGET_SPACE" "$TARGET_SLUG" '{}' "$TARGET_WORKER_SLUG" >/dev/null \
  || fail "failed to create Target $TARGET_SPACE/$TARGET_SLUG"
TARGET_ID=$(cub target get --space "$TARGET_SPACE" "$TARGET_SLUG" -o jq=.Target.TargetID 2>/dev/null | tr -d '"')
[[ -n "$TARGET_ID" && "$TARGET_ID" != "null" ]] || fail "could not resolve TargetID for $TARGET_SPACE/$TARGET_SLUG"
note "Target $TARGET_SPACE/$TARGET_SLUG → TargetID $TARGET_ID"

# 6. First upload — creates the Units, the AppConfig set (render-configmap
#    Invocation, AppConfig Unit, placeholder, Upsert Link), inferred Links,
#    and the installer record, carrying the well-known Space labels
#    (Component via --component), the free-form --space-label /
#    --space-annotation pairs, the --unit-label / --unit-annotation pairs on
#    every Unit, and binding new Units to the cross-Space Target (whose
#    TargetID is recorded as a Space annotation). Every upload names the
#    Space with --space: nothing local records it.
log "installer upload --space $SPACE (first upload — AppConfig pathway + Space/Unit metadata + cross-Space --target)"
run upload-first "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" \
  --component my-component \
  --layer App \
  --environment Prod \
  --region us-east1 \
  --owner Engineering \
  --space-label tier=infra \
  --space-annotation note=e2e \
  --unit-label managed-by=installer-e2e \
  --unit-annotation install-note=hello \
  --target "$TARGET_SPACE/$TARGET_SLUG" \
  || fail "first upload failed (see $WORK_TMP/upload-first.log)"
grep -qE "^Applied: [1-9][0-9]* created, 0 updated, 0 emptied, 0 revived, 0 adopted\\.$" "$WORK_TMP/upload-first.log" \
  || fail "first upload should only create Units (see $WORK_TMP/upload-first.log)"
[[ ! -e "$WORK_TMP/out/record/upload.yaml" ]] || fail "upload must not write a local upload.yaml"

# 6a. Standard-Unit assertions. Every resource is its own Unit, named for the
#     resource: the worker Deployment keeps its bare name.
unit_count=$(cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1' | wc -l | tr -d ' ')
note "Units in $SPACE after first upload: $unit_count"
[[ "$unit_count" -ge 6 ]] || fail "expected at least 6 Units in $SPACE, got $unit_count"
cub unit get --space "$SPACE" "$WORKER_SLUG" >/dev/null 2>&1 \
  || fail "expected the worker Deployment's Unit to be named $WORKER_SLUG"

# 6b. The installer record: one untargeted AppConfig/YAML Unit, a single
#     document using the configHub schema paths.
[[ "$(unit_jq installer .Unit.ToolchainType)" == "AppConfig/YAML" ]] || fail "installer record Unit should be AppConfig/YAML"
[[ "$(unit_jq installer .Unit.Annotations.UploadResource)" == "AppConfig/YAML/installer" ]] || fail "installer record Unit should be keyed AppConfig/YAML/installer"
rec_tid=$(unit_jq installer .Unit.TargetID)
[[ -z "$rec_tid" || "$rec_tid" == "null" ]] || fail "installer record Unit should have no Target, got '$rec_tid'"
cub unit data --space "$SPACE" installer > "$WORK_TMP/installer-record.txt"
grep -q "^  configSchema: InstallerRecord$" "$WORK_TMP/installer-record.txt" || fail "installer record lacks configHub.configSchema (see $WORK_TMP/installer-record.txt)"
grep -q "^apiVersion:" "$WORK_TMP/installer-record.txt" && fail "installer record should not be a KRM document (see $WORK_TMP/installer-record.txt)"
grep -q "^  namespace: $SPACE$" "$WORK_TMP/installer-record.txt" || fail "installer record should carry the inputs' namespace"
note "installer record: AppConfig/YAML, configSchema InstallerRecord, untargeted"

# 6c. AppConfig-pathway assertions: one render-configmap Invocation, one
#     *-rendered placeholder holding the rendered ConfigMap in the install
#     namespace, and an untargeted AppConfig Unit.
invocation_count=$(cub invocation list --space "$SPACE" 2>/dev/null | awk 'NR>1 && /-render/' | wc -l | tr -d ' ')
[[ "$invocation_count" -ge 1 ]] || fail "expected at least one render-configmap Invocation in $SPACE (got $invocation_count)"
cub unit data --space "$SPACE" confighub-worker-env-rendered 2>/dev/null > "$WORK_TMP/rendered.txt"
grep -q "kind: ConfigMap" "$WORK_TMP/rendered.txt" || fail "placeholder confighub-worker-env-rendered should contain a rendered ConfigMap"
grep -q "^  namespace: $SPACE$" "$WORK_TMP/rendered.txt" || fail "rendered ConfigMap should be in namespace $SPACE (see $WORK_TMP/rendered.txt)"
appcfg_tid=$(unit_jq confighub-worker-env .Unit.TargetID)
[[ -z "$appcfg_tid" || "$appcfg_tid" == "null" ]] || fail "AppConfig Unit confighub-worker-env should have no Target, got '$appcfg_tid'"
note "rendered ConfigMap present in the placeholder, in namespace $SPACE; AppConfig Unit untargeted"

link_count=$(cub link list --space "$SPACE" 2>/dev/null | awk 'NR>1' | wc -l | tr -d ' ')
[[ "$link_count" -ge 2 ]] || fail "expected inferred Links plus the Upsert Link in $SPACE"
note "Links in $SPACE: $link_count"

# 6d. Metadata assertions.
log "metadata assertions: Space labels/annotations, Unit labels/annotations, TargetID round-trip"
[[ "$(space_jq .Space.Labels.Component)"   == "my-component" ]] || fail "Space label Component != my-component (got '$(space_jq .Space.Labels.Component)')"
[[ "$(space_jq .Space.Labels.Variant)"     == "base" ]]         || fail "Space label Variant != base (the default)"
[[ "$(space_jq .Space.Labels.Namespace)"   == "$SPACE" ]]       || fail "Space label Namespace != $SPACE (the install namespace)"
[[ "$(space_jq .Space.Labels.Layer)"       == "App" ]]          || fail "Space label Layer != App"
[[ "$(space_jq .Space.Labels.Environment)" == "Prod" ]]         || fail "Space label Environment != Prod"
[[ "$(space_jq .Space.Labels.Region)"      == "us-east1" ]]     || fail "Space label Region != us-east1"
[[ "$(space_jq .Space.Labels.Owner)"       == "Engineering" ]]  || fail "Space label Owner != Engineering"
[[ "$(space_jq .Space.Labels.tier)"        == "infra" ]]        || fail "Space label tier != infra (--space-label)"
[[ "$(space_jq '.Space.Annotations.note')" == "e2e" ]]          || fail "Space annotation note != e2e (--space-annotation)"
[[ "$(space_jq '.Space.Annotations.TargetID')" == "$TARGET_ID" ]] || fail "Space TargetID annotation != $TARGET_ID (got '$(space_jq '.Space.Annotations.TargetID')')"
note "Space carries Component=my-component, Variant=base, Namespace, Layer/Environment/Region/Owner, tier, note, TargetID"

[[ "$(unit_jq "$WORKER_SLUG" .Unit.Labels.UploadSource)" == "confighub-worker" ]] || fail "Unit $WORKER_SLUG should be owned by the package (UploadSource=confighub-worker)"
[[ "$(unit_jq "$WORKER_SLUG" '.Unit.Labels["managed-by"]')" == "installer-e2e" ]] || fail "Unit $WORKER_SLUG managed-by label != installer-e2e (--unit-label)"
[[ "$(unit_jq "$WORKER_SLUG" '.Unit.Annotations["install-note"]')" == "hello" ]] || fail "Unit $WORKER_SLUG install-note annotation != hello (--unit-annotation)"
[[ "$(unit_jq "$WORKER_SLUG" .Unit.TargetID)" == "$TARGET_ID" ]] || fail "Unit $WORKER_SLUG TargetID != $TARGET_ID (cross-Space --target binding)"
note "Unit $WORKER_SLUG: UploadSource=confighub-worker, managed-by=installer-e2e, install-note=hello, TargetID=$TARGET_ID"

# 7. plan against the unchanged work-dir → No changes.
log "installer plan (clean) — expect No changes"
run plan-clean "$BIN" plan --work-dir "$WORK_TMP" --space "$SPACE" || fail "plan failed (see $WORK_TMP/plan-clean.log)"
grep -q "^No changes\\.$" "$WORK_TMP/plan-clean.log" \
  || fail "plan against just-uploaded work-dir should report No changes (see $WORK_TMP/plan-clean.log)"

# 8. AppConfig round-trip: change a value in the env carrier, re-render via
#    installer render (NOT setup, which would re-run the collector and
#    revert the pinned image), then upload. The change lands in the AppConfig
#    Unit and is rendered into the placeholder; the placeholder itself is
#    never a planned write.
log "AppConfig round-trip: edit the env carrier + render + upload"
appcfg_in_pkg="$WORK_TMP/package/bases/default/confighub-worker.env"
[[ -f "$appcfg_in_pkg" ]] || fail "expected $appcfg_in_pkg in pulled worker package"
sed -i.bak 's/^CONFIGHUB_WORKER_HTTP_SERVER_PORT=.*/CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093/' "$appcfg_in_pkg"
rm -f "$appcfg_in_pkg.bak"
grep -q '^CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093$' "$appcfg_in_pkg" \
  || fail "failed to flip port in $appcfg_in_pkg"

run render-appcfg "$BIN" render --work-dir "$WORK_TMP" || fail "render after AppConfig edit failed (see $WORK_TMP/render-appcfg.log)"

run plan-appcfg "$BIN" plan --work-dir "$WORK_TMP" --space "$SPACE" || fail "plan after AppConfig edit failed (see $WORK_TMP/plan-appcfg.log)"
grep -q "^Plan: 0 to create, 1 to update, 0 to empty, 0 to revive, 0 to adopt\\.$" "$WORK_TMP/plan-appcfg.log" \
  || fail "plan after AppConfig edit should report exactly 1 update (see $WORK_TMP/plan-appcfg.log)"
grep -qE "^  Update +confighub-worker-env$" "$WORK_TMP/plan-appcfg.log" \
  || fail "plan after AppConfig edit should name the AppConfig Unit confighub-worker-env (see $WORK_TMP/plan-appcfg.log)"

run upload-appcfg "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" --yes || fail "upload after AppConfig edit failed (see $WORK_TMP/upload-appcfg.log)"
grep -q "^Applied: 0 created, 1 updated, 0 emptied, 0 revived, 0 adopted\\.$" "$WORK_TMP/upload-appcfg.log" \
  || fail "upload after AppConfig edit should report exactly 1 update (see $WORK_TMP/upload-appcfg.log)"

cub unit data --space "$SPACE" confighub-worker-env > "$WORK_TMP/confighub-worker-env.after.txt"
grep -q "^CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093$" "$WORK_TMP/confighub-worker-env.after.txt" \
  || fail "AppConfig Unit confighub-worker-env did not pick up the new PORT (see $WORK_TMP/confighub-worker-env.after.txt)"
rendered_ok=
for _ in $(seq 1 30); do
  if cub unit data --space "$SPACE" confighub-worker-env-rendered 2>/dev/null | grep -q 'CONFIGHUB_WORKER_HTTP_SERVER_PORT: "9093"'; then
    rendered_ok=1; break
  fi
  sleep 1
done
[[ -n "$rendered_ok" ]] || fail "placeholder confighub-worker-env-rendered was not re-rendered with the new PORT"
note "AppConfig Unit and its rendered ConfigMap now have CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093"

# 9. Upload again — converges; a bundle with an AppConfig carrier writes nothing.
log "installer upload (unchanged) — re-run is a no-op"
run upload-converge-1 "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" || fail "converge upload failed (see $WORK_TMP/upload-converge-1.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-converge-1.log" \
  || fail "upload of an unchanged work-dir should be No changes (see $WORK_TMP/upload-converge-1.log)"
grep -q " link " "$WORK_TMP/upload-converge-1.log" \
  && fail "upload of an unchanged work-dir should not touch Links (see $WORK_TMP/upload-converge-1.log)"

# 9b. An added resource without --target binds to the Target the Space
#     recorded. Re-passing --environment updates that label; the labels not
#     re-passed, --component's included, and the TargetID annotation stay.
log "add without --target: binds from the Space's TargetID; --environment re-pass updates only Environment"
EXTRA_NAME=extra-e2e-config
cat > "$WORK_TMP/out/manifests/configmap-$EXTRA_NAME.yaml" <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: $EXTRA_NAME
  namespace: $SPACE
data:
  hello: world
EOF
EXTRA_SLUG="$EXTRA_NAME-configmap"

run upload-readback "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" --yes --environment Staging \
  || fail "add without --target failed (see $WORK_TMP/upload-readback.log)"
grep -q "^Applied: 1 created, 0 updated, 0 emptied, 0 revived, 0 adopted\\.$" "$WORK_TMP/upload-readback.log" \
  || fail "add should report exactly 1 created (see $WORK_TMP/upload-readback.log)"

extra_tid=$(unit_jq "$EXTRA_SLUG" .Unit.TargetID)
[[ "$extra_tid" == "$TARGET_ID" ]] || fail "added Unit $EXTRA_SLUG TargetID = '$extra_tid', want $TARGET_ID (read back from the Space annotation)"
note "added Unit $EXTRA_SLUG bound to TargetID $TARGET_ID without --target"

[[ "$(space_jq .Space.Labels.Environment)" == "Staging" ]]       || fail "Environment label != Staging (re-passed value should update)"
[[ "$(space_jq .Space.Labels.Component)"   == "my-component" ]]  || fail "Component label changed; it should stay unless --component is given"
[[ "$(space_jq .Space.Labels.Layer)"       == "App" ]]           || fail "Layer label changed; it was not re-passed"
[[ "$(space_jq .Space.Labels.Region)"      == "us-east1" ]]      || fail "Region label changed; it was not re-passed"
[[ "$(space_jq '.Space.Annotations.TargetID')" == "$TARGET_ID" ]] || fail "TargetID annotation changed across an upload without --target"
note "Environment→Staging; Component/Layer/Region and TargetID preserved"

# 10. Edit a rendered Kubernetes manifest (the Deployment) → plan names it.
#     The marker is injected into out/manifests/ after render and survives
#     because nothing re-renders before the upload.
log "edit rendered Deployment, plan surfaces the update"
python3 - "$DEP_FILE" <<'PY'
import sys, yaml
p = sys.argv[1]
with open(p) as f:
    docs = list(yaml.safe_load_all(f))
for d in docs:
    if isinstance(d, dict) and isinstance(d.get('metadata'), dict):
        labels = d['metadata'].setdefault('labels', {})
        labels['installer-test-marker'] = 'true'
        break
with open(p, 'w') as f:
    yaml.safe_dump_all(docs, f, default_flow_style=False, sort_keys=False)
PY

run plan-edited "$BIN" plan --work-dir "$WORK_TMP" --space "$SPACE" || fail "plan after edit failed (see $WORK_TMP/plan-edited.log)"
grep -q "^Plan: 0 to create, 1 to update, 0 to empty, 0 to revive, 0 to adopt\\.$" "$WORK_TMP/plan-edited.log" \
  || fail "plan after edit should report exactly 1 update (see $WORK_TMP/plan-edited.log)"
grep -qE "^  Update +$WORKER_SLUG$" "$WORK_TMP/plan-edited.log" \
  || fail "plan after edit should name $WORKER_SLUG (see $WORK_TMP/plan-edited.log)"

# 11. Upload — merges the edit.
log "installer upload (Deployment marker) — applies 1 update"
run upload-edited "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" --yes || fail "upload failed (see $WORK_TMP/upload-edited.log)"
grep -q "^Applied: 0 created, 1 updated, 0 emptied, 0 revived, 0 adopted\\.$" "$WORK_TMP/upload-edited.log" \
  || fail "upload should apply 1 update (see $WORK_TMP/upload-edited.log)"
cub unit data --space "$SPACE" "$WORKER_SLUG" > "$WORK_TMP/deployment.after.txt"
grep -q "installer-test-marker" "$WORK_TMP/deployment.after.txt" \
  || fail "Deployment Unit is missing the injected marker label (see $WORK_TMP/deployment.after.txt)"
note "Deployment Unit now has the installer-test-marker label"

# 12. Upload again — converges.
log "installer upload (unchanged) — re-run is a no-op"
run upload-converge-2 "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" || fail "converge upload failed (see $WORK_TMP/upload-converge-2.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-converge-2.log" \
  || fail "upload of an unchanged work-dir should be No changes (see $WORK_TMP/upload-converge-2.log)"

# 13. Resource removal: drop a rendered manifest. Its Unit is emptied, never
#     deleted, and the upload asks first. The ClusterRoleBinding is a safe
#     leaf to drop (cluster-scoped, nothing needs it).
log "resource removal: drop a rendered manifest; its Unit is emptied, not deleted"
DEL_FILE=$(ls "$WORK_TMP/out/manifests"/clusterrolebinding-*.yaml 2>/dev/null | head -1)
[[ -n "$DEL_FILE" ]] || fail "no rendered ClusterRoleBinding manifest to drop"
rm -f "$DEL_FILE"

run plan-deleted "$BIN" plan --work-dir "$WORK_TMP" --space "$SPACE" || fail "plan after manifest drop failed (see $WORK_TMP/plan-deleted.log)"
grep -q "^Plan: 0 to create, 0 to update, 1 to empty, 0 to revive, 0 to adopt\\.$" "$WORK_TMP/plan-deleted.log" \
  || fail "plan after manifest drop should report exactly 1 empty (see $WORK_TMP/plan-deleted.log)"
DEL_SLUG=$(awk '$1 == "Empty" {print $2; exit}' "$WORK_TMP/plan-deleted.log")
[[ -n "$DEL_SLUG" ]] || fail "plan after manifest drop should name the Unit it empties (see $WORK_TMP/plan-deleted.log)"
note "the upload would empty $DEL_SLUG"

# Without a terminal to confirm on, an upload that empties refuses without --yes.
if run upload-unconfirmed "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" </dev/null; then
  fail "upload must refuse to empty Units without --yes and without a terminal (see $WORK_TMP/upload-unconfirmed.log)"
fi
grep -q "pass --yes" "$WORK_TMP/upload-unconfirmed.log" \
  || fail "the refusal should name --yes (see $WORK_TMP/upload-unconfirmed.log)"

# 13a. DestroyGate refusal: a gated Unit is never emptied, and the upload
#      writes nothing.
log "DestroyGate refusal: gated Unit must not be emptied"
cub unit update --patch --space "$SPACE" --destroy-gate "installer-e2e" "$DEL_SLUG" >/dev/null \
  || fail "failed to set DestroyGate on $DEL_SLUG"
if run upload-gated "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" --yes; then
  fail "upload must refuse to empty $DEL_SLUG while it carries a DestroyGate (see $WORK_TMP/upload-gated.log)"
fi
grep -q "DestroyGates" "$WORK_TMP/upload-gated.log" \
  || fail "the refusal should mention DestroyGates (see $WORK_TMP/upload-gated.log)"
cub unit data --space "$SPACE" "$DEL_SLUG" 2>/dev/null | grep -q "kind: ClusterRoleBinding" \
  || fail "gated Unit $DEL_SLUG must remain intact after the refusal"
note "gated Unit $DEL_SLUG left untouched"

# 13b. Clear the gate and upload: the Unit is emptied, not deleted.
log "clear the gate; upload empties the Unit"
cub unit update --patch --space "$SPACE" --destroy-gate "installer-e2e=-" "$DEL_SLUG" >/dev/null \
  || fail "failed to remove DestroyGate from $DEL_SLUG"
run upload-emptied "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" --yes || fail "upload empty failed (see $WORK_TMP/upload-emptied.log)"
grep -q "^Applied: 0 created, 0 updated, 1 emptied, 0 revived, 0 adopted\\.$" "$WORK_TMP/upload-emptied.log" \
  || fail "upload should report exactly 1 emptied (see $WORK_TMP/upload-emptied.log)"
cub unit get --space "$SPACE" "$DEL_SLUG" >/dev/null 2>&1 \
  || fail "emptied Unit $DEL_SLUG must still exist — upload never deletes a Unit"
if cub unit data --space "$SPACE" "$DEL_SLUG" 2>/dev/null | grep -q "kind: ClusterRoleBinding"; then
  fail "emptied Unit $DEL_SLUG should no longer contain the ClusterRoleBinding"
fi
note "Unit $DEL_SLUG still exists and no longer contains the ClusterRoleBinding"

# 14. A fresh work-dir recovers the install from the installer record: setup
#     with no --namespace or --input re-enters the prior choices. --space names
#     the install, since the organization may hold other installs of the worker.
log "setup in a fresh work-dir recovers prior state from the installer record"
RECOVER_WD="$WORK_TMP/recovered"
run setup-recover "$BIN" setup --pull "$REPO_ROOT/packages/worker" --work-dir "$RECOVER_WD" --non-interactive --space "$SPACE" \
  || fail "setup in a fresh work-dir failed (see $WORK_TMP/setup-recover.log)"
grep -q "Loaded prior install state from confighub" "$WORK_TMP/setup-recover.log" \
  || fail "setup should load prior state from ConfigHub (see $WORK_TMP/setup-recover.log)"
grep -q "^  namespace: $SPACE$" "$RECOVER_WD/out/record/inputs.yaml" \
  || fail "recovered inputs should carry namespace $SPACE (see $RECOVER_WD/out/record/inputs.yaml)"
note "fresh work-dir recovered namespace and inputs from the installer record"

log "summary"
note "Space:           $SPACE"
note "BridgeWorker:    $WORKER_SLUG (in Space $SPACE)"
note "Worker image:    $PINNED_IMAGE (pinned via facts.yaml override)"
note "Work-dir:        $WORK_TMP"
note ""
note "Final Unit list:"
cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}'
note ""
note "Final Link list:"
cub link list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}' || true

log "OK"
