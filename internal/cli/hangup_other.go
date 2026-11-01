//go:build !unix

package cli

// redirectStdio does nothing where there is no SIGHUP.
func redirectStdio(string) error { return nil }
