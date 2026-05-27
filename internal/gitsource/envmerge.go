package gitsource

import (
	"regexp"
	"strings"
	"time"
)

// EnvDrift summarizes the difference between repo and stack .env after
// a sync. Persisted on stack_git_sources so the UI can render a
// "drift detected" banner without diffing client-side.
type EnvDrift struct {
	NewFromRepo    []string `json:"new_from_repo,omitempty"`    // repo has, stack didn't — added with repo's default
	NewFromCompose []string `json:"new_from_compose,omitempty"` // compose references, .env didn't have — added empty
	UserOnly       []string `json:"user_only,omitempty"`         // stack has, repo doesn't — left untouched
}

// envRefRe matches docker-compose variable references: ${KEY}, ${KEY:-x},
// ${KEY:?x}, $KEY (no braces, less common). Captures the bare key name.
var envRefRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)(?::[-+?][^}]*)?\}|\$([A-Z_][A-Z0-9_]*)\b`)

// envLineRe matches a KEY=value line in an .env file. Key has to start
// with a letter or underscore (POSIX-ish env-var rules). The value is
// captured raw — quoting/escaping is preserved verbatim.
var envLineRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$`)

// parseEnvKeys returns the ordered list of keys defined in an env file,
// plus a map from key to its raw `KEY=value` line (so the merger can
// reuse the repo's default value verbatim).
func parseEnvKeys(content string) ([]string, map[string]string) {
	keys := []string{}
	lines := map[string]string{}
	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := envLineRe.FindStringSubmatch(trimmed); m != nil {
			k := m[1]
			if _, ok := lines[k]; !ok {
				keys = append(keys, k)
			}
			lines[k] = trimmed
		}
	}
	return keys, lines
}

// composeRefs returns the unique ${VAR} keys referenced in compose.yaml,
// in first-seen order.
func composeRefs(composeYAML string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range envRefRe.FindAllStringSubmatch(composeYAML, -1) {
		key := m[1]
		if key == "" {
			key = m[2]
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// mergeEnvFiles produces the .env content to write into the stack dir
// after a git sync. Strategy:
//
//   - stack-.env content (verbatim, comments+order preserved) is the
//     baseline — values the operator set in the UI must survive.
//   - repo-.env keys that aren't in the stack-.env yet are appended at
//     the end with the repo's default value, marked with a comment line
//     so the operator sees what just arrived.
//   - compose `${VAR}` references that don't exist in either env file
//     are appended with empty value + a "from compose.yaml" comment so
//     a deploy doesn't fail with "variable not set".
//   - keys the operator added locally (in stack-.env, not in repo) are
//     untouched.
//
// The drift report lists each bucket so the UI can surface a banner.
func mergeEnvFiles(stackEnv, repoEnv, composeYAML string) (string, EnvDrift) {
	stackKeys, _ := parseEnvKeys(stackEnv)
	repoKeys, repoLines := parseEnvKeys(repoEnv)

	stackSet := map[string]bool{}
	for _, k := range stackKeys {
		stackSet[k] = true
	}
	repoSet := map[string]bool{}
	for _, k := range repoKeys {
		repoSet[k] = true
	}

	var drift EnvDrift
	for _, k := range repoKeys {
		if !stackSet[k] {
			drift.NewFromRepo = append(drift.NewFromRepo, k)
		}
	}
	for _, k := range stackKeys {
		if !repoSet[k] {
			drift.UserOnly = append(drift.UserOnly, k)
		}
	}

	out := strings.TrimRight(stackEnv, "\n")
	if len(drift.NewFromRepo) > 0 {
		if out != "" {
			out += "\n\n"
		}
		stamp := time.Now().UTC().Format("2006-01-02")
		out += "# Added by git sync on " + stamp + "\n"
		for _, k := range drift.NewFromRepo {
			out += repoLines[k] + "\n"
		}
	}

	// Compose ${VAR} that's now declared but missing from BOTH sides.
	// Treat them as missing so deploy doesn't blow up at compose-up.
	allKeys := map[string]bool{}
	for _, k := range stackKeys {
		allKeys[k] = true
	}
	for _, k := range drift.NewFromRepo {
		allKeys[k] = true
	}
	for _, k := range composeRefs(composeYAML) {
		if allKeys[k] {
			continue
		}
		drift.NewFromCompose = append(drift.NewFromCompose, k)
	}
	if len(drift.NewFromCompose) > 0 {
		if !strings.HasSuffix(out, "\n") && out != "" {
			out += "\n"
		}
		if out != "" {
			out += "\n"
		}
		out += "# Referenced by compose.yaml but not set — fill in via UI\n"
		for _, k := range drift.NewFromCompose {
			out += k + "=\n"
		}
	}
	if out == "" {
		// First-ever sync against an empty stack and an empty repo —
		// nothing to write, callers can short-circuit if they care.
		return "", drift
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, drift
}
