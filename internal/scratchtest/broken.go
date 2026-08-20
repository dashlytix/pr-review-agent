// Package scratchtest exists only to deliberately break the build so the
// AI CI Agent workflow has a real failure to investigate. Safe to delete
// once the test PR is closed.
package scratchtest

func Broken() string {
	return "missing closing paren"
