//go:build linux

package system

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// listMounts parses /proc/self/mountinfo. The major:minor field is the
// dedupe key, so bind mounts of one filesystem collapse into one entry.
func listMounts() []mountEntry {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseMountinfo(bufio.NewScanner(f))
}

func parseMountinfo(sc *bufio.Scanner) []mountEntry {
	var out []mountEntry
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, fl := range fields {
			if fl == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || len(fields) < sep+3 {
			continue
		}
		out = append(out, mountEntry{
			key:    fields[2],
			point:  unescapeMount(fields[4]),
			fstype: fields[sep+1],
			device: unescapeMount(fields[sep+2]),
		})
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 for space) the kernel
// uses in mount tables.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func deviceKey(path string) (string, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", false
	}
	dev := uint64(st.Dev) //nolint:unconvert // Dev is uint32 on some arches
	major := ((dev >> 8) & 0xfff) | ((dev >> 32) &^ 0xfff)
	minor := (dev & 0xff) | ((dev >> 12) &^ 0xff)
	return strconv.FormatUint(major, 10) + ":" + strconv.FormatUint(minor, 10), true
}
