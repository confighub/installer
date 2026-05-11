package render

import (
	"crypto/sha1"
	"fmt"
	"sort"
	"strings"

	"github.com/confighubai/installer/pkg/api"
	"gopkg.in/yaml.v3"
)

// File represents one rendered Kubernetes resource as it will be written to
// disk and uploaded as one ConfigHub Unit.
type File struct {
	// Filename is the recommended filename within out/manifests/.
	Filename string
	// Slug is the recommended ConfigHub Unit slug for upload.
	Slug string
	// Body is the YAML body of the resource.
	Body []byte
	// Kind / Name / Namespace / APIVersion are sniffed from the doc, used
	// for the manifest index.
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
}

// splitForUnits splits a multi-doc YAML stream into one File per resource,
// deriving deterministic slugs and filenames from kind + name + namespace.
// Slug collisions are disambiguated with a short hash suffix.
func splitForUnits(stream []byte) ([]File, error) {
	docs, err := api.SplitMultiDoc(stream)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(docs))
	for _, doc := range docs {
		f, err := fileForDoc(doc)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		files = append(files, *f)
	}

	// Disambiguate slug collisions.
	seen := map[string]int{}
	for i := range files {
		seen[files[i].Slug]++
	}
	for i := range files {
		if seen[files[i].Slug] > 1 {
			h := sha1.Sum(files[i].Body)
			suffix := fmt.Sprintf("-%x", h[:2])
			files[i].Slug += suffix
			files[i].Filename = files[i].Slug + ".yaml"
		}
	}

	// Stable order so re-renders produce identical output.
	sort.SliceStable(files, func(i, j int) bool { return files[i].Slug < files[j].Slug })
	return files, nil
}

func fileForDoc(doc []byte) (*File, error) {
	var meta struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(doc, &meta); err != nil {
		return nil, fmt.Errorf("sniff doc: %w", err)
	}
	if meta.Kind == "" || meta.Metadata.Name == "" {
		// Skip empty/null docs that survived split.
		return nil, nil
	}
	slug := slugify(meta.Kind, meta.Metadata.Namespace, meta.Metadata.Name)
	return &File{
		Filename:   slug + ".yaml",
		Slug:       slug,
		Body:       doc,
		APIVersion: meta.APIVersion,
		Kind:       meta.Kind,
		Name:       meta.Metadata.Name,
		Namespace:  meta.Metadata.Namespace,
	}, nil
}

// slugify derives a Unit slug from kind + namespace + name following kubectl-
// style conventions: kind is lowercased and short-formed, namespace is
// included only when present, name is sanitized to slug-safe characters.
func slugify(kind, namespace, name string) string {
	parts := []string{strings.ToLower(kind)}
	if namespace != "" {
		parts = append(parts, sanitize(namespace))
	}
	parts = append(parts, sanitize(name))
	return strings.Join(parts, "-")
}

func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '.' || r == '_':
			b.WriteRune('-')
		}
	}
	return b.String()
}
