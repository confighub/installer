package wizard

import (
	"reflect"
	"sort"
	"testing"

	"github.com/confighubai/installer/pkg/api"
)

func TestDiffInputs_Carry(t *testing.T) {
	old := pkg(input("greeting", "string", "hello", false))
	nu := pkg(input("greeting", "string", "hello", false))
	prior := map[string]any{"greeting": "hi"}
	d := DiffInputs(old, nu, prior)
	if d.Carry["greeting"] != "hi" {
		t.Errorf("expected carry, got %v", d.Carry)
	}
	if len(d.NewRequired) != 0 || len(d.Dropped) != 0 || len(d.AdoptedDefaults) != 0 {
		t.Errorf("unexpected non-carry buckets: %+v", d)
	}
}

func TestDiffInputs_AdoptedDefault(t *testing.T) {
	old := pkg()
	nu := pkg(input("replicas", "int", 2, false))
	d := DiffInputs(old, nu, nil)
	if d.AdoptedDefaults["replicas"] != 2 {
		t.Errorf("expected default adopted, got %+v", d.AdoptedDefaults)
	}
	if len(d.NewRequired) != 0 {
		t.Errorf("default should not show as required: %+v", d.NewRequired)
	}
}

func TestDiffInputs_NewRequired(t *testing.T) {
	old := pkg()
	nu := pkg(requiredInput("license", "string"))
	d := DiffInputs(old, nu, nil)
	if len(d.NewRequired) != 1 || d.NewRequired[0].Name != "license" {
		t.Errorf("expected NewRequired, got %+v", d.NewRequired)
	}
}

func TestDiffInputs_Dropped(t *testing.T) {
	old := pkg(input("legacy", "string", "x", false))
	nu := pkg()
	d := DiffInputs(old, nu, map[string]any{"legacy": "anything"})
	if !reflect.DeepEqual(d.Dropped, []string{"legacy"}) {
		t.Errorf("expected dropped=[legacy], got %v", d.Dropped)
	}
	if _, present := d.Carry["legacy"]; present {
		t.Errorf("dropped key should not appear in carry")
	}
}

func TestDiffInputs_TypeChanged(t *testing.T) {
	old := pkg(input("port", "string", "8080", false))
	nu := pkg(input("port", "int", 8080, false))
	d := DiffInputs(old, nu, map[string]any{"port": "8080"})
	if len(d.TypeChanged) != 1 || d.TypeChanged[0].Name != "port" {
		t.Errorf("expected TypeChanged, got %+v", d.TypeChanged)
	}
	// TypeChanged inputs must NOT also appear in Carry — operator
	// must explicitly re-answer.
	if _, present := d.Carry["port"]; present {
		t.Errorf("type-changed input should not carry: %+v", d.Carry)
	}
}

func TestDiffInputs_TypeImplicitStringIsCompatible(t *testing.T) {
	// Type="" defaults to "string"; an upgrade that adds explicit
	// type: string for a previously unspecified input is not a type
	// change.
	old := pkg(input("name", "", "x", false))
	nu := pkg(input("name", "string", "x", false))
	d := DiffInputs(old, nu, map[string]any{"name": "user-supplied"})
	if d.Carry["name"] != "user-supplied" {
		t.Errorf("expected carry, got %+v", d)
	}
	if len(d.TypeChanged) != 0 {
		t.Errorf("string vs '' should be compatible: %+v", d.TypeChanged)
	}
}

func TestDiffComponents_DefaultPresetAdopted(t *testing.T) {
	old := pkg(component("a", true), component("b", false))
	nu := pkg(component("a", true), component("b", false), component("c", true))
	// Prior selection matches old's default preset (just "a").
	d := DiffComponents(old, nu, []string{"a"})
	if !d.DefaultPresetDetected {
		t.Errorf("expected default preset to be detected")
	}
	sort.Strings(d.Components)
	if !reflect.DeepEqual(d.Components, []string{"a", "c"}) {
		t.Errorf("expected new defaults adopted, got %v", d.Components)
	}
	if !reflect.DeepEqual(d.AdoptedNewDefaults, []string{"c"}) {
		t.Errorf("expected AdoptedNewDefaults=[c], got %v", d.AdoptedNewDefaults)
	}
}

func TestDiffComponents_NonDefaultFiltersExisting(t *testing.T) {
	old := pkg(component("a", false), component("b", false), component("legacy", false))
	nu := pkg(component("a", false), component("b", false))
	d := DiffComponents(old, nu, []string{"a", "legacy"})
	if d.DefaultPresetDetected {
		t.Errorf("non-default selection should not detect default preset")
	}
	if !reflect.DeepEqual(d.Components, []string{"a"}) {
		t.Errorf("expected legacy filtered out, got %v", d.Components)
	}
	if !reflect.DeepEqual(d.RemovedFromPrior, []string{"legacy"}) {
		t.Errorf("expected RemovedFromPrior=[legacy], got %v", d.RemovedFromPrior)
	}
}

func TestDiffComponents_EmptyPriorMatchesEmptyDefaults(t *testing.T) {
	// A package with no default-flagged components: empty selection
	// matches default preset (which is also empty).
	old := pkg(component("a", false), component("b", false))
	nu := pkg(component("a", true)) // new package now flags a as default
	d := DiffComponents(old, nu, nil)
	if !d.DefaultPresetDetected {
		t.Errorf("empty prior + empty old defaults should detect default preset")
	}
	if !reflect.DeepEqual(d.Components, []string{"a"}) {
		t.Errorf("expected new defaults [a], got %v", d.Components)
	}
}

func TestFormatNewRequiredHint(t *testing.T) {
	hint := FormatNewRequiredHint("/tmp/wd", []api.Input{
		{Name: "license", Type: "string", Prompt: "Your license key", Required: true},
		{Name: "port", Type: "int", Required: true},
	})
	if hint == "" {
		t.Fatal("expected non-empty hint")
	}
	for _, want := range []string{"license", "port", "Your license key", "/tmp/wd", "installer wizard"} {
		if !contains(hint, want) {
			t.Errorf("hint missing %q\n%s", want, hint)
		}
	}
	if FormatNewRequiredHint("/wd", nil) != "" {
		t.Errorf("empty list should produce empty hint")
	}
}

// --- helpers ----------------------------------------------------------------

func pkg(inputs ...any) *api.Package {
	p := &api.Package{Spec: api.PackageSpec{}}
	for _, in := range inputs {
		switch v := in.(type) {
		case api.Input:
			p.Spec.Inputs = append(p.Spec.Inputs, v)
		case api.Component:
			p.Spec.Components = append(p.Spec.Components, v)
		}
	}
	return p
}

func input(name, typ string, def any, required bool) api.Input {
	return api.Input{Name: name, Type: typ, Default: def, Required: required}
}

func requiredInput(name, typ string) api.Input {
	return api.Input{Name: name, Type: typ, Required: true}
}

func component(name string, dflt bool) api.Component {
	return api.Component{Name: name, Default: dflt}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
