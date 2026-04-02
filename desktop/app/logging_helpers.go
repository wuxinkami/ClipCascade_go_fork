package app

import (
	"path/filepath"
	"strings"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
)

func clipboardLogNames(clipData *protocol.ClipboardData) string {
	return protocol.BracketedNames(protocol.ClipboardItemNames(clipData))
}

func historyItemLogNames(item *history.HistoryItem) string {
	if item == nil {
		return ""
	}

	switch item.Type {
	case constants.TypeFileStub:
		if manifest, err := protocol.DecodePayload[protocol.FileStubManifest](item.Payload); err == nil {
			if names := protocol.ManifestNames(manifest); len(names) > 0 {
				return protocol.BracketedNames(names)
			}
		}
		if names := pathBaseNames(item.LocalPaths); len(names) > 0 {
			return protocol.BracketedNames(names)
		}
		if name := strings.TrimSpace(item.DisplayName); name != "" {
			return protocol.BracketedNames([]string{name})
		}
	case constants.TypeImage, constants.TypeFileEager:
		if name := strings.TrimSpace(item.FileName); name != "" {
			return protocol.BracketedNames([]string{name})
		}
		if names := pathBaseNames(item.LocalPaths); len(names) > 0 {
			return protocol.BracketedNames(names)
		}
	}

	return ""
}

func pathLogNames(paths []string) string {
	return protocol.BracketedNames(pathBaseNames(paths))
}

func pathBaseNames(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	names := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		name := filepath.Base(path)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = path
		}
		names = append(names, name)
	}
	return names
}
