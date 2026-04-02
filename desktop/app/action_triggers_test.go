package app

import (
	"errors"
	"testing"
)

func TestTriggerSendCurrentClipboardInvokesSendAction(t *testing.T) {
	called := false
	app := &Application{}
	orig := notifyFn
	notifyFn = func(title, message string) {}
	t.Cleanup(func() { notifyFn = orig })

	appSend := appSendCurrentClipboard
	appSendCurrentClipboard = func(a *Application) error {
		if a != app {
			t.Fatalf("got application %p, want %p", a, app)
		}
		called = true
		return nil
	}
	t.Cleanup(func() { appSendCurrentClipboard = appSend })

	app.triggerSendCurrentClipboard()

	if !called {
		t.Fatal("send action was not called")
	}
}

func TestTriggerSendCurrentClipboardFailureNotifies(t *testing.T) {
	app := &Application{}
	wantErr := errors.New("boom")
	notified := false

	origNotify := notifyFn
	notifyFn = func(title, message string) {
		notified = true
	}
	t.Cleanup(func() { notifyFn = origNotify })

	origSend := appSendCurrentClipboard
	appSendCurrentClipboard = func(a *Application) error {
		return wantErr
	}
	t.Cleanup(func() { appSendCurrentClipboard = origSend })

	app.triggerSendCurrentClipboard()

	// 发送失败后不应弹出系统通知（只记录日志）
	if notified {
		t.Fatal("should not notify on send failure")
	}
}

func TestTriggerReplayActiveHistoryItemInvokesReplayAction(t *testing.T) {
	called := false
	app := &Application{}

	origNotify := notifyFn
	notifyFn = func(title, message string) {}
	t.Cleanup(func() { notifyFn = origNotify })

	origReplay := appReplaySharedClipboardItem
	appReplaySharedClipboardItem = func(a *Application, mode ReplayMode) (ReplayResult, error) {
		if a != app {
			t.Fatalf("got application %p, want %p", a, app)
		}
		if mode != ReplayModeClipboardImmediate {
			t.Fatalf("mode = %q, want %q", mode, ReplayModeClipboardImmediate)
		}
		called = true
		return ReplayResult{Action: replayActionClipboardStaged}, nil
	}
	t.Cleanup(func() { appReplaySharedClipboardItem = origReplay })

	app.triggerReplayActiveHistoryItem()

	if !called {
		t.Fatal("replay action was not called")
	}
}

func TestTriggerPasteRealContentHistoryItemInvokesReplayAction(t *testing.T) {
	called := false
	app := &Application{}

	origNotify := notifyFn
	notifyFn = func(title, message string) {}
	t.Cleanup(func() { notifyFn = origNotify })

	origReplay := appReplaySharedClipboardItem
	appReplaySharedClipboardItem = func(a *Application, mode ReplayMode) (ReplayResult, error) {
		if a != app {
			t.Fatalf("got application %p, want %p", a, app)
		}
		if mode != ReplayModeSystemClipboardPaste {
			t.Fatalf("mode = %q, want %q", mode, ReplayModeSystemClipboardPaste)
		}
		called = true
		return ReplayResult{Action: replayActionClipboardStaged}, nil
	}
	t.Cleanup(func() { appReplaySharedClipboardItem = origReplay })

	app.triggerPasteRealContentHistoryItem()

	if !called {
		t.Fatal("replay action was not called")
	}
}

func TestTriggerReplayActiveHistoryItemNotifiesDownloadStarted(t *testing.T) {
	app := &Application{}
	notified := false

	origNotify := notifyFn
	notifyFn = func(title, message string) {
		notified = true
	}
	t.Cleanup(func() { notifyFn = origNotify })

	origReplay := appReplaySharedClipboardItem
	appReplaySharedClipboardItem = func(a *Application, mode ReplayMode) (ReplayResult, error) {
		return ReplayResult{Action: replayActionDownloadRequested, Message: "Started file transfer for active item"}, nil
	}
	t.Cleanup(func() { appReplaySharedClipboardItem = origReplay })

	app.triggerReplayActiveHistoryItem()

	// 热键触发不再发通知，通知只在文件传输最终完成时发出
	if notified {
		t.Fatal("should not notify on replay trigger")
	}
}

func TestTriggerPasteRealContentHistoryItemNotifiesManualPasteFallback(t *testing.T) {
	app := &Application{}
	notified := false

	origNotify := notifyFn
	notifyFn = func(title, message string) {
		notified = true
	}
	t.Cleanup(func() { notifyFn = origNotify })

	origReplay := appReplaySharedClipboardItem
	appReplaySharedClipboardItem = func(a *Application, mode ReplayMode) (ReplayResult, error) {
		return ReplayResult{
			Action:              replayActionClipboardStaged,
			ManualPasteRequired: true,
			Message:             "Real content staged. Press Ctrl+V manually.",
		}, nil
	}
	t.Cleanup(func() { appReplaySharedClipboardItem = origReplay })

	app.triggerPasteRealContentHistoryItem()

	// 热键触发不再发通知
	if notified {
		t.Fatal("should not notify on replay trigger")
	}
}

func TestTriggerOpenHistoryPanelInvokesOpenHelper(t *testing.T) {
	app := &Application{}
	called := false

	origOpen := appOpenHistoryPanel
	appOpenHistoryPanel = func(a *Application) {
		if a != app {
			t.Fatalf("got application %p, want %p", a, app)
		}
		called = true
	}
	t.Cleanup(func() { appOpenHistoryPanel = origOpen })

	app.triggerOpenHistoryPanel()

	if !called {
		t.Fatal("open history panel helper was not called")
	}
}
