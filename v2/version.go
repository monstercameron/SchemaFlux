package schemaflux

// Version is the version of this library, a semantic version without a leading
// v. It is what the library names itself as in the instrumentation scope of its
// spans, so that a trace says which release produced it. It reads 2.0.0-dev
// until version 2 is released, and is changed only by a release.
const Version = "2.0.0-dev"
