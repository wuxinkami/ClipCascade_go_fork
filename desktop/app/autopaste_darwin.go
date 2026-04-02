//go:build darwin

package app

import (
	"context"
	"os/exec"
	"time"
)

const autoPasteWait = 150 * time.Millisecond
const autoPasteExecTimeout = 500 * time.Millisecond

func simulateAutoPaste() error {
	time.Sleep(autoPasteWait)
	ctx, cancel := context.WithTimeout(context.Background(), autoPasteExecTimeout)
	defer cancel()
	if err := exec.CommandContext(
		ctx,
		"osascript",
		"-e",
		`tell application "System Events" to keystroke "v" using command down`,
	).Run(); err != nil {
		return ErrAutoPasteUnavailable
	}
	return nil
}
