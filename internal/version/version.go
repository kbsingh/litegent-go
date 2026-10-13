// Package version exposes the Litegent build version string.
package version

// Version is the Litegent release version. It can be overridden at build
// time via:
//
//	go build -ldflags "-X litegent/internal/version.Version=1.2.3"
var Version = "0.1.0"
