//go:build !unix

package restore

func ownerOf(string) (fileOwner, bool) { return fileOwner{}, false }

func chownDirNoFollow(string, fileOwner) error { return nil }
