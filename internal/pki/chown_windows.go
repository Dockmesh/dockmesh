package pki

import "os"

func chownLike(*os.File, string) error { return nil }

const oNoFollow = 0
