//go:build !linux && !darwin

package scanner

func freeBytes(string) (uint64, bool) { return 0, false }
