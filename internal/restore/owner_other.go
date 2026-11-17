//go:build !unix

package restore

func ownerOf(string) (fileOwner, bool) { return fileOwner{}, false }
