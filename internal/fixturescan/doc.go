// Package fixturescan holds a test that fails when a committed fixture or
// research note contains something that looks like a credential, an email
// address or a personal home path (ADR-0017: recorded sessions are
// re-redacted before they reach main). It scans every testdata directory
// in the module and docs/research, and reports only file, line and rule,
// never the matched text.
package fixturescan
