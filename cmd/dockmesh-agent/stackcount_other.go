//go:build !unix

package main

// countStacks reports no stacks off Unix, where agents are not installed.
func countStacks(string) (int, error) { return 0, nil }
