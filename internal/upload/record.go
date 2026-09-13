// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package upload

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/pkg/api"
)

const (
	// RecordFileName is the path the record document is uploaded under.
	RecordFileName = "installer-record.yaml"
	// RecordConfigName is the record's configHub.configName, and so the slug
	// of the Unit it becomes. A Space holds one per package.
	RecordConfigName = "installer"
	// RecordSchema is the record's configHub.configSchema.
	RecordSchema = "InstallerRecord"
)

// Record is the installer's record of one package's render, uploaded as a
// single AppConfig/YAML document: the package definition, the user's selection
// and inputs, the collected facts, and, for the parent, the lock. It uses the
// AppConfig/YAML schema's reserved configHub paths rather than KRM apiVersion,
// kind, and metadata, so it is application configuration no Kubernetes function
// or Trigger touches, and the wizard reads it back to re-enter a prior install.
type Record struct {
	ConfigHub RecordMeta         `yaml:"configHub"`
	Package   RecordPackage      `yaml:"package"`
	Selection *api.SelectionSpec `yaml:"selection,omitempty"`
	Inputs    *api.InputsSpec    `yaml:"inputs,omitempty"`
	Facts     *api.FactsSpec     `yaml:"facts,omitempty"`
	Lock      *api.LockSpec      `yaml:"lock,omitempty"`
}

// RecordMeta is the AppConfig/YAML schema's reserved configHub block.
type RecordMeta struct {
	ConfigSchema string `yaml:"configSchema"`
	ConfigName   string `yaml:"configName"`
}

// RecordPackage is installer.yaml without its apiVersion, kind, and metadata.
type RecordPackage struct {
	Name              string                `yaml:"name"`
	InstallerMetadata api.InstallerMetadata `yaml:"installerMetadata,omitempty"`
	Spec              api.PackageSpec       `yaml:"spec"`
}

// specsNamingThePackage are the record sections whose specs repeat the package
// name and version, which the record's package section already says once.
var specsNamingThePackage = []string{"selection", "inputs", "facts"}

// BuildRecord reads pkg's installer.yaml and record directory and returns the
// record document. facts.yaml is optional, as is lock.yaml, which only the
// parent has.
func BuildRecord(pkg Package) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(pkg.PackageDir, "installer.yaml"))
	if err != nil {
		return nil, err
	}
	p, err := api.ParsePackage(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s installer.yaml: %w", pkg.Name, err)
	}
	rec := Record{
		ConfigHub: RecordMeta{ConfigSchema: RecordSchema, ConfigName: RecordConfigName},
		Package:   RecordPackage{Name: p.Metadata.Name, InstallerMetadata: p.InstallerMetadata, Spec: p.Spec},
	}

	if data, err := readRequired(pkg.RecordDir, "selection.yaml"); err != nil {
		return nil, err
	} else if sel, err := api.ParseSelection(data); err != nil {
		return nil, fmt.Errorf("parse selection.yaml: %w", err)
	} else {
		rec.Selection = &sel.Spec
	}
	if data, err := readRequired(pkg.RecordDir, "inputs.yaml"); err != nil {
		return nil, err
	} else if in, err := api.ParseInputs(data); err != nil {
		return nil, fmt.Errorf("parse inputs.yaml: %w", err)
	} else {
		rec.Inputs = &in.Spec
	}
	if data, ok, err := readOptional(pkg.RecordDir, "facts.yaml"); err != nil {
		return nil, err
	} else if ok {
		f, err := api.ParseFacts(data)
		if err != nil {
			return nil, fmt.Errorf("parse facts.yaml: %w", err)
		}
		rec.Facts = &f.Spec
	}
	if data, ok, err := readOptional(pkg.RecordDir, "lock.yaml"); err != nil {
		return nil, err
	} else if ok {
		l, err := api.ParseLock(data)
		if err != nil {
			return nil, fmt.Errorf("parse lock.yaml: %w", err)
		}
		rec.Lock = &l.Spec
	}
	return marshalRecord(rec)
}

// marshalRecord renders the record, dropping the package name and version each
// spec repeats.
func marshalRecord(rec Record) ([]byte, error) {
	var root yaml.Node
	if err := root.Encode(rec); err != nil {
		return nil, err
	}
	for _, section := range specsNamingThePackage {
		spec := mappingValue(&root, section)
		deleteMappingKey(spec, "package")
		deleteMappingKey(spec, "packageVersion")
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PriorDocuments is what a record says about a prior install, as the installer's
// own document types.
type PriorDocuments struct {
	Package   *api.Package
	Selection *api.Selection
	Inputs    *api.Inputs
	Facts     *api.Facts
	Lock      *api.Lock
}

// ParseRecord reads a record document back into the installer's document
// types, restoring the package name and version the record says once.
func ParseRecord(data []byte) (*PriorDocuments, error) {
	var rec Record
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse installer record: %w", err)
	}
	if rec.ConfigHub.ConfigSchema != RecordSchema {
		return nil, fmt.Errorf("installer record has configHub.configSchema %q, want %q", rec.ConfigHub.ConfigSchema, RecordSchema)
	}
	name, version := rec.Package.Name, rec.Package.InstallerMetadata.Version
	out := &PriorDocuments{
		Package: &api.Package{
			APIVersion:        api.APIVersion,
			Kind:              api.KindPackage,
			Metadata:          api.Metadata{Name: name},
			InstallerMetadata: rec.Package.InstallerMetadata,
			Spec:              rec.Package.Spec,
		},
	}
	if rec.Selection != nil {
		spec := *rec.Selection
		spec.Package, spec.PackageVersion = name, version
		out.Selection = &api.Selection{APIVersion: api.APIVersion, Kind: api.KindSelection,
			Metadata: api.Metadata{Name: name + "-selection"}, Spec: spec}
	}
	if rec.Inputs != nil {
		spec := *rec.Inputs
		spec.Package, spec.PackageVersion = name, version
		out.Inputs = &api.Inputs{APIVersion: api.APIVersion, Kind: api.KindInputs,
			Metadata: api.Metadata{Name: name + "-inputs"}, Spec: spec}
	}
	if rec.Facts != nil {
		spec := *rec.Facts
		spec.Package, spec.PackageVersion = name, version
		out.Facts = &api.Facts{APIVersion: api.APIVersion, Kind: api.KindFacts,
			Metadata: api.Metadata{Name: name + "-facts"}, Spec: spec}
	}
	if rec.Lock != nil {
		out.Lock = &api.Lock{APIVersion: api.APIVersion, Kind: api.KindLock,
			Metadata: api.Metadata{Name: name + "-lock"}, Spec: *rec.Lock}
	}
	return out, nil
}

// Namespace returns the install namespace recorded in pkg's inputs.yaml.
func Namespace(pkg Package) (string, error) {
	data, err := readRequired(pkg.RecordDir, "inputs.yaml")
	if err != nil {
		return "", err
	}
	in, err := api.ParseInputs(data)
	if err != nil {
		return "", fmt.Errorf("parse inputs.yaml: %w", err)
	}
	return in.Spec.Namespace, nil
}

func readRequired(dir, name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("read %s — run `installer setup` first: %w", filepath.Join(dir, name), err)
	}
	return data, nil
}

func readOptional(dir, name string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// mappingValue returns the value node for key in the document's top-level
// mapping, or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func deleteMappingKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}
