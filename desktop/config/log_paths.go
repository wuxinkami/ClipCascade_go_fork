package config

import "path/filepath"

// LogDir 返回桌面端日志目录。
func LogDir() string {
	return filepath.Join(ConfigDir(), "logs")
}

// LogFilePath 返回桌面端主日志文件路径。
func LogFilePath() string {
	return filepath.Join(LogDir(), "desktop.log")
}
