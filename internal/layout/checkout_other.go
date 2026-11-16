//go:build !unix

package layout

import "errors"

// TrustedCheckout refuses every directory: ownership cannot be checked
// off Unix.
func TrustedCheckout(string, int) error {
	return errors.New("the data of a source run is only used on Unix")
}
