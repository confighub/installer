// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"testing"

	"github.com/confighub/sdk/configkit/envkit"

	"github.com/confighub/installer/pkg/api"
)

// Validators see an env-mode ConfigMap's values as they are, including ones
// an env file has to quote.
func TestExtractAppConfigBody_EnvModeQuotesValues(t *testing.T) {
	shape := &appConfigCarrierShape{Kind: "ConfigMap"}
	shape.Metadata.Annotations = map[string]string{
		api.AnnotationToolchain:     "AppConfig/Env",
		api.AnnotationAppConfigMode: api.AppConfigModeEnv,
	}
	shape.Data = map[string]string{
		"ADMIN_JWK": `{"kty":"OKP","x":"abc"}`,
		"GREETING":  "hello world",
		"HASHED":    "a # b",
		"QUOTED":    "'single'",
		"PORT":      "5432",
	}
	body, err := extractAppConfigBody(shape)
	if err != nil {
		t.Fatalf("extractAppConfigBody: %v", err)
	}
	entries, err := envkit.ParseEnvEntries(body)
	if err != nil {
		t.Fatalf("ParseEnvEntries: %v", err)
	}
	if len(entries) != len(shape.Data) {
		t.Errorf("got %d entries, want %d:\n%s", len(entries), len(shape.Data), body)
	}
	for _, e := range entries {
		if want := shape.Data[e.Key]; e.Value != want {
			t.Errorf("%s = %q, want %q\nbody:\n%s", e.Key, e.Value, want, body)
		}
	}
}
