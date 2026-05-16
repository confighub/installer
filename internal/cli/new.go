package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/cubctx"
	"github.com/confighub/installer/internal/userconfig"
)

// kubernetesResourcesPackageName must match
// packages/kubernetes-resources/installer.yaml's metadata.name. Used
// as the userconfig key when looking up the bootstrapped Space.
const kubernetesResourcesPackageName = "kubernetes-resources"

// canonicalExample names the kubernetes-resources example template
// `installer new <kind>` clones for each Kubernetes kind. The slug
// uses the deterministic <kind-lowercase>-<namespace>-<name> shape
// produced by render's splitForUnits — `installer new` substitutes
// the bootstrapped namespace at lookup time.
//
// origName is the placeholder name in the example (renamed to the
// operator's <name> argument). For shapes with no useful name (the
// Namespace example's name IS the namespace), origName is empty and
// the new resource carries the operator's <name> verbatim with no
// rename.
type canonicalExample struct {
	kind     string // lowercase, used in slug
	origName string
}

// kindToExample maps the user-facing kind on `installer new <kind>`
// to the canonical example Unit. Aliases included for ergonomics
// (hpa → horizontalpodautoscaler, etc.).
var kindToExample = map[string]canonicalExample{
	"deployment":              {kind: "deployment", origName: "hello-app"},
	"statefulset":             {kind: "statefulset", origName: "hello-db"},
	"daemonset":               {kind: "daemonset", origName: "hello-exporter"},
	"job":                     {kind: "job", origName: "hello-migrate"},
	"cronjob":                 {kind: "cronjob", origName: "hello-backup"},
	"ingress":                 {kind: "ingress", origName: "hello-app"},
	"service":                 {kind: "service", origName: "hello-app"},
	"serviceaccount":          {kind: "serviceaccount", origName: "hello-app"},
	"role":                    {kind: "role", origName: "hello-app"},
	"rolebinding":             {kind: "rolebinding", origName: "hello-app"},
	"horizontalpodautoscaler": {kind: "horizontalpodautoscaler", origName: "hello-app"},
	"hpa":                     {kind: "horizontalpodautoscaler", origName: "hello-app"},
	"poddisruptionbudget":     {kind: "poddisruptionbudget", origName: "hello-app"},
	"pdb":                     {kind: "poddisruptionbudget", origName: "hello-app"},
	"networkpolicy":           {kind: "networkpolicy", origName: "default-deny-all"},
	"namespace":               {kind: "namespace", origName: ""},
}

