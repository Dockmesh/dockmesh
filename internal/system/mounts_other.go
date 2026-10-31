//go:build !linux && !darwin

package system

func listMounts() []mountEntry { return nil }

func deviceKey(string) (string, bool) { return "", false }

func readDisk(string) (total, used uint64, percent float64) { return 0, 0, 0 }
