//go:build !unix

package run

// processAlive assumes the process lives: Runs are refused here
// (ADR-0017), so nothing is swept.
func processAlive(int) bool { return true }

// sweepGroup kills nothing here: Runs are refused (ADR-0017).
func sweepGroup(int, int, string) bool { return false }
