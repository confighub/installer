// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

// Annotation keys on a ConfigMap that carries AppConfig. They are what
// ConfigHub's upload reads, whatever produced the ConfigMap. Toolchain is the
// only one authors write by hand; the transformer derives mode and source-key
// from the ConfigMap's data: shape, and mutable and name from whether
// kustomize will hash its name. Authors can pre-set any of them to override
// the inference.
const (
	AnnotationToolchain          = "confighub.com/toolchain"
	AnnotationAppConfigMode      = "confighub.com/appconfig-mode"
	AnnotationAppConfigSourceKey = "confighub.com/appconfig-source-key"
	AnnotationAppConfigMutable   = "confighub.com/appconfig-mutable"
	AnnotationAppConfigName      = "confighub.com/appconfig-name"
)

// Values of AnnotationAppConfigMode: whether the ConfigMap holds the AppConfig
// as one file, under AnnotationAppConfigSourceKey, or as key/value entries.
const (
	AppConfigModeFile = "file"
	AppConfigModeEnv  = "env"
)
