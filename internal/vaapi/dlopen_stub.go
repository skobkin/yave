//go:build !linux

package vaapi

import "errors"

func init() {
	loadLibVA = func() (*libvaHandle, error) {
		return nil, errors.New("VA-API device probing is only supported on Linux")
	}
}