func sortedKindNames() []string {
	out := make([]string, 0, len(kindToExample))
	for k := range kindToExample {
		out = append(out, k)
	}
	// stable, alphabetic — used for help text
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func newNewCmd() *cobra.Command {
	var (
		image       string
		port        int
		replicas    int
		pkgDir      string
		outFile     string
		stdoutOnly  bool
		nsOverride  string
		spaceSlug   string
		updateKust  bool
	)
	cmd := &cobra.Command{
		Use:   "new <kind> <name>",
		Short: "Scaffold a Kubernetes resource into the current package, cloning the kubernetes-resources example",
		Long: "New writes a Kubernetes YAML file into the current package's\n" +
			"bases/default/ (or --package <dir>'s bases/default/), cloning the\n" +
			"canonical example from the kubernetes-resources package in\n" +
			"ConfigHub. Pre-applies operator-supplied customizations (--name is\n" +
			"required; --image, --port, --replicas optional) and substitutes\n" +
			"`confighubplaceholder` for the namespace so the package's own\n" +
			"set-namespace function can rewrite it at install time.\n\n" +
			"Supported kinds (alphabetical): " + strings.Join(sortedKindNames(), ", ") + ".\n\n" +
			"The kubernetes-resources package must be installed in the current\n" +
			"cub organization first. Bootstrap with:\n\n" +
			"  installer wizard <path>/packages/kubernetes-resources \\\n" +
			"      --work-dir /tmp/k8s-res \\\n" +
			"      --non-interactive --namespace kubernetes-resources\n" +
			"  installer render /tmp/k8s-res\n" +
			"  installer upload /tmp/k8s-res --space kubernetes-resources\n\n" +
			"The upload step records the install in\n" +
			"~/.confighub/installer/state.yaml; subsequent `installer new` calls\n" +
			"read the recorded Space and pull the canonical Unit body via cub.\n\n" +
			"Output goes to <package>/bases/default/<kind>-<name>.yaml. Pass\n" +
			"--stdout to print to stdout instead. Pass --update-kustomization\n" +
			"to also append the new file to bases/default/kustomization.yaml's\n" +
			"resources list.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			kindArg := strings.ToLower(args[0])
			name := args[1]
			example, ok := kindToExample[kindArg]
			if !ok {
				return fmt.Errorf("unsupported kind %q; supported: %s",
					kindArg, strings.Join(sortedKindNames(), ", "))
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Locate the kubernetes-resources install for the active
			// cub organization.
			cc, err := cubctx.Get(ctx)
			if err != nil {
				return fmt.Errorf("read cub context: %w", err)
			}
			install, err := findKubernetesResourcesInstall(cc.OrganizationID)
			if err != nil {
				return err
			}
			// --space-slug overrides the recorded Space (useful when
			// the operator wants to install kubernetes-resources to
			// a non-default Space and wire `installer new` to it).
			if spaceSlug == "" {
				spaceSlug = install.SpaceSlug
			}

			// The Unit slug is <kind>-<install-namespace>-<orig-name>
			// when origName is non-empty; <kind>-<install-namespace>
			// otherwise (Namespace, where the resource name IS the
			// install namespace).
			installNs := nsOverride
			if installNs == "" {
				installNs = install.SpaceSlug // default install --namespace matches Space slug; see Long
			}
			unitSlug := example.kind + "-" + installNs
			if example.origName != "" {
				unitSlug += "-" + example.origName
			}

			body, err := fetchUnitData(ctx, spaceSlug, unitSlug)
			if err != nil {
				return fmt.Errorf("fetch %s/%s: %w (the kubernetes-resources install may use a different namespace; pass --install-namespace <ns> or check the Space)",
					spaceSlug, unitSlug, err)
			}

			// Customize: rename, set namespace placeholder, image,
			// ports, replicas. Mutates the YAML in-place.
			customized, err := customizeManifest(body, customizeOpts{
				origName: example.origName,
				newName:  name,
				image:    image,
				port:     port,
				replicas: replicas,
			})
			if err != nil {
				return err
			}

			if stdoutOnly {
				_, err := os.Stdout.Write(customized)
				return err
			}

			abs, err := filepath.Abs(pkgDir)
			if err != nil {
				return err
			}
			baseDir := filepath.Join(abs, "bases", "default")
			if _, err := os.Stat(filepath.Join(abs, "installer.yaml")); err != nil {
				return fmt.Errorf("%s does not look like a package (no installer.yaml); run `installer init` first or pass --package <dir>", abs)
			}
			if outFile == "" {
				outFile = filepath.Join(baseDir, example.kind+"-"+name+".yaml")
			} else if !filepath.IsAbs(outFile) {
				outFile = filepath.Join(baseDir, outFile)
			}
			if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(outFile, customized, 0o644); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", outFile)

			if updateKust {
				if err := appendToKustomization(baseDir, filepath.Base(outFile)); err != nil {
					return fmt.Errorf("update kustomization.yaml: %w", err)
				}
				fmt.Printf("Updated %s\n", filepath.Join(baseDir, "kustomization.yaml"))
			}
			fmt.Println()
			fmt.Println("Next: re-render to verify the new resource:")
			fmt.Printf("  %s wizard %s --work-dir /tmp/dev --non-interactive --namespace demo\n", InvocationName(), abs)
			fmt.Printf("  %s render /tmp/dev\n", InvocationName())
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "container image ref (workloads only)")
	cmd.Flags().IntVar(&port, "port", 0, "container port (workloads + Service only)")
	cmd.Flags().IntVar(&replicas, "replicas", 0, "replica count (Deployment only)")
	cmd.Flags().StringVar(&pkgDir, "package", ".", "package directory containing installer.yaml")
	cmd.Flags().StringVar(&outFile, "out-file", "", "output filename (relative to <package>/bases/default/, or absolute)")
	cmd.Flags().BoolVar(&stdoutOnly, "stdout", false, "print the customized YAML to stdout instead of writing a file")
	cmd.Flags().StringVar(&nsOverride, "install-namespace", "", "namespace the kubernetes-resources package was installed with (default: same as space slug)")
	cmd.Flags().StringVar(&spaceSlug, "space-slug", "", "override the kubernetes-resources Space recorded in user state")
	cmd.Flags().BoolVar(&updateKust, "update-kustomization", true, "append the new file to bases/default/kustomization.yaml resources list")
	return cmd
}

// findKubernetesResourcesInstall reads ~/.confighub/installer/state.yaml
// and returns the install record for the active org. Returns a
// formatted error with bootstrap instructions when missing.
func findKubernetesResourcesInstall(orgID string) (*userconfig.InstallRecord, error) {
	path, err := userconfig.DefaultPath()
	if err != nil {
		return nil, err
	}
	state, err := userconfig.Load(path)
	if err != nil {
		return nil, err
	}
	if rec := state.FindInstall(kubernetesResourcesPackageName, orgID); rec != nil {
		return rec, nil
	}
	return nil, fmt.Errorf(
		"kubernetes-resources is not installed for organization %s. Bootstrap with:\n"+
			"  installer wizard <path>/packages/kubernetes-resources --work-dir /tmp/k8s-res --non-interactive --namespace kubernetes-resources\n"+
			"  installer render /tmp/k8s-res\n"+
			"  installer upload /tmp/k8s-res --space kubernetes-resources\n"+
			"Then re-run `installer new`.",
		orgID,
	)
}

// fetchUnitData shells out to `cub unit data` and returns the body.
func fetchUnitData(ctx context.Context, space, slug string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "cub", "unit", "data", "--space", space, slug)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cub unit data: %w\n%s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type customizeOpts struct {
	origName string
	newName  string
	image    string
	port     int
	replicas int
}

// customizeManifest mutates a single-resource YAML body to apply the
// operator's customizations. Per-doc mutations (multi-doc bundles
// like Deployment+Service get each doc updated). All resources get
// their namespace rewritten to `confighubplaceholder` so the
// operator's own package-level set-namespace function rewrites it at
// install time.
func customizeManifest(body []byte, opts customizeOpts) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(body))
	var out bytes.Buffer
	first := true
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("decode YAML doc: %w", err)
		}
		if node.Kind == 0 {
			continue
		}
		if err := mutateDoc(&node, opts); err != nil {
			return nil, err
		}
		if !first {
			out.WriteString("---\n")
		}
		first = false
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(&node); err != nil {
			return nil, err
		}
		_ = enc.Close()
	}
	return out.Bytes(), nil
}

