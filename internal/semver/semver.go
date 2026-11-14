// Package semver parses and orders Dockmesh release versions.
package semver

import "strings"

// Version is a parsed semantic version without build metadata.
type Version struct {
	core [3]int
	pre  []string
}

// Parse parses "1.2.3", "v1.2.3-rc.1" or "1.2.3+build"; build
// metadata is ignored.
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return Version{}, false
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if !validIdent(id) {
				return Version{}, false
			}
		}
	}
	return v, true
}

// validIdent reports whether id is a semver pre-release identifier:
// non-empty, ASCII letters, digits and hyphens.
func validIdent(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '-' {
			return false
		}
	}
	return true
}

// number parses a non-empty run of ASCII digits.
func number(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// Compare orders by semver precedence: a pre-release sorts before its
// release, numeric identifiers numerically and below alphanumeric ones.
func (a Version) Compare(b Version) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			return cmpInt(a.core[i], b.core[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		an, aNum := number(a.pre[i])
		bn, bNum := number(b.pre[i])
		switch {
		case aNum && bNum:
			if an != bn {
				return cmpInt(an, bn)
			}
		case aNum:
			return -1
		case bNum:
			return 1
		default:
			if c := strings.Compare(a.pre[i], b.pre[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Prerelease reports whether v carries a pre-release suffix such as -rc.1.
func (a Version) Prerelease() bool { return len(a.pre) > 0 }

// Newer reports whether a is a higher version than b. Either failing to
// parse makes it false, so an unparsable version is never an upgrade.
func Newer(a, b string) bool {
	va, ok := Parse(a)
	if !ok {
		return false
	}
	vb, ok := Parse(b)
	if !ok {
		return false
	}
	return va.Compare(vb) > 0
}
