package app

import (
	"log/slog"

	"github.com/clipcascade/desktop/ui"
)

func (a *Application) triggerOpenLogWindow() {
	if a == nil {
		return
	}
	if err := ui.OpenLogWindow(a.logFilePath); err != nil {
		slog.Warn("application: failed to open log window", "path", a.logFilePath, "error", err)
		a.recordControlEvent("console", "Open log window failed: "+err.Error())
		return
	}
	a.recordControlEvent("console", "Opened log window")
}
