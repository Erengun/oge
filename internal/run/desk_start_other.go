//go:build unix && !darwin && !linux

package run

// processStart can't be read here: the pid alone identifies the process.
func processStart(int) string { return "" }