// mutateDoc walks one parsed YAML node tree and applies the
// operator's customizations. Conservative: only paths we know about
// get touched; anything we don't recognize is left alone.
func mutateDoc(node *yaml.Node, opts customizeOpts) error {
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return nil
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}

	// metadata.namespace → confighubplaceholder (applies to every
	// namespaced resource; harmless on cluster-scoped Kinds, which
	// have no metadata.namespace and we just don't touch).
	if md := mappingChild(root, "metadata"); md != nil {
		// metadata.name: rename if origName matches
		if opts.origName != "" {
			renameStringPath(md, "name", opts.origName, opts.newName)
			renameLabelMap(md, opts.origName, opts.newName)
		} else if opts.newName != "" {
			// Namespace-style: replace whatever's there with newName.
			setStringPath(md, "name", opts.newName)
		}
		setStringPath(md, "namespace", "confighubplaceholder")
		stripConfighubAnnotations(md)
	}

	// spec.* customizations: replicas, selector matchLabels, template
	// labels, container image / port.
	spec := mappingChild(root, "spec")
	if spec != nil {
		if opts.replicas > 0 {
			setIntPath(spec, "replicas", opts.replicas)
		}
		// selector.matchLabels.app rename
		if opts.origName != "" {
			if sel := mappingChild(spec, "selector"); sel != nil {
				if ml := mappingChild(sel, "matchLabels"); ml != nil {
					renameStringPath(ml, "app", opts.origName, opts.newName)
				}
			}
			// template.metadata.labels.app rename + serviceAccountName
			if tmpl := mappingChild(spec, "template"); tmpl != nil {
				if tmd := mappingChild(tmpl, "metadata"); tmd != nil {
					renameLabelMap(tmd, opts.origName, opts.newName)
				}
				if tspec := mappingChild(tmpl, "spec"); tspec != nil {
					renameStringPath(tspec, "serviceAccountName", opts.origName, opts.newName)
					mutatePodSpec(tspec, opts)
				}
			}
		} else {
			// Pod-style or Service-style — apply container/port edits
			// to spec directly when there's a template.
			if tmpl := mappingChild(spec, "template"); tmpl != nil {
				if tspec := mappingChild(tmpl, "spec"); tspec != nil {
					mutatePodSpec(tspec, opts)
				}
			}
		}
		// Service: spec.ports[].port = opts.port if set
		if opts.port > 0 {
			if portsSeq := mappingChild(spec, "ports"); portsSeq != nil && portsSeq.Kind == yaml.SequenceNode {
				for _, p := range portsSeq.Content {
					if p.Kind == yaml.MappingNode {
						setIntPath(p, "port", opts.port)
					}
				}
			}
		}
	}
	return nil
}

