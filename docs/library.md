# Embedding the installer as a Go library — Design and plan

Status: phases 1–3 are done and released in v0.4.0, with the CI job of
phase 4. Sending the logs of the functions a render runs to `ErrOut`
came after v0.4.0. Whether cub embeds the installer is undecided (see
phase 4).

The installer runs as a binary, standalone (`installer ...`) or as a cub
plugin (`cub installer ...`). This doc proposes a public Go package that
lets another program run the install lifecycle (pull, wizard, render,
upload) in its own process, with the same behavior as the CLI, and the
changes needed to get there. Phases are ordered so each leaves the
installer working end-to-end with the existing examples and e2e scripts.

## Motivation

`cub cluster up --k8sbot-installer` installs k8sbot's shared base Space
from k8sbot's installer package. It does so by finding the installer cub
plugin and running it twice as a subprocess, `setup --pull <ref> --space
<base> --non-interactive ...` and then `upload --space <base> --variant
<variant> --yes`, streaming its output to the terminal. That works, with
these limits:

- **The plugin has to be installed separately**, with `cub plugin install
  confighub/installer`, and cub has to find the plugin's own entrypoint
  rather than an `installer` on `PATH` (on macOS that is the system's
  package installer).
- **The version is whatever the user installed.** cub can't require
  installer behavior newer than the user's plugin. The base install, for
  example, relies on `setup --space` treating a Space that doesn't exist
  yet as a fresh install, which first shipped in v0.3.1.
- **The interface is flags, text, and an exit status.** cub can't see what
  the wizard chose, which Units the upload created or emptied, or whether
  anything changed, except by reading ConfigHub again afterwards. Errors
  arrive as an exit status after the installer has printed them.

Linking the installer into cub removes all three. The same applies to any
other Go program that installs packages as one step of a larger workflow,
such as the manager tools built on the SDK's `managerkit`.

## Goals

- A public package, `github.com/confighub/installer/pkg/installer`, that
  runs setup (pull, prior-state re-entry, wizard, dependency lock,
  render, rendered OCI) and upload (plan, apply) with the same results as
  the CLI given the same inputs.
- The host program controls everything that is process-wide today: where
  output goes, whether anything prompts, the ConfigHub client, registry
  credentials and plain-HTTP access, the signing policy, the cosign
  binary, and files written outside the work-dir.
- Results come back as values: the selection, inputs, facts, lock and
  rendered files from setup, and each Space's `UploadResult` from upload.
- The installer CLI is rebuilt on the package, so there is one
  implementation. The cub plugin is unaffected.

## Non-goals

- **Exporting the cobra command tree.** A host that wants all of `cub
  installer` already gets it through the plugin protocol. Mounting the
  command tree in cub would tie cub's release to every installer flag.
- **An in-memory work-dir.** The work-dir layout (`package/`, `out/`,
  `out/record/`, `out/compose/`) is the installer's state and stays on
  disk. A host uses a temporary directory, as `cub cluster up` does now.
- **Authoring and publishing.** `init`, `new`, `edit`, `vet`, `build`,
  `push`, `tag`, `sign` and `verify` stay CLI-only for now. They can be
  added to the package when a host needs them.
- **API stability beyond the module's.** The module is v0. Changes to the
  package are called out in release notes.

## What prevents embedding today

1. **Only `pkg/api` is importable.** Go doesn't allow another module to
   import `internal/...`, and everything except the package schema in
   `pkg/api` is under `internal/`.

