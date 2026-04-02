package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeServerURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"192.168.18.240:8080", "http://192.168.18.240:8080"},
		{" http://192.168.18.240:8080/ ", "http://192.168.18.240:8080"},
		{"https://example.com", "https://example.com"},
		{"", ""},
	}

	for _, tc := range cases {
		got := NormalizeServerURL(tc.in)
		if got != tc.want {
			t.Fatalf("NormalizeServerURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSaveServerURLOnlyPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// 模拟旧版配置文件（JSON 中含明文 password 和 username）
	origJSON := `{
  "server_url": "http://old-host:8080",
  "username": "admin",
  "password": "secret",
  "e2ee_enabled": true,
  "p2p_enabled": true,
  "stun_url": "stun:stun.l.google.com:19302",
  "auto_reconnect": true,
  "reconnect_delay_sec": 5,
  "file_memory_threshold_mib": 2048
}`
	if err := os.WriteFile(cfgPath, []byte(origJSON), 0o600); err != nil {
		t.Fatalf("write orig config: %v", err)
	}

	cfg := &Config{FilePath: cfgPath}
	if err := cfg.SaveServerURLOnly("192.168.18.240:8080"); err != nil {
		t.Fatalf("SaveServerURLOnly: %v", err)
	}

	updatedData, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}

	// 验证为 raw JSON map，确保所有字段都保留
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(updatedData, &raw); err != nil {
		t.Fatalf("unmarshal updated config: %v", err)
	}

	// 验证 server_url 已更新
	var serverURL string
	if err := json.Unmarshal(raw["server_url"], &serverURL); err != nil {
		t.Fatalf("unmarshal server_url: %v", err)
	}
	if serverURL != "http://192.168.18.240:8080" {
		t.Fatalf("server url mismatch: got %q", serverURL)
	}

	// 验证 username 和 password 字段保留不变
	var username string
	if err := json.Unmarshal(raw["username"], &username); err != nil {
		t.Fatalf("unmarshal username: %v", err)
	}
	if username != "admin" {
		t.Fatalf("username should be preserved, got %q", username)
	}

	var password string
	if err := json.Unmarshal(raw["password"], &password); err != nil {
		t.Fatalf("unmarshal password: %v", err)
	}
	if password != "secret" {
		t.Fatalf("password should be preserved, got %q", password)
	}

	// 验证 file_memory_threshold_mib 保留
	var threshold float64
	if err := json.Unmarshal(raw["file_memory_threshold_mib"], &threshold); err != nil {
		t.Fatalf("unmarshal file_memory_threshold_mib: %v", err)
	}
	if int64(threshold) != 2048 {
		t.Fatalf("file memory threshold mismatch: got %v want 2048", threshold)
	}
}

func TestConfigFileMemoryThresholdBytesUsesDefaultAndCapsMax(t *testing.T) {
	cfg := &Config{}
	if got, want := cfg.FileMemoryThresholdBytes(), int64(1024)<<20; got != want {
		t.Fatalf("default bytes = %d, want %d", got, want)
	}

	cfg.FileMemoryThresholdMiB = 2048
	if got, want := cfg.FileMemoryThresholdBytes(), int64(2048)<<20; got != want {
		t.Fatalf("configured bytes = %d, want %d", got, want)
	}

	cfg.FileMemoryThresholdMiB = 99999
	if got, want := cfg.FileMemoryThresholdBytes(), int64(5120)<<20; got != want {
		t.Fatalf("capped bytes = %d, want %d", got, want)
	}
}

func TestDefaultConfigWebPort(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.WebPort != 6666 {
		t.Fatalf("default web port = %d, want 6666", cfg.WebPort)
	}
}

func TestPasswordEncryptionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// 确保主密钥目录可用（使用测试方式不可控，但验证加密解密对称即可）
	plainPassword := "test-password-秘密"
	encrypted, err := encryptPassword(plainPassword)
	if err != nil {
		t.Fatalf("encryptPassword: %v", err)
	}
	if encrypted == "" {
		t.Fatalf("encrypted password should not be empty")
	}
	if encrypted == plainPassword {
		t.Fatalf("encrypted password should differ from plaintext")
	}

	decrypted, err := decryptPassword(encrypted)
	if err != nil {
		t.Fatalf("decryptPassword: %v", err)
	}
	if decrypted != plainPassword {
		t.Fatalf("decrypted = %q, want %q", decrypted, plainPassword)
	}

	// 验证保存/加载循环
	cfg := DefaultConfig()
	cfg.FilePath = cfgPath
	cfg.Username = "testuser"
	cfg.Password = plainPassword
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 读取文件验证密码不是明文
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	fileContent := string(data)
	if contains(fileContent, plainPassword) {
		t.Fatalf("config file should not contain plaintext password")
	}

	// 重新加载验证密码正确恢复。
	// 注意：Load() 使用固定路径，不方便直接测试。
	// 这里手动模拟加载逻辑。
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if loaded.PasswordEncrypted == "" {
		t.Fatalf("password_encrypted should be set in saved file")
	}
	recoveredPw, err := decryptPassword(loaded.PasswordEncrypted)
	if err != nil {
		t.Fatalf("decryptPassword from saved: %v", err)
	}
	if recoveredPw != plainPassword {
		t.Fatalf("recovered password = %q, want %q", recoveredPw, plainPassword)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLegacyPasswordMigration(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// 模拟旧版配置：密码以明文 "password" 字段存储
	legacyJSON := `{
  "server_url": "http://localhost:8080",
  "username": "admin",
  "password": "admin123",
  "e2ee_enabled": true
}`
	if err := os.WriteFile(cfgPath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	var cfg Config
	data, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(data, &cfg)

	// LegacyPassword 应被填充
	if cfg.LegacyPassword != "admin123" {
		t.Fatalf("legacy password = %q, want %q", cfg.LegacyPassword, "admin123")
	}
	// Password (json:"-") 不应被填充
	if cfg.Password != "" {
		t.Fatalf("Password field should be empty from unmarshal, got %q", cfg.Password)
	}
}
