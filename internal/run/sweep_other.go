//go:build !unix

package run

import "os"

// processAlive assumes the process lives: Runs are refused here
// (ADR-0017), so nothing is swept.
func processAlive(int) bool { return true }

// lockRun is advisory on platforms without flock.
func lockRun(*os.File) error { return nil }

func unlockRun(*os.File) {}

func runLocked(string) bool { return false }
