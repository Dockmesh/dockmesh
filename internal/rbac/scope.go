package rbac

// ScopeMatchesHost decides whether a caller whose role is scoped to
// `userScopeTags` may act on a host whose tags are `hostTags`.
//
// Semantics:
//   - Empty / nil userScopeTags means "all hosts" — always match.
//   - Otherwise the caller needs at least one tag that the host also has
//     (OR semantics). This covers the common "member of any listed team"
//     pattern. AND semantics would need a separate role scope per tag.
//
// Passing a userScopeTags slice with values but the host having no
// matching tag deliberately returns false — the caller is restricted.
func ScopeMatchesHost(userScopeTags, hostTags []string) bool {
	if len(userScopeTags) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(hostTags))
	for _, t := range hostTags {
		have[t] = struct{}{}
	}
	for _, t := range userScopeTags {
		if _, ok := have[t]; ok {
			return true
		}
	}
	return false
}