2. **The lifecycle lives in `internal/cli`.** The packages below it (`pkg`,
   `wizard`, `deps`, `render`, `upload`) each do one step. The steps are
   put together in cobra command code, which takes flag structs:
   - `runFlow` (`internal/cli/setup.go`), shared by `setup` and `wizard`:
     resolve the source digest for `--output-oci`, recover the pull
     reference from the installer record in `--space`, pull, load, read
     prior state (work-dir, then the installer record), build answers for
     a fresh install or an upgrade, run the wizard, update the lock, render
     the package and its dependencies, publish the rendered OCI artifact.
   - `prepareUpload` (`internal/cli/upload.go`), shared by `upload` and
     `plan`: validate labels and annotations, discover the packages, look
     up each Space, keep an existing Space's Component and Variant labels
     unless they are given, resolve the Target and ChangeSet, build one
     request per package. The `upload` command then previews the requests
     as dry runs, asks before emptying Units, sends them, hides each
     installer record Unit, and records the install in the user state
     file.

3. **Render runs the transformer by executing the current binary.** Render
   runs kustomize in-process (`krusty`), but the function chain runs as a
   kustomize exec plugin: render writes `out/compose/installer-transformer.sh`,
   which runs `"<os.Executable()>" transformer`, and kustomize runs that
   script. In a host, `os.Executable()` is the host, which has no
   `transformer` subcommand. The transformer's implementation
   (`runTransformer` in `internal/cli/transformer.go`, and the AppConfig
   handling in `internal/cli/appconfig.go`) is also in the cli package.

4. **Output goes to the process's stdout and stderr.** Progress lines are
   `fmt.Printf` calls throughout the flow, warnings go to `os.Stderr`, and
   the collector's and cosign's stderr are the process's. Follow-up hints
   ("Next: installer render --work-dir ...") name the command from
   `CUB_PLUGIN` and `os.Args[0]`.

5. **Prompting is decided from the process's stdin.** The wizard prompts
   when stdin is a terminal and `--non-interactive` isn't given, and the
   upload's confirmation before emptying Units reads `os.Stdin`.

6. **The ConfigHub client is a package global**, `var confighub =
   cubapi.MemoizedClient{UserAgent: "installer"}`, which loads the cub
   environment and configuration itself. `internal/cubctx` reads the cub
   configuration again for the organization ID.

7. **Settings come from the environment and the home directory:**
   - `ORAS_OCI_INSECURE=1` turns on plain HTTP for registries.
   - Registry credentials come from the Docker credential store.
   - Pull and dependency resolution enforce the signing policy in
     `INSTALLER_SIGNING_POLICY`, or `~/.config/installer/policy.yaml`, by
     running `INSTALLER_COSIGN_BIN`, or `cosign` on `PATH`.
   - Upload writes `~/.confighub/installer/state.yaml`, which `installer
     new` reads.

8. **The version is set at link time.** `internal/version.Version` is set
   with `-ldflags` when the installer binary is built, and reads `dev`
   otherwise. It is sent as the upload request's `ClientVersion` and
   stamped into pushed and rendered OCI artifacts.

9. **Errors name CLI flags**, such as "pass --pull <ref> to fetch one",
   "pass --space to choose one", and
   "--variant must not be empty". A host has different flags, or none.

Already fine: nothing outside `cmd/` exits the process or panics;
function chains and validators run in-process through `chainexec`;
`wizard.LoadPriorState` already takes a record fetcher and a warning
callback; `wizard.Ask` already takes its terminal streams; `upload.Report`
already writes to an `io.Writer`.

## Design

### One public package

`pkg/installer` is the only new public package, next to `pkg/api`. The
step packages stay internal, so they can keep changing without breaking
hosts. The package uses `pkg/api` types for the documents it reads and
writes (`Package`, `Selection`, `Inputs`, `Facts`, `Lock`) and the SDK's
`cubapi` and `goclient-new` types for ConfigHub.

### API

