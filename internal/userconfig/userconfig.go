// Package userconfig reads and writes the installer's per-user state
// file at ~/.confighub/installer/state.yaml. The file records which
// "bootstrap" packages (notably kubernetes-resources) the user has
// already installed into ConfigHub, keyed by organization, so
// `installer new` can pull from them without re-installing on every
// invocation.
//
// The file is scoped to one operator, not one organization — entries
// carry their organizationID so a single laptop signed into multiple
// orgs has a stable record per (org, package) pair.
package userconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// State is the on-disk shape of ~/.confighub/installer/state.yaml.
type State struct {
	APIVersion string    `yaml:"apiVersion" json:"apiVersion"`
	Kind       string    `yaml:"kind" json:"kind"`
	Metadata   Metadata  `yaml:"metadata" json:"metadata"`
	Spec       StateSpec `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

type StateSpec struct {
	// Installs records each (organizationID, package) → Space slug
	// the operator has bootstrapped. `installer new` consults this
	// before pulling so the kubernetes-resources package only gets
	// uploaded once per org.
	Installs []InstallRecord `yaml:"installs,omitempty" json:"installs,omitempty"`
}

// InstallRecord captures one bootstrap install. Keyed on (Package,
// OrganizationID); ServerURL is informational. SpaceSlug is where the
// package was uploaded.
type InstallRecord struct {
	Package        string `yaml:"package" json:"package"`
	PackageVersion string `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	OrganizationID string `yaml:"organizationID" json:"organizationID"`
	Server         string `yaml:"server,omitempty" json:"server,omitempty"`
	SpaceSlug      string `yaml:"spaceSlug" json:"spaceSlug"`
	InstalledAt    string `yaml:"installedAt,omitempty" json:"installedAt,omitempty"`
}

const (
	apiVersion = "installer.confighub.com/v1alpha1"
	kind       = "UserState"
)

// DefaultPath returns the path the installer writes to:
// ~/.confighub/installer/state.yaml. Honors $HOME for testing.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".confighub", "installer", "state.yaml"), nil
}

// Load reads state from path. Returns an empty State (no installs,
// not an error) if the file does not exist — a fresh laptop has no
// prior installs.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{
				APIVersion: apiVersion,
				Kind:       kind,
				Metadata:   Metadata{Name: "user-state"},
			}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s State
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.APIVersion == "" {
		s.APIVersion = apiVersion
	}
	if s.Kind == "" {
		s.Kind = kind
	}
	if s.Metadata.Name == "" {
		s.Metadata.Name = "user-state"
	}
	return &s, nil
}

// Save writes state to path, creating the parent directory if needed.
// Permissions: 0700 on the directory, 0600 on the file — state
// contains organization IDs but no secrets; defensive nonetheless.
func Save(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// FindInstall returns the install record matching (pkg, orgID), or
// nil if no such record exists. Empty orgID matches any.
func (s *State) FindInstall(pkg, orgID string) *InstallRecord {
	if s == nil {
		return nil
	}
	for i := range s.Spec.Installs {
		r := &s.Spec.Installs[i]
		if r.Package != pkg {
			continue
		}
		if orgID != "" && r.OrganizationID != orgID {
			continue
		}
		return r
	}
	return nil
}

// UpsertInstall replaces the existing record matching (pkg, orgID)
// or appends a new one. InstalledAt is set to now.
func (s *State) UpsertInstall(r InstallRecord) {
	if r.InstalledAt == "" {
		r.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	for i := range s.Spec.Installs {
		ex := &s.Spec.Installs[i]
		if ex.Package == r.Package && ex.OrganizationID == r.OrganizationID {
			*ex = r
			return
		}
	}
	s.Spec.Installs = append(s.Spec.Installs, r)
}
