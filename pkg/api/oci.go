package api

// OCI media types and annotation keys for native installer artifacts.
//
// An installer artifact is a single-layer OCI image manifest where:
//   - ArtifactType discriminates the artifact from Helm OCI charts and other
//     unrelated artifacts in the same registry,
//   - the config blob (ConfigMediaType) carries enough metadata for the
//     resolver and `installer inspect` to operate without pulling the layer,
//   - the single layer (LayerMediaType) is the deterministic .tgz produced by
//     internal/bundle.
const (
	// ArtifactType is the OCI artifactType set on the image manifest for
	// native installer packages.
	ArtifactType = "application/vnd.confighub.installer.package.v1+json"

	// ConfigMediaType is the media type of the config blob (a JSON-encoded
	// ConfigBlob document).
	ConfigMediaType = "application/vnd.confighub.installer.package.config.v1+json"

	// LayerMediaType is the media type of the single .tgz layer.
	LayerMediaType = "application/vnd.confighub.installer.package.tar+gzip"
)

// OCI manifest annotation keys for installer-specific metadata. These mirror
// fields in ConfigBlob so registry-listing UIs can show name/version without
// fetching the config blob.
const (
	AnnotationName             = "installer.confighub.com/name"
	AnnotationVersion          = "installer.confighub.com/version"
	AnnotationKubeVersion      = "installer.confighub.com/kube-version"
	AnnotationInstallerVersion = "installer.confighub.com/installer-version"
)

// LayerTitle is the file/directory name set on the layer descriptor's
// org.opencontainers.image.title annotation. Combined with the file store's
// unpack annotation, this becomes the subdirectory name when pulling.
const LayerTitle = "package"
