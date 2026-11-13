//go:build !unix

package main

func readRootOwnedFile(string) ([]byte, bool) { return nil, false }
