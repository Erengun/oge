// Package fixturescan holds a test that fails when any file git tracks
// contains something that looks like a credential, an email address or a
// personal home path (ADR-0017: recorded sessions are re-redacted before
// they reach main). It skips binaries, go.sum and the vendored third-party
// skills. Known-safe matches, such as the synthetic tokens in tests of
// credential handling, are listed one by one with a reason. Failures name
// only file, line and rule, never the matched text.
package fixturescan
