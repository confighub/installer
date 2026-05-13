package api

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// SniffKind returns the apiVersion + kind of a single YAML doc without parsing
// the full body. Returns ("", "", err) on parse failure.
func SniffKind(data []byte) (apiVersion, kind string, err error) {
	var h Header
	if err := yaml.Unmarshal(data, &h); err != nil {
		return "", "", fmt.Errorf("sniff: %w", err)
	}
	return h.APIVersion, h.Kind, nil
}

// ParsePackage parses installer.yaml bytes into a Package, validating the
// header.
func ParsePackage(data []byte) (*Package, error) {
	var p Package
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse Package: %w", err)
	}
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}
	if p.APIVersion != APIVersion {
		return nil, fmt.Errorf("unsupported apiVersion %q (want %q)", p.APIVersion, APIVersion)
	}
	if p.Kind != KindPackage {
		return nil, fmt.Errorf("unexpected kind %q (want %q)", p.Kind, KindPackage)
	}
	if p.Metadata.Name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}
	if len(p.Spec.Bases) == 0 {
		return nil, fmt.Errorf("spec.bases must declare at least one base")
	}
	defaults := 0
	for _, b := range p.Spec.Bases {
		if b.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return nil, fmt.Errorf("only one base may set default: true")
	}
	return &p, nil
}

// ParseSelection parses selection.yaml bytes into a Selection.
func ParseSelection(data []byte) (*Selection, error) {
	var s Selection
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse Selection: %w", err)
	}
	if s.Kind != "" && s.Kind != KindSelection {
		return nil, fmt.Errorf("unexpected kind %q (want %q)", s.Kind, KindSelection)
	}
	return &s, nil
}

// ParseInputs parses inputs.yaml bytes into Inputs.
func ParseInputs(data []byte) (*Inputs, error) {
	var i Inputs
	if err := yaml.Unmarshal(data, &i); err != nil {
		return nil, fmt.Errorf("parse Inputs: %w", err)
	}
	if i.Kind != "" && i.Kind != KindInputs {
		return nil, fmt.Errorf("unexpected kind %q (want %q)", i.Kind, KindInputs)
	}
	return &i, nil
}

// ParseFacts parses facts.yaml bytes into Facts.
func ParseFacts(data []byte) (*Facts, error) {
	var f Facts
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse Facts: %w", err)
	}
	if f.Kind != "" && f.Kind != KindFacts {
		return nil, fmt.Errorf("unexpected kind %q (want %q)", f.Kind, KindFacts)
	}
	return &f, nil
}

// ParseFunctionChain parses function-chain.yaml bytes.
func ParseFunctionChain(data []byte) (*FunctionChain, error) {
	var c FunctionChain
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse FunctionChain: %w", err)
	}
	if c.Kind != "" && c.Kind != KindFunctionChain {
		return nil, fmt.Errorf("unexpected kind %q (want %q)", c.Kind, KindFunctionChain)
	}
	return &c, nil
}

// MarshalYAML emits a deterministic, header-first YAML doc for any of the
// installer kinds.
func MarshalYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SplitMultiDoc returns each YAML doc in data as a separate []byte. Empty
// docs are skipped.
func SplitMultiDoc(data []byte) ([][]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out [][]byte
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if node.Kind == 0 {
			continue
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&node); err != nil {
			return nil, err
		}
		_ = enc.Close()
		if len(bytes.TrimSpace(buf.Bytes())) == 0 {
			continue
		}
		out = append(out, buf.Bytes())
	}
	return out, nil
}
