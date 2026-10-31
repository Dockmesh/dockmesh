package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Label is the version with the commit appended when the build stamped
// one, so two dev builds of different commits tell apart.
func Label() string {
	if Commit != "" && Commit != "none" {
		return Version + "+" + Commit
	}
	return Version
}