```go
package installer

// Options configure an Installer. The zero value prints nothing, never
// prompts, and uses the Docker credential store and no signing policy.
type Options struct {
	// ConfigHub is the client for reading installer records and uploading.
	// Without it, Setup re-enters only from the work-dir, and fails with
	// ErrNoConfigHub when SetupOptions.Space needs a record; uploads always
	// need it, and verify its session first (ErrNotAuthenticated).
	ConfigHub *cubapi.Client
	// Out receives progress lines; nil discards them.
	Out io.Writer
	// ErrOut receives warnings, unless Warn is set, the stderr of the
	// package's collector and of cosign, and a line for each function a
	// render runs; nil discards them.
	ErrOut io.Writer
	// Warn receives each warning, such as an installer record that can't be
	// read, after which setup starts fresh; nil prints "warning: <err>" to
	// ErrOut. The error may be a sentinel, for the host to add its remedy.
	Warn func(error)
	// Registry configures access to OCI registries.
	Registry RegistryOptions
	// SigningPolicy is enforced on every package pulled, including
	// dependencies. Nil enforces none. LoadSigningPolicy reads the file
	// the CLI uses.
	SigningPolicy *api.SigningPolicy
	// CosignBinary runs signature verification; "" means cosign on PATH.
	CosignBinary string
	// ClientVersion is sent with uploads and stamped into rendered OCI
	// artifacts; "" means the installer module's version from the build.
	ClientVersion string
	// TransformerCommand runs kustomize's transform step; nil re-executes
	// the current binary, which must call RunTransformerIfRequested.
	TransformerCommand []string
}

type RegistryOptions struct {
	// Credentials authenticates to registries; nil uses the Docker
	// credential store.
	Credentials credentials.Store
	// PlainHTTP uses HTTP for every registry. localhost and 127.0.0.1
	// always use HTTP.
	PlainHTTP bool
	// PackageRepository is where a bare package name, <name>[:<version>],
	// resolves: a mirror of ConfigHub's packages, say. "" is
	// api.PackageRepository, which is read anonymously.
	PackageRepository string
}

func New(opts Options) (*Installer, error)

// LoadSigningPolicy reads the signing policy at path, or at
// ~/.config/installer/policy.yaml when path is "". It returns nil when
// the file doesn't exist.
func LoadSigningPolicy(path string) (*api.SigningPolicy, error)

type SetupOptions struct {
	WorkDir   string            // created if missing
	Pull      string            // <name>[:<version>], oci://<ref>, or a path; "" uses <WorkDir>/package
	Space     string            // re-enter the install whose installer record is in this Space
	Base      string            // "" means the package's default base
	Namespace string
	Select    []string
	Preset    string
	Inputs    map[string]string
	SetImage  map[string]string // image name → reference
	Reuse     bool              // take prior choices without prompting
	Terminal  *Terminal         // prompt on these streams; nil never prompts
	Render    bool              // also lock dependencies and render
	OutputOCI string            // also publish the render as a rendered OCI artifact
}

type Terminal struct {
	In  interface{ io.Reader; Fd() uintptr }
	Out interface{ io.Writer; Fd() uintptr }
	Err io.Writer
}

type SetupResult struct {
	Package     *api.Package
	PriorSource PriorSource // none, local, or confighub
	Selection   *api.Selection
	Inputs      *api.Inputs
	Facts       *api.Facts    // nil when the package has no collector
	Lock        *api.Lock     // nil when the package has no dependencies
	Render      *RenderResult // nil unless SetupOptions.Render
	RenderedOCI *RenderedOCI  // nil unless SetupOptions.OutputOCI
}

func (i *Installer) Setup(ctx context.Context, opts SetupOptions) (*SetupResult, error)

// Setup resolves a bare package name in SetupOptions.Pull itself, so a host
// passes the name it was given through, and gets the same packages the
// installer CLI does. api.PackageRepository is where names resolve unless
// RegistryOptions.PackageRepository names another.

// Render renders the work-dir's package from the selection and inputs in
// out/record, as `installer render` does.
func (i *Installer) Render(ctx context.Context, workDir string) (*RenderResult, error)

type UploadOptions struct {
	WorkDir      string
	Space        string // only for a package with no dependencies
	SpacePattern string // "" means {{.PackageName}}
	Target       string
	ChangeSet    string
	// Component and Variant label the Spaces. "" keeps the label an
	// existing Space has, or else uses the package name and "base".
	Component, Variant                        string
	Layer, Environment, Region, Owner         string
	SpaceLabels, SpaceAnnotations             map[string]string
	UnitLabels, UnitAnnotations               map[string]string
}

// PrepareUpload builds one upload request per package. It reads ConfigHub
// (Spaces, Components, the Target and ChangeSet) and writes nothing.
func (i *Installer) PrepareUpload(ctx context.Context, opts UploadOptions) (*UploadPlan, error)

// PreviewUpload runs each request as a dry run and records the result in
// its package's Preview; Mutations asks for the paths each write changes.
func (i *Installer) PreviewUpload(ctx context.Context, plan *UploadPlan, opts PreviewOptions) error

// PlanUpload is PrepareUpload, then PreviewUpload with Mutations, as
// `installer plan` does. It writes nothing.
func (i *Installer) PlanUpload(ctx context.Context, opts UploadOptions) (*UploadPlan, error)

type UploadPlan struct {
	Packages []PlannedPackage // each locked dependency, then the parent
}

// Emptied lists the Units the previewed upload would empty, as <space>/<unit>.
func (p *UploadPlan) Emptied() []string

// Upload sends the plan's requests and hides each installer record Unit.
// A failed write in one Space doesn't stop the others; UploadResult.Failed
// reports it.
func (i *Installer) Upload(ctx context.Context, plan *UploadPlan) (*UploadResult, error)

type UploadResult struct {
	Packages []UploadedPackage // each with its Space, its goclientnew.UploadResult, and its ChangeSet
}

func (r *UploadResult) Failed() bool
func (r *UploadResult) Changed() bool

// Install is Setup with rendering, then PrepareUpload and Upload, having
// verified the ConfigHub session before pulling. opts.Space is required: the
// installer record there is re-entered and the package uploads to it, so an
// install never re-enters one Space and uploads to another. With
// ConfirmEmpty set, it previews the upload and asks before emptying Units.
// It does not release.
func (i *Installer) Install(ctx context.Context, opts InstallOptions) (*InstallResult, error)

type InstallOptions struct {
	Space        string        // replaces Setup.Space and Upload.Space
	Setup        SetupOptions
	Upload       UploadOptions // its WorkDir is Setup's
	ConfirmEmpty func(units []string) error
}

type InstallResult struct {
	Setup  *SetupResult
	Upload *UploadResult
}

// FindRecord reads the installer record of one install: the one in space, of
// packageName when the Space holds several packages' records, or the only
// install of packageName in the organization when space is "".
func (i *Installer) FindRecord(ctx context.Context, space, packageName string) (*InstalledRecord, error)

// ListRecords reads the installer records in space, or in every Space when
// space is "", of packageName or of every package, ordered by Space and
// package.
func (i *Installer) ListRecords(ctx context.Context, space, packageName string) ([]*InstalledRecord, error)

type InstalledRecord struct {
	Space  string
	Data   []byte      // as stored
	Record *api.Record // parsed
}

func (r *InstalledRecord) Package() string
func (r *InstalledRecord) Version() string

// WriteRecord writes a record for a person to read, as `installer show`
// prints it.
func WriteRecord(w io.Writer, r *InstalledRecord) error
```

