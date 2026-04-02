package protocol

import "testing"

func TestClipboardDataWithFilePayloadRoundTrip(t *testing.T) {
	manifest := FileStubManifest{
		ProtocolVersion:     FileProtocolVersion,
		EntryID:             "entry-1",
		TransferID:          "transfer-1",
		SourceSessionID:     "session-1",
		Kind:                "single_file",
		ArchiveFormat:       "zip",
		DisplayName:         "a.txt",
		EntryCount:          1,
		TopLevelNames:       []string{"a.txt"},
		EstimatedTotalBytes: 12,
	}
	data, err := NewClipboardDataWithPayload("file_stub", manifest)
	if err != nil {
		t.Fatalf("NewClipboardDataWithPayload() error = %v", err)
	}
	decoded, err := DecodePayload[FileStubManifest](data.Payload)
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if decoded.TransferID != manifest.TransferID {
		t.Fatalf("decoded.TransferID = %q, want %q", decoded.TransferID, manifest.TransferID)
	}
	if decoded.DisplayName != manifest.DisplayName {
		t.Fatalf("decoded.DisplayName = %q, want %q", decoded.DisplayName, manifest.DisplayName)
	}
}

func TestFileReleasePayloadRoundTrip(t *testing.T) {
	release := FileRelease{
		TransferID:      "transfer-1",
		TargetSessionID: "session-1",
		ReleaseReason:   "received_ok",
	}
	data, err := NewClipboardDataWithPayload("file_release", release)
	if err != nil {
		t.Fatalf("NewClipboardDataWithPayload() error = %v", err)
	}
	decoded, err := DecodePayload[FileRelease](data.Payload)
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if decoded.TransferID != release.TransferID {
		t.Fatalf("decoded.TransferID = %q, want %q", decoded.TransferID, release.TransferID)
	}
	if decoded.ReleaseReason != release.ReleaseReason {
		t.Fatalf("decoded.ReleaseReason = %q, want %q", decoded.ReleaseReason, release.ReleaseReason)
	}
}
