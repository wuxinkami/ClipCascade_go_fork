package protocol

import "testing"

func TestClipboardItemNamesUsesManifestNames(t *testing.T) {
	clipData, err := NewClipboardDataWithPayload("file_stub", FileStubManifest{
		ProtocolVersion: FileProtocolVersion,
		TransferID:      "transfer-1",
		EntryID:         "entry-1",
		DisplayName:     "a.txt and 1 more",
		TopLevelNames:   []string{"a.txt", "b.txt"},
	})
	if err != nil {
		t.Fatalf("NewClipboardDataWithPayload() error = %v", err)
	}

	names := ClipboardItemNames(clipData)
	if len(names) != 2 || names[0] != "a.txt" || names[1] != "b.txt" {
		t.Fatalf("ClipboardItemNames() = %#v", names)
	}
}

func TestBracketedNamesFormatsNonEmptyDistinctNames(t *testing.T) {
	got := BracketedNames([]string{"a.txt", "a.txt", "", "b.jpg"})
	if got != "[a.txt] [b.jpg]" {
		t.Fatalf("BracketedNames() = %q, want %q", got, "[a.txt] [b.jpg]")
	}
}
