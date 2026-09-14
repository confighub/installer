// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/api"
)

// PriorState bundles the documents we recover from a prior install. Any
// field may be nil if it was never written (e.g., Facts when the
// package has no Collector).
type PriorState struct {
	Selection *api.Selection
	Inputs    *api.Inputs
	Facts     *api.Facts
	// PriorPackage is the installer.yaml that produced this state —
	// recovered either from the installer record in ConfigHub or from the
	// work-dir's package/installer.yaml. Used by upgrade's schema-diff to
	// compare new vs old.
	PriorPackage *api.Package
}

// PriorSource names where the prior state came from.
type PriorSource string

const (
	// SourceNone indicates no prior state was found.
	SourceNone PriorSource = "none"
	// SourceLocal indicates state came from out/record/*.yaml.
	SourceLocal PriorSource = "local"
	// SourceConfigHub indicates state came from the installer record Unit
	// an earlier upload wrote to ConfigHub.
	SourceConfigHub PriorSource = "confighub"
)

// RecordFetcher returns the installer record document an earlier upload of the
// named package wrote to ConfigHub, or nil when there is none to use.
type RecordFetcher func(ctx context.Context, packageName string) ([]byte, error)

// LoadPriorState looks for prior install state for packageName, preferring the
// work-dir's own record of its last render. A work-dir without one, such as a
// fresh clone, is recovered from the installer record in ConfigHub through
// fetch, when fetch is non-nil. Returns SourceNone with a nil state if neither
// has any.
//
// A ConfigHub fetch failure is not an error: it is reported through warn (the
// CLI prints to stderr; tests pass nil) and the install starts fresh.
func LoadPriorState(ctx context.Context, workDir, packageName string, fetch RecordFetcher, warn func(string)) (*PriorState, PriorSource, error) {
	state, err := loadLocalSpec(workDir)
	if err != nil {
		return nil, SourceNone, err
	}
	if state != nil {
		return state, SourceLocal, nil
	}
	if fetch == nil {
		return nil, SourceNone, nil
	}
	data, err := fetch(ctx, packageName)
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("fetch the installer record for %s from ConfigHub: %v", packageName, err))
		}
		return nil, SourceNone, nil
	}
	if data == nil {
		return nil, SourceNone, nil
	}
	prior, err := upload.ParseRecord(data)
	if err != nil {
		return nil, SourceNone, err
	}
	return &PriorState{
		Selection:    prior.Selection,
		Inputs:       prior.Inputs,
		Facts:        prior.Facts,
		PriorPackage: prior.Package,
	}, SourceConfigHub, nil
}

// loadLocalSpec reads selection.yaml / inputs.yaml / facts.yaml from the
// work-dir's record dir. Returns nil with no error if none are present (a
// fresh work-dir is not an error).
//
// PriorPackage is read from <work-dir>/package/installer.yaml so
// upgrade's schema-diff has something to compare against. If the
// package directory is missing, PriorPackage is left nil.
func loadLocalSpec(workDir string) (*PriorState, error) {
	recordDir := filepath.Join(workDir, "out", api.RecordDir)
	state := &PriorState{}
	any := false

	if data, err := os.ReadFile(filepath.Join(recordDir, "selection.yaml")); err == nil {
		s, perr := api.ParseSelection(data)
		if perr != nil {
			return nil, fmt.Errorf("parse selection.yaml: %w", perr)
		}
		state.Selection = s
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(recordDir, "inputs.yaml")); err == nil {
		i, perr := api.ParseInputs(data)
		if perr != nil {
			return nil, fmt.Errorf("parse inputs.yaml: %w", perr)
		}
		state.Inputs = i
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(recordDir, "facts.yaml")); err == nil {
		f, perr := api.ParseFacts(data)
		if perr != nil {
			return nil, fmt.Errorf("parse facts.yaml: %w", perr)
		}
		state.Facts = f
		any = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if data, err := os.ReadFile(filepath.Join(workDir, "package", "installer.yaml")); err == nil {
		p, perr := api.ParsePackage(data)
		if perr == nil {
			state.PriorPackage = p
		}
	}

	if !any {
		return nil, nil
	}
	return state, nil
}
