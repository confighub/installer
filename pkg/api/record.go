// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	// RecordSchema is an installer record's configHub.configSchema.
	RecordSchema = "InstallerRecord"
	// RecordConfigName is an installer record's configHub.configName, and so
	// the slug of the Unit it is stored as. A Space holds one per package.
	RecordConfigName = "installer"
)

// Record is the installer's record of one package's install, which an upload
// stores in the install's Space as a hidden AppConfig/YAML Unit: the package
// definition, where it was pulled from, the selection and inputs, the
// collected facts, and, for the parent, the lock. It is enough to re-enter the
// install without the work-dir it was rendered in. It uses the AppConfig/YAML
// schema's reserved configHub paths rather than KRM apiVersion, kind, and
// metadata, so it is application configuration no Kubernetes function or
// Trigger touches.
//
// The package name and version are said once, in Package; the specs that
// would repeat them leave them empty.
type Record struct {
	ConfigHub RecordMeta     `yaml:"configHub" json:"configHub"`
	Package   RecordPackage  `yaml:"package" json:"package"`
	Source    *SourceSpec    `yaml:"source,omitempty" json:"source,omitempty"`
	Selection *SelectionSpec `yaml:"selection,omitempty" json:"selection,omitempty"`
	Inputs    *InputsSpec    `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Facts     *FactsSpec     `yaml:"facts,omitempty" json:"facts,omitempty"`
	Lock      *LockSpec      `yaml:"lock,omitempty" json:"lock,omitempty"`
}

// RecordMeta is the AppConfig/YAML schema's reserved configHub block.
type RecordMeta struct {
	ConfigSchema string `yaml:"configSchema" json:"configSchema"`
	ConfigName   string `yaml:"configName" json:"configName"`
}

// RecordPackage is installer.yaml without its apiVersion, kind, and metadata.
type RecordPackage struct {
	Name              string            `yaml:"name" json:"name"`
	InstallerMetadata InstallerMetadata `yaml:"installerMetadata,omitempty" json:"installerMetadata,omitempty"`
	Spec              PackageSpec       `yaml:"spec" json:"spec"`
}

// ParseRecord parses an installer record as it is stored.
func ParseRecord(data []byte) (*Record, error) {
	var rec Record
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse installer record: %w", err)
	}
	if rec.ConfigHub.ConfigSchema != RecordSchema {
		return nil, fmt.Errorf("installer record has configHub.configSchema %q, want %q", rec.ConfigHub.ConfigSchema, RecordSchema)
	}
	return &rec, nil
}
