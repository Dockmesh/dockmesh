//go:build !unix

package main

func codeGroup(string) int { return 0 }
