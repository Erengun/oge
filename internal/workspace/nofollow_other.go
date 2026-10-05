//go:build !unix

package workspace

// oNoFollow is unavailable here; the walk's Lstat already excluded links.
const oNoFollow = 0
