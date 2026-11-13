// Package version reports the semantic version of the gocode build.
package version

// Version is the version advertised by this build. Release binaries inject the
// real value at link time via
// -X github.com/mevarx/GoCode/internal/version.Version={{.Version}};
// development builds keep the default.
var Version = "dev"
