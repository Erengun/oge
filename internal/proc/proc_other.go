//go:build !unix

package proc

import "os/exec"

// Runs are refused on these platforms (ADR-0017); this only keeps the
// cross-build working.
func setGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func group(*exec.Cmd) int { return 0 }

func groupGone(int) bool { return true }

func termGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
