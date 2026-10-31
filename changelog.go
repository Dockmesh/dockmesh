// Package dockmesh holds files from the repository root that the server
// binary embeds.
package dockmesh

import _ "embed"

// Changelog is CHANGELOG.md as of the build; the server shows the
// running version's section as "what's new".
//
//go:embed CHANGELOG.md
var Changelog string
