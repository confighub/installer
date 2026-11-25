// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer_test

import (
	"bytes"
	"testing"

	"github.com/confighub/installer/pkg/api"
	"github.com/confighub/installer/pkg/installer"
)

const writeTestRecord = `configHub:
  configSchema: InstallerRecord
  configName: installer
package:
  name: k8sbot
  installerMetadata:
    version: 0.2.9
    kubeVersion: '>= 1.28'
  spec:
    bases:
    - name: default
      path: bases/default
      default: true
      externalRequires:
      - kind: CRD
        name: applications.argoproj.io
        version: '>= v2.0'
    components:
    - name: argocd
      path: components/argocd
      default: true
      description: |
        Drive Argo CD.
    - name: flux
      path: components/flux
      description: |
        Drive Flux 2.6 or later: request a reconciliation of the
        OCIRepository that pulls a Space.
      transformers:
      - toolchain: Kubernetes/YAML
        invocations:
        - name: set-yq
          args: ['.x = 1']
        description: Not selected, so not shown.
    componentGroups:
    - name: controller
      components: [argocd, flux]
      required: true
    inputs:
    - name: channel
      type: string
      default: ""
    - name: replicas
      type: int
      default: 1
    transformers:
    - toolchain: Kubernetes/YAML
      invocations:
      - name: set-namespace
        args: ['{{ .Namespace }}']
      description: |
        Put k8sbot in the install
        namespace.
    validators:
    - toolchain: Kubernetes/YAML
      invocations:
      - name: vet-schemas
      - name: vet-pod-security-standard
        args: [restricted]
selection:
  base: default
  components: [argocd]
inputs:
  namespace: k8sbot
  values:
    channel: ops/k8sbot
    retired: old
  imageOverrides:
    k8sbot: ghcr.io/confighub/k8sbot:0.2.9
source:
  ref: oci://ghcr.io/confighub/packages/k8sbot:0.2.9
  digest: sha256:101c9b
facts:
  values:
    serverURL: https://hub.confighub.com
lock:
  package:
    name: k8sbot
  resolved:
  - name: crds
    ref: oci://ghcr.io/confighub/packages/argo-crds:2.1.0
    digest: sha256:abc
    version: 2.1.0
    requestedBy: [root]
`

const writeTestWant = `Package:             k8sbot
Version:             0.2.9
Space:               nonprod-k8sbot
Source:              oci://ghcr.io/confighub/packages/k8sbot:0.2.9
Digest:              sha256:101c9b
Namespace:           k8sbot
Base:                default
Kubernetes version:  >= 1.28

Components:
  * argocd  (one of controller)
        Drive Argo CD.
    flux  (one of controller)
        Drive Flux 2.6 or later: request a reconciliation of the OCIRepository
        that pulls a Space.
  (* selected)

Inputs:
  INPUT     VALUE       DEFAULT
  channel   ops/k8sbot  ""
  replicas              1
  retired   old

Image overrides:
  IMAGE   OVERRIDE
  k8sbot  ghcr.io/confighub/k8sbot:0.2.9

Facts:
  FACT       VALUE
  serverURL  https://hub.confighub.com

Dependencies:
  DEPENDENCY  VERSION  REFERENCE                                         REQUESTED BY
  crds        2.1.0    oci://ghcr.io/confighub/packages/argo-crds:2.1.0  root

Cluster requirements:
  KIND  NAME                      VERSION  REQUIRED BY
  CRD   applications.argoproj.io  >= v2.0  base default

Transformers:
  FROM     FUNCTIONS      DESCRIPTION
  package  set-namespace  Put k8sbot in the install namespace.

Validators:
  vet-schemas, vet-pod-security-standard restricted
`

func TestWriteRecord(t *testing.T) {
	rec, err := api.ParseRecord([]byte(writeTestRecord))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := installer.WriteRecord(&out, &installer.InstalledRecord{Space: "nonprod-k8sbot", Record: rec}); err != nil {
		t.Fatal(err)
	}
	if out.String() != writeTestWant {
		t.Errorf("WriteRecord:\n%s\nwant:\n%s", out.String(), writeTestWant)
	}
}
