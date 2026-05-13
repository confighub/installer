# validation/

Machine-readable documentation of the worker container, generated from the
worker image itself. These files are not consumed by `installer render` —
they exist so a consumer (human operator or AI agent) can determine what
env vars the workload accepts and what runtime contract it expects without
re-pulling the image.

| file               | content                                                                     |
| ------------------ | --------------------------------------------------------------------------- |
| `command.yaml`     | Cobra command tree (flags, subcommands, descriptions) emitted by `docgen command`. |
| `env.schema.json`  | JSON Schema of env vars the worker reads, emitted by `docgen env`. Use as input validation when editing the rendered Deployment's `env:`. |
| `runtime.yaml`     | Runtime spec (ports, mount paths, probes) emitted by `docgen runtime`. Use to pick probe paths, expose ports, mount writable scratch. |

## Regenerate

Bump the image tag and re-run:

```bash
IMAGE=ghcr.io/confighubai/confighub-worker:latest
docker run --rm "$IMAGE" docgen command > command.yaml
docker run --rm "$IMAGE" docgen env     > env.schema.json
docker run --rm "$IMAGE" docgen runtime > runtime.yaml
```

The same three commands work against any tag — pin to the tag this package's
`functionChainTemplate` is wired against (defaulted by `cub worker get-image`
to match the server version).