func mutatePodSpec(podSpec *yaml.Node, opts customizeOpts) {
	cs := mappingChild(podSpec, "containers")
	if cs == nil || cs.Kind != yaml.SequenceNode {
		return
	}
	for _, c := range cs.Content {
		if c.Kind != yaml.MappingNode {
			continue
		}
		if opts.image != "" {
			setStringPath(c, "image", opts.image)
		}
		if opts.port > 0 {
			ports := mappingChild(c, "ports")
			if ports != nil && ports.Kind == yaml.SequenceNode && len(ports.Content) > 0 {
				if first := ports.Content[0]; first.Kind == yaml.MappingNode {
					setIntPath(first, "containerPort", opts.port)
				}
			}
		}
	}
}

// --- yaml.Node helpers ---

func mappingChild(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setStringPath(node *yaml.Node, key, value string) {
	if v := mappingChild(node, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!str"
		v.Value = value
		return
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func setIntPath(node *yaml.Node, key string, value int) {
	if v := mappingChild(node, key); v != nil {
		v.Kind = yaml.ScalarNode
		v.Tag = "!!int"
		v.Value = fmt.Sprintf("%d", value)
		return
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)},
	)
}

// renameStringPath rewrites node[key] from origValue to newValue
// only when the existing value matches origValue. No-op otherwise.
func renameStringPath(node *yaml.Node, key, origValue, newValue string) {
	v := mappingChild(node, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return
	}
	if v.Value == origValue {
		v.Value = newValue
	}
}

// renameLabelMap rewrites the value of `app: <orig>` to `app: <new>`
// inside node.labels (if present). The `app` label is the convention
// every kubernetes-resources example uses for cross-resource wiring.
func renameLabelMap(metadataNode *yaml.Node, origName, newName string) {
	labels := mappingChild(metadataNode, "labels")
	if labels == nil {
		return
	}
	renameStringPath(labels, "app", origName, newName)
}

// stripConfighubAnnotations removes any annotation key under the
// `confighub.com/` prefix from metadata.annotations. cub injects
// `confighub.com/ResourceMergeID` on every Unit it stores; we don't
// want that bookkeeping landing in the operator's package source.
func stripConfighubAnnotations(metadataNode *yaml.Node) {
	annots := mappingChild(metadataNode, "annotations")
	if annots == nil || annots.Kind != yaml.MappingNode {
		return
	}
	kept := annots.Content[:0]
	for i := 0; i+1 < len(annots.Content); i += 2 {
		if !strings.HasPrefix(annots.Content[i].Value, "confighub.com/") {
			kept = append(kept, annots.Content[i], annots.Content[i+1])
		}
	}
	if len(kept) == 0 {
		// Empty annotations map → drop the field entirely.
		for i := 0; i+1 < len(metadataNode.Content); i += 2 {
			if metadataNode.Content[i].Value == "annotations" {
				metadataNode.Content = append(metadataNode.Content[:i], metadataNode.Content[i+2:]...)
				return
			}
		}
		return
	}
	annots.Content = kept
}

// appendToKustomization edits bases/default/kustomization.yaml to
// include filename in its resources: list. Idempotent — does
// nothing if filename is already listed. Uses `kustomize edit add
// resource` if the binary is on PATH; otherwise edits the YAML
// directly.
func appendToKustomization(baseDir, filename string) error {
	kustPath := filepath.Join(baseDir, "kustomization.yaml")
	data, err := os.ReadFile(kustPath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	resources, _ := doc["resources"].([]any)
	for _, r := range resources {
		if s, ok := r.(string); ok && s == filename {
			return nil // already present
		}
	}
	resources = append(resources, filename)
	doc["resources"] = resources
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(kustPath, out, 0o644)
}
