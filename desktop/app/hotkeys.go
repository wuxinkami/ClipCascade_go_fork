package app

type hotkeyBindings struct {
	sendCurrentClipboard func()
	pastePlaceholder     func()
	pasteRealContent     func()
}

type hotkeyManager interface {
	Start(bindings hotkeyBindings) error
	Stop() error
}
