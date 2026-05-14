package diff

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkloadImage is one container's image inside a rendered manifest.
// Used by the per-Space `Images:` footer in plan output. Generated
// from local files (not from ConfigHub) so the footer reflects what
// the next apply would land, regardless of current ConfigHub state.
type WorkloadImage struct {
	Kind      string // Deployment, StatefulSet, DaemonSet, Job, CronJob, Pod, ReplicaSet
	Name      string // metadata.name
	Container string // .name from the container spec
	Image     string // .image
	// Init reports whether this entry came from initContainers.
	Init bool
}

// imageBearingKinds names the workload kinds we extract images from.
// Kept conservative on purpose: PodTemplate-bearing custom resources
// would also have images, but their schemas vary; the shipped
// installer focuses on built-in kinds the Kubernetes/YAML toolchain
// already recognizes.
var imageBearingKinds = map[string]struct{}{
	"Deployment":  {},
	"StatefulSet": {},
	"DaemonSet":   {},
	"ReplicaSet":  {},
	"Job":         {},
	"CronJob":     {},
	"Pod":         {},
}

// ExtractImages walks every .yaml / .yml file in dir (one level deep),
// parses each as a Kubernetes resource, and returns one WorkloadImage
// per container in every workload kind. Output is sorted by (kind,
// name, container, init) so the printer is deterministic.
//
// Unknown kinds are silently skipped; YAML parse errors on a single
// file return an error (the renderer should produce well-formed YAML).
func ExtractImages(dir string) ([]WorkloadImage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A missing manifests dir is treated as empty (e.g., a package
		// with no rendered output yet). Distinguish "I/O error" from
		// "doesn't exist" so the caller's flow is not blocked.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []WorkloadImage
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		imgs, err := imagesFromManifest(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out = append(out, imgs...)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Init != b.Init {
			return a.Init // init containers first within a workload
		}
		return a.Container < b.Container
	})
	return out, nil
}

// imagesFromManifest extracts images from a single multi-doc YAML
// stream. Non-workload docs in the stream are silently skipped.
func imagesFromManifest(data []byte) ([]WorkloadImage, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var out []WorkloadImage
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(doc) == 0 {
			continue
		}
		kind, _ := doc["kind"].(string)
		if _, ok := imageBearingKinds[kind]; !ok {
			continue
		}
		name := metaName(doc)
		podSpec := podSpecForKind(kind, doc)
		if podSpec == nil {
			continue
		}
		out = append(out, containersFromPod(kind, name, podSpec, false)...)
		out = append(out, initContainersFromPod(kind, name, podSpec)...)
	}
	return out, nil
}

func metaName(doc map[string]any) string {
	md, _ := doc["metadata"].(map[string]any)
	if md == nil {
		return ""
	}
	n, _ := md["name"].(string)
	return n
}

// podSpecForKind navigates the workload-specific path to the pod
// template's spec map. Returns nil if the path is missing or the
// shape is unexpected.
func podSpecForKind(kind string, doc map[string]any) map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	if spec == nil {
		return nil
	}
	switch kind {
	case "Pod":
		return spec
	case "CronJob":
		jt, _ := spec["jobTemplate"].(map[string]any)
		if jt == nil {
			return nil
		}
		js, _ := jt["spec"].(map[string]any)
		if js == nil {
			return nil
		}
		t, _ := js["template"].(map[string]any)
		if t == nil {
			return nil
		}
		ts, _ := t["spec"].(map[string]any)
		return ts
	default:
		t, _ := spec["template"].(map[string]any)
		if t == nil {
			return nil
		}
		ts, _ := t["spec"].(map[string]any)
		return ts
	}
}

func containersFromPod(kind, name string, spec map[string]any, init bool) []WorkloadImage {
	cs, _ := spec["containers"].([]any)
	return imagesFromList(kind, name, cs, init)
}

func initContainersFromPod(kind, name string, spec map[string]any) []WorkloadImage {
	cs, _ := spec["initContainers"].([]any)
	return imagesFromList(kind, name, cs, true)
}

func imagesFromList(kind, name string, list []any, init bool) []WorkloadImage {
	var out []WorkloadImage
	for _, c := range list {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		cn, _ := cm["name"].(string)
		ci, _ := cm["image"].(string)
		if ci == "" {
			continue
		}
		out = append(out, WorkloadImage{
			Kind:      kind,
			Name:      name,
			Container: cn,
			Image:     ci,
			Init:      init,
		})
	}
	return out
}
