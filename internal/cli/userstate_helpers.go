package cli

import (
	"context"
	"fmt"

	"github.com/confighubai/installer/internal/cubctx"
	"github.com/confighubai/installer/internal/upload"
	"github.com/confighubai/installer/internal/userconfig"
)

// recordedPackages is the allowlist of package names whose upload
// gets recorded in ~/.confighub/installer/state.yaml. Only packages
// `installer new` (or another reader) needs to locate later belong
// here. Most installs do not — operators would not benefit from a
// listing of every package they have ever uploaded, and the user-
// state file should stay scoped to actually-used metadata.
var recordedPackages = map[string]bool{
	"kubernetes-resources": true,
}

// recordUploadInUserState writes a per-user record of the parent
// package's install (org + space + version) into
// ~/.confighub/installer/state.yaml IF the package is in
// recordedPackages. No-op for any other package.
//
// Used by `installer upload` so `installer new` can locate the
// kubernetes-resources package without operator intervention.
func recordUploadInUserState(ctx context.Context, packages []upload.Package) error {
	if len(packages) == 0 || !packages[0].IsParent {
		return nil
	}
	parent := packages[0]
	if !recordedPackages[parent.Name] {
		return nil
	}
	cc, err := cubctx.Get(ctx)
	if err != nil {
		return fmt.Errorf("read cub context: %w", err)
	}
	path, err := userconfig.DefaultPath()
	if err != nil {
		return err
	}
	state, err := userconfig.Load(path)
	if err != nil {
		return err
	}
	state.UpsertInstall(userconfig.InstallRecord{
		Package:        parent.Name,
		PackageVersion: parent.Version,
		OrganizationID: cc.OrganizationID,
		Server:         cc.ServerURL,
		SpaceSlug:      parent.SpaceSlug,
	})
	if err := userconfig.Save(path, state); err != nil {
		return err
	}
	fmt.Printf("Recorded %s install in %s (org %s, space %s)\n",
		parent.Name, path, cc.OrganizationID, parent.SpaceSlug)
	return nil
}
