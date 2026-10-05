//go:build !unix

package run

// processAlive assumes the process lives: Runs are refused here
// (ADR-0017), so nothing is swept.
func processAlive(int) bool { return true }
