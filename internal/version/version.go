// Package version exposes the installer build version. The default is "dev";
// release builds set it via -ldflags:
//
//	go build -ldflags "-X github.com/confighubai/installer/internal/version.Version=0.4.0" ./cmd/installer
package version

// Version is overridden at build time. Recorded in ConfigBlob.BundleInfo so
// each artifact carries the installer that produced it.
var Version = "dev"
