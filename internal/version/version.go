package version

// ApplicationVersion is the injected git commit hash for this build.
// It is set via -ldflags during compilation.
var ApplicationVersion = "unknown"
