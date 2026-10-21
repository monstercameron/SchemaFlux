// Package guard holds the tests that hold every public function of version 2 to
// the library's rules at once: one Record per call, a classified error and no
// value beside it, no payload in any Record, error or log line, the client's
// timeout, the data policy's floor, the surface snapshots and the import graph.
// It has no non-test code: it is not part of the library, and it sits under
// internal so that a failing guard fails only its own package while the
// functions are still being written.
package guard
