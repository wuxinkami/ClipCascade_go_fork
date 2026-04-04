package handler

const (
	defaultWebSocketReadLimitBytes int64 = 64 * 1024 * 1024
	minWebSocketReadLimitBytes     int64 = 1024 * 1024
)

func normalizeWebSocketReadLimitBytes(limitBytes int64) int64 {
	if limitBytes <= 0 {
		return defaultWebSocketReadLimitBytes
	}
	if limitBytes < minWebSocketReadLimitBytes {
		return minWebSocketReadLimitBytes
	}
	return limitBytes
}

// ResolveWebSocketReadLimitBytes resolves the server-side websocket frame read limit.
// It prefers explicit byte config, then MiB config, and always returns a safe non-zero value.
func ResolveWebSocketReadLimitBytes(maxMessageSizeBytes int64, maxMessageSizeMiB int) int64 {
	if maxMessageSizeBytes > 0 {
		return normalizeWebSocketReadLimitBytes(maxMessageSizeBytes)
	}
	if maxMessageSizeMiB > 0 {
		return normalizeWebSocketReadLimitBytes(int64(maxMessageSizeMiB) * 1024 * 1024)
	}
	return defaultWebSocketReadLimitBytes
}

type wsReadLimitSetter interface {
	SetReadLimit(limit int64)
}

func applyWebSocketReadLimit(conn wsReadLimitSetter, limitBytes int64) {
	if conn == nil {
		return
	}
	conn.SetReadLimit(normalizeWebSocketReadLimitBytes(limitBytes))
}
