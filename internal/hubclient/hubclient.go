// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package hubclient is the ConfigHub lookups several parts of the installer
// share: finding a Space, and finding the installer record an upload left in
// one.
package hubclient

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"

	"github.com/confighub/installer/internal/upload"
)

// ExistingSpace returns the Space with slug, or nil when there is none yet.
func ExistingSpace(ctx context.Context, c *cubapi.Client, slug string) (*goclientnew.Space, error) {
	found, err := cubapi.ResolveSpace(ctx, c, cubapi.NewRef("", slug), cubapi.ResolveOpts{})
	if cubapi.IsNotFoundError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up Space %s: %w", slug, err)
	}
	return found.Space, nil
}

// FoundRecord is an installer record and the Space it is in.
type FoundRecord struct {
	Data      []byte
	SpaceSlug string
}

// ErrSeveralPackages is what a *SeveralPackagesError is, for errors.Is.
var ErrSeveralPackages = errors.New("the Space holds installer records of several packages")

// ErrSeveralInstalls is what a *SeveralInstallsError is, for errors.Is.
var ErrSeveralInstalls = errors.New("the package is installed in several Spaces")

// SeveralPackagesError is returned when a Space holds the installer records of
// more than one package and no package was named.
type SeveralPackagesError struct {
	Space string
	Count int
}

func (e *SeveralPackagesError) Error() string {
	return fmt.Sprintf("Space %s holds installer records of %d packages", e.Space, e.Count)
}

func (e *SeveralPackagesError) Is(target error) bool { return target == ErrSeveralPackages }

// SeveralInstallsError is returned when a package is installed in more than
// one Space and no Space was named.
type SeveralInstallsError struct {
	Package string
	Count   int
	Spaces  []string
}

func (e *SeveralInstallsError) Error() string {
	return fmt.Sprintf("%s is installed in %d Spaces (%s)", e.Package, e.Count, strings.Join(e.Spaces, ", "))
}

func (e *SeveralInstallsError) Is(target error) bool { return target == ErrSeveralInstalls }

// FindInstallerRecord reads the installer record an earlier upload wrote: the
// Unit keyed AppConfig/YAML/installer that the package's uploads own, which is
// hidden. With space set it reads the record in that Space, of whichever
// package owns it when packageName is empty. Without, it looks across the
// organization and uses the one it finds, refusing when several installs of
// the package exist, since picking one could re-enter someone else's. It
// returns nil when there is no record.
func FindInstallerRecord(ctx context.Context, c *cubapi.Client, space, packageName string) (*FoundRecord, error) {
	if packageName == "" && space == "" {
		return nil, fmt.Errorf("a Space or a package name is needed to find an installer record")
	}
	units, err := recordUnits(ctx, c, space, packageName)
	if err != nil {
		return nil, err
	}
	switch len(units) {
	case 0:
		return nil, nil
	case 1:
	default:
		if space != "" {
			return nil, &SeveralPackagesError{Space: space, Count: len(units)}
		}
		var spaces []string
		for _, u := range units {
			spaces = append(spaces, unitSpaceSlug(u, ""))
		}
		return nil, &SeveralInstallsError{Package: packageName, Count: len(units), Spaces: spaces}
	}
	return readRecord(ctx, c, units[0], space)
}

// ListInstallerRecords reads the installer records in space, or across the
// organization when space is empty, of packageName or, when it is empty, of
// every package. A Space that does not exist has none.
func ListInstallerRecords(ctx context.Context, c *cubapi.Client, space, packageName string) ([]*FoundRecord, error) {
	units, err := recordUnits(ctx, c, space, packageName)
	if err != nil {
		return nil, err
	}
	out := make([]*FoundRecord, 0, len(units))
	for _, u := range units {
		found, err := readRecord(ctx, c, u, space)
		if err != nil {
			return nil, err
		}
		out = append(out, found)
	}
	return out, nil
}

// recordUnits lists the installer record Units in space, or across the
// organization, of packageName, or of every package.
func recordUnits(ctx context.Context, c *cubapi.Client, space, packageName string) ([]*goclientnew.ExtendedUnit, error) {
	where := cubapi.Where{}.
		Eq("Annotations.UploadResource", "AppConfig/YAML/"+upload.RecordConfigName)
	if packageName != "" {
		where = where.Eq("Labels.UploadSource", packageName)
	}
	if space != "" {
		found, err := ExistingSpace(ctx, c, space)
		if err != nil {
			return nil, err
		}
		if found == nil {
			// Nothing is installed there yet; the upload creates it.
			return nil, nil
		}
		where = where.Eq("SpaceID", found.SpaceID.String())
	}
	return cubapi.ListUnits(ctx, c, where, cubapi.ListOpts{Include: "SpaceID", IncludeHidden: upload.RecordHiddenReason})
}

// readRecord reads the data of a record Unit recordUnits listed.
func readRecord(ctx context.Context, c *cubapi.Client, u *goclientnew.ExtendedUnit, space string) (*FoundRecord, error) {
	data, err := cubapi.UnitData(ctx, c, u.Unit.SpaceID, u.Unit.UnitID)
	if err != nil {
		return nil, err
	}
	return &FoundRecord{Data: []byte(data), SpaceSlug: unitSpaceSlug(u, space)}, nil
}

// unitSpaceSlug is the slug of the Space a listed Unit is in, or fallback when
// the list didn't include it.
func unitSpaceSlug(u *goclientnew.ExtendedUnit, fallback string) string {
	if u.Space != nil {
		return u.Space.Slug
	}
	return fallback
}
