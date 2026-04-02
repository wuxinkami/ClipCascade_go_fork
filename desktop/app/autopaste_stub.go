//go:build !windows && !linux && !darwin

package app

func simulateAutoPaste() error {
	return ErrAutoPasteUnavailable
}
