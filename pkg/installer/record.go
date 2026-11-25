// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/confighub/installer/internal/hubclient"
	"github.com/confighub/installer/pkg/api"
)

// InstalledRecord is an installer record and the Space it is in. An upload
// stores one in each package's Space, saying what was installed there.
type InstalledRecord struct {
	// Space is the slug of the Space the record is in.
	Space string
	// Data is the record as stored, as YAML.
	Data []byte
	// Record is Data parsed.
	Record *api.Record
}

// Package is the name of the package the record is of.
func (r *InstalledRecord) Package() string { return r.Record.Package.Name }

// Version is the version of the package the record is of.
func (r *InstalledRecord) Version() string { return r.Record.Package.InstallerMetadata.Version }

// FindRecord reads the installer record of one install: the one in space, of
// packageName when the Space holds the records of several packages, or, when
// space is empty, the only install of packageName in the organization. One of
// them has to be set.
//
// It fails with ErrNoInstallerRecord when there is no such record,
// ErrSeveralPackages when space holds the records of several packages and
// packageName is empty, and ErrSeveralInstalls when packageName is installed
// in several Spaces and space is empty.
func (i *Installer) FindRecord(ctx context.Context, space, packageName string) (*InstalledRecord, error) {
	if space == "" && packageName == "" {
		return nil, fmt.Errorf("finding an installer record needs a Space or a package name")
	}
	c, err := i.verifiedClient(ctx)
	if err != nil {
		return nil, err
	}
	found, err := hubclient.FindInstallerRecord(ctx, c, space, packageName)
	if err != nil {
		return nil, err
	}
	if found == nil {
		switch {
		case space != "" && packageName != "":
			return nil, optionErrorf(ErrNoInstallerRecord, nil, "Space %s does not exist or has no installer record of %s", space, packageName)
		case space != "":
			return nil, optionErrorf(ErrNoInstallerRecord, nil, "Space %s does not exist or has no installer record", space)
		default:
			return nil, optionErrorf(ErrNoInstallerRecord, nil, "no Space has an installer record of %s", packageName)
		}
	}
	return parseFound(found)
}

// ListRecords reads the installer records in space, or in every Space of the
// organization when space is empty, of packageName, or of every package when
// it is empty. They are ordered by Space and then package. A Space that does
// not exist has none. A record that can't be parsed is left out, with a
// warning.
func (i *Installer) ListRecords(ctx context.Context, space, packageName string) ([]*InstalledRecord, error) {
	c, err := i.verifiedClient(ctx)
	if err != nil {
		return nil, err
	}
	found, err := hubclient.ListInstallerRecords(ctx, c, space, packageName)
	if err != nil {
		return nil, err
	}
	out := make([]*InstalledRecord, 0, len(found))
	for _, f := range found {
		rec, err := parseFound(f)
		if err != nil {
			i.warn(err)
			continue
		}
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b *InstalledRecord) int {
		return cmp.Or(cmp.Compare(a.Space, b.Space), cmp.Compare(a.Package(), b.Package()))
	})
	return out, nil
}

func parseFound(found *hubclient.FoundRecord) (*InstalledRecord, error) {
	rec, err := api.ParseRecord(found.Data)
	if err != nil {
		return nil, fmt.Errorf("the installer record in Space %s: %w", found.SpaceSlug, err)
	}
	return &InstalledRecord{Space: found.SpaceSlug, Data: found.Data, Record: rec}, nil
}
