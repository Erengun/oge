//go:build !unix

package delivery

// lockTree has no cross-process lock off unix: Öge refuses Windows
// (ADR-0017), and this keeps the package building there.
func lockTree(r *Run, target string) (func(), error) { return func() {}, nil }
