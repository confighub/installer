# example-stack

A two-package fixture demonstrating installer dependencies.

- `example-stack` is this package — an app `Deployment` whose container
  reads env vars from a `ConfigMap` that lives in the dep.
- The dep ref is hard-coded to `oci://localhost:5555/installer-e2e/example-base`
  so a single `git clone` exercises the multi-package render pipeline
  without needing to edit YAML. `test/e2e/package-and-deps.sh` starts a
  local `registry:2` on `:5555`, pushes `examples/example-base` to it,
  then drives `wizard → deps update → render → upload`.

To use this example against a different registry, edit
`spec.dependencies[0].package` in `installer.yaml` to point at your own
copy of the `example-base` artifact.
