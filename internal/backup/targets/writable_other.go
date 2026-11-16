//go:build !unix

package targets

func canCreateIn(string) error { return nil }
