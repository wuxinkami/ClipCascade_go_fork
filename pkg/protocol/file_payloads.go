package protocol

import "encoding/json"

const FileProtocolVersion = 1

const (
	FileKindSingleFile = "single_file"
	FileKindMultiFile  = "multi_file"
	FileKindFolder     = "folder"
	FileKindImage      = "image"
)

type FileStubManifest struct {
	ProtocolVersion     int      `json:"protocol_version"`
	EntryID             string   `json:"entry_id"`
	TransferID          string   `json:"transfer_id"`
	SourceSessionID     string   `json:"source_session_id"`
	SourceDevice        string   `json:"source_device,omitempty"`
	Kind                string   `json:"kind"`
	ArchiveFormat       string   `json:"archive_format"`
	DisplayName         string   `json:"display_name"`
	EntryCount          int      `json:"entry_count"`
	TopLevelNames       []string `json:"top_level_names"`
	EstimatedTotalBytes int64    `json:"estimated_total_bytes"`
}

type FileRequest struct {
	TransferID      string `json:"transfer_id"`
	EntryID         string `json:"entry_id"`
	TargetSessionID string `json:"target_session_id"`
	ResumeFromChunk int    `json:"resume_from_chunk"`
}

type FileChunk struct {
	TransferID      string `json:"transfer_id"`
	TargetSessionID string `json:"target_session_id"`
	ArchiveMode     string `json:"archive_mode,omitempty"`
	ChunkIndex      int    `json:"chunk_index"`
	TotalChunks     int    `json:"total_chunks"`
	ChunkData       string `json:"chunk_data"`
	ChunkSHA256     string `json:"chunk_sha256"`
}

type FileComplete struct {
	TransferID       string `json:"transfer_id"`
	TargetSessionID  string `json:"target_session_id"`
	ArchiveMode      string `json:"archive_mode,omitempty"`
	ArchiveSHA256    string `json:"archive_sha256"`
	ActualTotalBytes int64  `json:"actual_total_bytes"`
}

type FileError struct {
	TransferID      string `json:"transfer_id"`
	TargetSessionID string `json:"target_session_id"`
	ErrorCode       string `json:"error_code"`
	ErrorMessage    string `json:"error_message"`
	Retryable       bool   `json:"retryable"`
}

type FileRelease struct {
	TransferID      string `json:"transfer_id"`
	TargetSessionID string `json:"target_session_id"`
	ReleaseReason   string `json:"release_reason"`
}

func EncodePayload[T any](payload T) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DecodePayload[T any](payload string) (*T, error) {
	var value T
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func NewClipboardDataWithPayload[T any](kind string, payload T) (*ClipboardData, error) {
	encoded, err := EncodePayload(payload)
	if err != nil {
		return nil, err
	}
	return &ClipboardData{
		Type:    kind,
		Payload: encoded,
	}, nil
}
