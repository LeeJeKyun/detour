//go:build linux

package redirect

import "errors"

func New(rule Rule, verbose bool) (Redirector, error) {
	return nil, errors.New("linux support is not implemented yet")
}
