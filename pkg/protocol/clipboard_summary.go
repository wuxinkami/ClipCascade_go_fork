package protocol

import (
	"strings"
)

// ManifestNames returns the best-effort file names represented by a file stub manifest.
func ManifestNames(manifest *FileStubManifest) []string {
	if manifest == nil {
		return nil
	}
	names := dedupeNames(manifest.TopLevelNames)
	if len(names) > 0 {
		return names
	}
	if name := strings.TrimSpace(manifest.DisplayName); name != "" {
		return []string{name}
	}
	return nil
}

// ClipboardItemNames returns best-effort item names for logging.
func ClipboardItemNames(clipData *ClipboardData) []string {
	if clipData == nil {
		return nil
	}

	switch clipData.Type {
	case "file_stub":
		manifest, err := DecodePayload[FileStubManifest](clipData.Payload)
		if err == nil {
			return ManifestNames(manifest)
		}
	case "image", "file_eager":
		if name := strings.TrimSpace(clipData.FileName); name != "" {
			return []string{name}
		}
	}

	return nil
}

// BracketedNames formats names as "[a] [b]" for logs.
func BracketedNames(names []string) string {
	filtered := dedupeNames(names)
	if len(filtered) == 0 {
		return ""
	}

	parts := make([]string, 0, len(filtered))
	for _, name := range filtered {
		parts = append(parts, "["+name+"]")
	}
	return strings.Join(parts, " ")
}

func dedupeNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}

	filtered := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		filtered = append(filtered, name)
	}
	return filtered
}
