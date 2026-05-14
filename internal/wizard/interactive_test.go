package wizard

import (
	"reflect"
	"sort"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

func TestResolvePreset(t *testing.T) {
	pkg := &api.Package{
		Spec: api.PackageSpec{
			Components: []api.Component{
				{Name: "a", Default: true},
				{Name: "b"},
				{Name: "c", Default: true},
			},
		},
	}
	cases := []struct {
		preset string
		want   []string
		err    bool
	}{
		{PresetMinimal, nil, false},
		{PresetDefault, []string{"a", "c"}, false},
		{PresetAll, []string{"a", "b", "c"}, false},
		{PresetSelected, nil, true},
		{"bogus", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.preset, func(t *testing.T) {
			got, err := ResolvePreset(pkg, tc.preset)
			if tc.err {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultBaseName(t *testing.T) {
	pkg := &api.Package{
		Spec: api.PackageSpec{
			Bases: []api.Base{
				{Name: "first"},
				{Name: "second", Default: true},
				{Name: "third"},
			},
		},
	}
	if got := defaultBaseName(pkg, nil); got != "second" {
		t.Errorf("with no prior, want package default: got %q", got)
	}
	prior := &PriorState{Selection: &api.Selection{Spec: api.SelectionSpec{Base: "third"}}}
	if got := defaultBaseName(pkg, prior); got != "third" {
		t.Errorf("with prior selection, want prior: got %q", got)
	}
	priorBogus := &PriorState{Selection: &api.Selection{Spec: api.SelectionSpec{Base: "removed"}}}
	if got := defaultBaseName(pkg, priorBogus); got != "second" {
		t.Errorf("with prior selection naming a removed base, want package default: got %q", got)
	}
	emptyPkg := &api.Package{Spec: api.PackageSpec{Bases: []api.Base{{Name: "only"}}}}
	if got := defaultBaseName(emptyPkg, nil); got != "only" {
		t.Errorf("with no default flag, want first base: got %q", got)
	}
}

func TestDefaultPreset(t *testing.T) {
	if got := defaultPreset(nil); got != PresetDefault {
		t.Errorf("no prior, want default preset: got %q", got)
	}
	prior := &PriorState{Selection: &api.Selection{Spec: api.SelectionSpec{Components: []string{"a"}}}}
	if got := defaultPreset(prior); got != PresetSelected {
		t.Errorf("prior with components, want selected: got %q", got)
	}
	priorEmpty := &PriorState{Selection: &api.Selection{}}
	if got := defaultPreset(priorEmpty); got != PresetMinimal {
		t.Errorf("prior with no components, want minimal: got %q", got)
	}
}

func TestRawAnswersFromPrior(t *testing.T) {
	pkg := &api.Package{}
	prior := &PriorState{
		Selection: &api.Selection{Spec: api.SelectionSpec{
			Base:       "default",
			Components: []string{"a", "b"},
		}},
		Inputs: &api.Inputs{Spec: api.InputsSpec{
			Namespace: "demo",
			Values: map[string]any{
				"replicas":  3,
				"name":      "hello",
				"enabled":   true,
				"hostnames": []any{"a.example", "b.example"},
			},
		}},
	}
	raw := RawAnswersFromPrior(pkg, prior)
	if raw.BaseName != "default" {
		t.Errorf("BaseName = %q", raw.BaseName)
	}
	if !reflect.DeepEqual(raw.SelectedComponents, []string{"a", "b"}) {
		t.Errorf("SelectedComponents = %v", raw.SelectedComponents)
	}
	if raw.Namespace != "demo" {
		t.Errorf("Namespace = %q", raw.Namespace)
	}
	if raw.Inputs["replicas"] != "3" {
		t.Errorf("replicas not stringified: %q", raw.Inputs["replicas"])
	}
	if raw.Inputs["enabled"] != "true" {
		t.Errorf("bool not stringified: %q", raw.Inputs["enabled"])
	}
	if raw.Inputs["hostnames"] != "a.example,b.example" {
		t.Errorf("list not joined: %q", raw.Inputs["hostnames"])
	}
}

func TestStringifyAny(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"x", "x"},
		{true, "true"},
		{42, "42"},
		{int64(42), "42"},
		{3.0, "3"},
		{3.14, "3.14"},
		{[]any{"a", "b"}, "a,b"},
	}
	for _, tc := range cases {
		if got := stringifyAny(tc.in); got != tc.want {
			t.Errorf("stringifyAny(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
