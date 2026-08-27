// Package scratchtest exists only to deliberately break the build, to
// check the AI CI Agent's CI-failure assessment. Safe to delete once the
// test PR is closed.
package scratchtest

func Broken() string {
	return "missing closing paren"