`installer install` and a host's install command, such as `cub package
install`, both call `Install`, so the two take a package to ConfigHub the
same way. `installer show` calls `FindRecord` and `WriteRecord`, and a
host's commands that show and list installs call them and `ListRecords`,
with their own flags and output formats. The record's types, and
`api.ParseRecord`, are in `pkg/api`.

Preparing and previewing are separate because the CLI uses them
differently: `installer upload --yes` sends no dry runs, `installer upload`
previews without Mutations to find the Units it would empty, and
`installer plan` previews with them, which costs the server more.

The CLI keeps what only the CLI needs: flag parsing, deciding whether to
pass a `Terminal` (stdin is a terminal and `--non-interactive` isn't
given), the confirmation before emptying Units (`PrepareUpload`, then
`PreviewUpload` and `Emptied`, then `Upload`), the "Next:" hints, the
revert hint printed from each Space's ChangeSet, and recording the install
in `~/.confighub/installer/state.yaml` for `installer new`.

Where an error's remedy is a CLI flag, the package returns a sentinel
error (`ErrNoPackage`, `ErrRecordedSourcePull`, `ErrNoInstallerRecord`,
`ErrNoRecordedSource`, `ErrSeveralPackages`, `ErrSeveralInstalls`,
`ErrPresetAndSelect`, `ErrNoPriorInstall`, `ErrNamespaceRequired`,
`ErrInputTypesChanged`, `ErrNewRequiredInputs`, `ErrNoLock`,
`ErrStaleLock`, `ErrSpaceRequired`) wrapped with the details, and
states the condition in terms of the options. The CLI matches it with
`errors.Is` and adds the flag or command to use. Flag syntax (`--input
k=v`, `--space-label k=v`, reserved label keys) is the CLI's to check
before it calls the package.

### The transform step

Render has to keep running the function chain inside kustomize's build.
krusty loads only built-in, exec and container plugins, and running the
chain on kustomize's output instead would move it after hash suffixing and
name-reference fixups. The transformer depends on running before them: the
AppConfig handling infers an immutable ConfigMap from
`internal.config.kubernetes.io/needsHashSuffix`, an internal annotation
kustomize consumes when it adds the suffix and doesn't emit.

So the exec plugin stays, and stops depending on a `transformer`
subcommand. Render writes the wrapper as

```sh
#!/bin/sh
INSTALLER_RUN_TRANSFORMER=1 exec "<os.Executable()>"
```

and a host calls `installer.RunTransformerIfRequested()` first thing in
`main`. When `INSTALLER_RUN_TRANSFORMER=1` is set, it reads the
ResourceList from stdin, writes the result to stdout, and exits;
otherwise it returns at once. This is the re-exec pattern Docker uses in
[`reexec`](https://github.com/moby/sys/tree/main/reexec).
`RunTransformerIfRequested` also records that it was called, so `Render`
can fail with an error naming the missing call instead of a kustomize exec
failure. `TransformerCommand` overrides the wrapper's command for a host
that would rather dispatch a hidden subcommand, and for tests.

Everything a render logs goes to `ErrOut`, not to the host's stderr or
its `slog` default logger. kustomize gives an exec function the stderr of
the process running kustomize, which is the host's, so render passes the
step `INSTALLER_TRANSFORMER_LOG`, naming a file the step writes its log
and any error to. Render copies the file to `ErrOut`, or into the error
when the step fails. The functions the installer runs in the host's
process, the validators and its kustomization edits, log through a
logger that writes to `ErrOut`, which it puts in their context with the
SDK's `handler.WithLogger` (SDK v0.11.13).

`out/compose/` still reproduces with `kustomize build --enable-exec
--enable-alpha-plugins`, using whichever binary rendered it. The
`installer transformer` subcommand stays for people wiring the
transformer into their own kustomizations, as
[transformer.md](./transformer.md) describes. `RunTransformer(ctx, in)
([]byte, error)` is exported for hosts that want to do the same.

### Version

`ClientVersion` defaults to the version of `github.com/confighub/installer`
in `debug.ReadBuildInfo()`, which is the release a host required in its
`go.mod`. The installer binary keeps setting `internal/version.Version`
with `-ldflags`, and that value wins when set. `installerMetadata.installerVersion`
is not checked today; when it is, the check uses this version, so an
embedded installer is held to the release the host links.

## Dependencies for a host

Linking the installer adds `sigs.k8s.io/kustomize/api`,
`github.com/AlecAivazis/survey/v2`, `oras.land/oras-go/v2`,
`github.com/Masterminds/semver/v3`, `github.com/monochromegane/go-gitignore`
and the OCI image-spec modules to the host. All are MIT, Apache 2.0 or
BSD licensed.

For cub specifically:

- **kustomize versions have to line up.** cub pins kustomize with
  `replace` directives, which apply to every module in cub's build. The
  installer requires `sigs.k8s.io/kustomize/api` and `kyaml` v0.20.1
  (kustomize v5.7.1), so cub's pin moves from v5.5.0 (`api` v0.18.0,
  `kyaml` v0.18.1) to v5.7.1 before cub links the installer. When either
  moves later, the other has to follow.
- **cub builds against the SDK at its own revision, not a release.** The
  installer requires released SDK modules (`sdk/core`,
  `sdk/function-impl` and the `configkit` modules they need). cub is
  built in the same tree as the SDK, and its `go.mod` replaces every SDK
  module with that module's directory in the tree. A `replace` that names
  no version applies to every version of the module, so the installer's
  SDK code is compiled from cub's tree, never downloaded: an SDK change
  that breaks the installer's build breaks cub's build in the same
  change, until an installer release adapts to it. That covers only the
  modules cub replaces. If the installer starts using an SDK module cub
  doesn't, cub's build takes that one from its release until a `replace`
  is added.
- **cub's SDK requirements become release versions.** Go still requires
  each SDK requirement in cub's `go.mod` to be at least the version the
  installer requires, and `go mod tidy` raises it, so cub's SDK
  requirements read as that release (v0.11.4 for installer v0.4.0) rather
  than the `v0.0.0` placeholders the other SDK modules use. Only the
  number changes; the `replace` decides the code. When the SDK is
  released, its `go.mod` files are rewritten to require every SDK module
  at the new release, whatever version they named, not just the
  placeholders; otherwise the published cub would require the older
  release the installer names, and build against it.
- **The installer's own build uses SDK releases.** Its CI also builds and
  tests against the SDK's main branch, in a job that doesn't block
  merging. That branch moves when the SDK is released, so the job finds
  a break when the release that causes it comes out, before cub requires
  it. To try the installer against an unreleased SDK, build it locally
  with a `go.work` or a `replace` naming a checkout of the SDK. The
  installer uses a small part of the SDK (`cubapi`, `goclient-new`,
  `plugin`, and the function executor), which keeps such breaks rare.
- **The module graph has a cycle**, `sdk/cmd/cub` → `installer` →
  `sdk/core`. Go allows it across modules. Releases go in order: SDK,
  then an installer release requiring it, then cub requiring that
  installer release.

## Plan

### Phase 1 — `pkg/installer` with the lifecycle moved in (done)

Done as described below, with these differences:

- Upload is `PrepareUpload`, `PreviewUpload` and `Upload`, with
  `PlanUpload` combining the first two (see API above).
- `Options.ConfigHub` exists; when it is nil the package falls back to the
  cub environment's client, as the CLI did. Phase 3 removes the fallback.
- `Options.Warn` lets the CLI add flags to warnings.
- The shared ConfigHub lookups (finding a Space, finding an installer
  record) are in `internal/hubclient`, which `new` uses too.
- Three upload errors name the option instead of the flag (`Target "x"
  names a Target in Space ...`, `resolve Target x: ...`, and the same for
  ChangeSet), and a multi-package upload prints every Space's revert hint
  after all the reports instead of after each.

Before the move, `internal/cli`'s lifecycle tests were added: they run
setup, wizard, render, plan and upload through the cobra tree against
`internal/fakehub`, a fake ConfigHub, and `internal/testregistry`, an
in-memory OCI registry, and compare the output, the work-dir and every
request sent to ConfigHub with golden files. Apart from the changes above,
the golden files are unchanged by the move. `pkg/installer`'s own tests
use only the public API.

The original plan:

- New: `pkg/installer/{installer.go, setup.go, upload.go, errors.go}` —
  `Options`, `New`, `Setup`, `Render`, `PlanUpload`, `Upload`, and the
  sentinel errors.
- Move from `internal/cli/setup.go`: `runFlow`, `buildAnswers`,
  `buildFreshAnswers`, `buildUpgradeAnswers`, `hasPriorChoices`,
  `runDepsUpdate`, `recordedSourceRef`, `runRenderInWorkDir`.
- Move from `internal/cli/upload.go`: `prepareUpload`, `existingSpace`,
  `uploadTargetID`, `resolveChangeSet`, `hideRecord`,
  `installerRecordFetcher`, the label and annotation validation, and the
  per-package send loop. `componentSet` and `variantSet` become empty
  `Component` and `Variant`.
- Progress lines go to `Options.Out`, warnings to `Options.ErrOut`.
- Modify: `internal/cli/{setup,wizard,render,plan,upload}.go` — build
  options from flags, call the package, print "Next:" and revert hints,
  confirm before emptying, record user state, and turn sentinel errors
  into flag-specific messages.
- In this phase the package still uses the global client and the
  environment; phase 3 replaces them.

Acceptance: every script in `test/e2e/` passes unchanged, and the CLI's
output is unchanged apart from line order where a line moved between
the package and the CLI.

### Phase 2 — The transform step without a subcommand (done)

Done as described below. `internal/transform` also has `Requested` and
`Main`, the re-exec check and the process entry point, which
`RunTransformerIfRequested` wraps and test binaries call directly in
`TestMain`. `Setup` with `Render`, and `Render`, return
`ErrNoTransformer` when the program neither called
`RunTransformerIfRequested` nor set `TransformerCommand`. The example the
plan names is a Go `Example` that renders `collector-app`, which has a
function chain and an AppConfig/Env ConfigMap, with a collector that
needs no cub context; `hello-app` is rendered through the public package
by the other `pkg/installer` tests. The only change to the lifecycle
golden files is the wrapper's line.

The original plan:

- New: `internal/transform` — `runTransformer` and the AppConfig handling
  moved from `internal/cli/transformer.go` and `internal/cli/appconfig.go`,
  with no cobra dependency.
- New: `pkg/installer/transformer.go` — `RunTransformer`,
  `RunTransformerIfRequested`.
- Modify: `internal/render/compose.go` — the wrapper sets
  `INSTALLER_RUN_TRANSFORMER=1` and runs the binary with no arguments, or
  runs `TransformerCommand` when set. `render.Options.TransformerBinary`
  becomes `TransformerCommand []string`.
- Modify: `cmd/installer/main.go` — call `RunTransformerIfRequested`
  before the plugin hook.
- Modify: `internal/cli/transformer.go` — the subcommand calls
  `transform`.
- Tests: the render tests' `TestMain` calls `RunTransformerIfRequested`
  instead of building an installer binary. New
  `pkg/installer/example_test.go` sets up and renders
  `examples/hello-app` and `examples/collector-app` through the public
  package only.

Acceptance: a program that imports only `pkg/installer` and `pkg/api`
renders a package with a function chain and AppConfig.

### Phase 3 — Process-wide settings become options (done)

Done as described below, with these details:

- Registry access is an `ipkg.Registry` value (credentials, plain HTTP,
  and a `sign.Verifier` holding the policy and cosign binary) whose
  methods replace the package-level pull, push, inspect, list and tag
  functions, so nothing below the CLI can reach the defaults by accident.
  `deps` and `render` take it too, for resolving and fetching
  dependencies.
- The CLI's environment reads are in `internal/cli/env.go`, including
  the memoized cub client, which moved out of `internal/hubclient`.
  Commands that pull load the signing policy, as before only pulls
  enforced it.
- Uploads verify the client's session themselves (`GET /me`, as the
  CLI's preflight did), returning `ErrNotAuthenticated`; the CLI adds
  "run `cub auth login`".
- Without a client, a fresh setup doesn't look for an installer record.
  For the CLI, that means a user with no cub configuration no longer gets
  a warning that the record couldn't be fetched; every other message is
  unchanged.
- `ClientVersion` defaults to the version linked into the installer
  binary, or else the installer module's version in the program's build
  info. The CLI passes its own version, so a development build still
  reports `dev`.
- With `Registry.Credentials` nil the Docker credential store is used,
  which reads `~/.docker/config.json`; that is the one home-directory
  file a zero `Options` reads, by design. `RunTransformerIfRequested`
  reads `INSTALLER_RUN_TRANSFORMER`, the re-exec protocol of phase 2.
- `TestIgnoresEnvironmentAndHome` sets the signing policy, cosign and cub
  variables and files the CLI reads, each of which would change what
  setup does, and checks that an `Installer` ignores them.

The original plan:

- Modify: `internal/pkg` — registry access takes a `RegistryOptions` in
  place of `authClient()`, the Docker store and `ORAS_OCI_INSECURE`.
- Modify: `internal/sign`, `internal/pkg/pull.go`,
  `internal/deps/resolver.go` — verification takes the policy and cosign
  binary as arguments. `LoadPolicy` keeps reading
  `INSTALLER_SIGNING_POLICY` and the default path, for the CLI and for
  `LoadSigningPolicy`.
- Modify: `internal/collector` — stderr goes to `Options.ErrOut`.
- Remove the `confighub` global; the package uses `Options.ConfigHub`.
  The CLI builds the client with `cubapi.MemoizedClient`, as now.
- `ClientVersion` from build info, as above.
- Modify: `internal/cli` — read `ORAS_OCI_INSECURE`, the signing policy,
  `INSTALLER_COSIGN_BIN` and the cub configuration, and pass them as
  options, so the CLI's behavior is unchanged.

Acceptance: `pkg/installer` and the internal packages it calls read no
environment variables (other than passing the environment through to the
collector) and nothing under the home directory; e2e scripts pass
unchanged.

### Phase 4 — Release and cub adoption

- Move cub's kustomize pin to v5.7.1, as above (done).
- Add the CI job that builds and tests against the SDK's main branch
  (done: `sdk-main` in `.github/workflows/test.yml`, on every pull
  request and daily).
- Release the installer with phases 1–3 (done: v0.4.0).
- In cub: require that release, call `RunTransformerIfRequested` at the
  start of `main`, and replace the two plugin runs in `cub cluster up
  --k8sbot-installer` with `Setup` and `PrepareUpload`/`Upload`, using
  cub's client and printing through cub's output. `--k8sbot-installer`
  then no longer needs the plugin installed. `cub installer` keeps
  running the plugin.

  This is drafted, not merged; whether cub should embed the installer is
  undecided. The draft installs k8sbot's Argo CD and Flux bases, and
  re-enters an existing base from its installer record, as the plugin
  did. What it costs and changes:
  - cub's released binary grows by about 3 MB (105 to 108 MB). It is
    built with cgo off. With cgo on, as a local build on macOS is, it grows
    by 13 MB unless built with the kustomize tag
    `kustomize_disable_go_plugin_support`, which drops kustomize's import of
    Go's `plugin` package; the installer loads no Go plugins. cub's and the
    installer's builds use the tag.
  - The SDK release has to rewrite every SDK requirement, as above.
  - The package is read anonymously, as cub already reads its newest
    release, rather than with the Docker credential store the plugin
    used, so an expired registry login can't refuse it.
  - The upload prints its report but not the installer CLI's revert
    hint; cub's cluster commands undo their own changes on failure.

## Open questions

- Should `Setup` without `Render` stay one call with a flag, or become
  `Wizard` and `Render`? The CLI has both shapes (`setup` and `wizard`
  plus `render`); the proposal mirrors `runFlow`.
- Should `Upload` take `UploadOptions` directly as well, for a host that
  doesn't preview? The proposal makes the preview explicit because an
  upload can empty Units, which withdraws what they deployed.
- Which authoring or publishing operations does a host need first, if any?
