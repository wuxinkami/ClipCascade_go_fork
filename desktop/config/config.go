// Package config 管理 desktop client config。
package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	pkgcrypto "github.com/clipcascade/pkg/crypto"

	"github.com/clipcascade/pkg/constants"
)

// configMasterKeySize 是用于加密密码的本地主密钥长度（32 字节 = AES-256）。
const configMasterKeySize = 32

// Config 保存 client config。
type Config struct {
	ServerURL              string `json:"server_url"` // e.g. "http://localhost:8080"
	Username               string `json:"username"`
	Password               string `json:"-"`                  // 运行时明文密码，不直接序列化
	PasswordEncrypted      string `json:"password_encrypted"` // 加密后的密码密文（JSON 格式）
	E2EEEnabled            bool   `json:"e2ee_enabled"`
	P2PEnabled             bool   `json:"p2p_enabled"`
	StunURL                string `json:"stun_url"`
	AutoReconnect          bool   `json:"auto_reconnect"`
	ReconnectDelay         int    `json:"reconnect_delay_sec"`       // seconds
	FileMemoryThresholdMiB int64  `json:"file_memory_threshold_mib"` // MiB
	WebPort                int    `json:"web_port"`                  // 控制面板监听端口，默认 16666
	WebPassword            string `json:"-"`                         // 运行时控制面板明文密码，不直接序列化
	WebPasswordEncrypted   string `json:"web_password_encrypted"`    // 加密后的控制面板密码
	FilePath               string `json:"-"`                         // config 文件路径，不序列化

	// 兼容旧版：如果 JSON 中存在明文 password 字段，反序列化时先读取再迁移。
	LegacyPassword string `json:"password,omitempty"`
}

// DefaultConfig 返回带有合理默认值的 config。
func DefaultConfig() *Config {
	return &Config{
		ServerURL:              "http://localhost:" + strconv.Itoa(constants.DefaultPort),
		Username:               "",
		Password:               "",
		E2EEEnabled:            true,
		P2PEnabled:             true,
		StunURL:                constants.DefaultStunURL,
		AutoReconnect:          true,
		ReconnectDelay:         constants.DefaultReconnectDelay,
		FileMemoryThresholdMiB: constants.DefaultFileMemoryThresholdMiB,
		WebPort:                constants.DefaultWebPort,
	}
}

// ConfigDir 返回特定于 OS 的 config 目录。
func ConfigDir() string {
	var dir string
	switch runtime.GOOS {
	case "windows":
		dir = os.Getenv("APPDATA")
	case "darwin":
		dir, _ = os.UserHomeDir()
		dir = filepath.Join(dir, "Library", "Application Support")
	default: // linux
		dir = os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir, _ = os.UserHomeDir()
			dir = filepath.Join(dir, ".config")
		}
	}
	return filepath.Join(dir, "ClipCascade")
}

// masterKeyPath 返回本地主密钥文件的路径。
func masterKeyPath() string {
	return filepath.Join(ConfigDir(), "master.key")
}

// ensureMasterKey 确保本地主密钥存在，不存在时自动创建。
func ensureMasterKey() ([]byte, error) {
	keyPath := masterKeyPath()

	// 如果已存在，直接读取
	if data, err := os.ReadFile(keyPath); err == nil && len(data) == configMasterKeySize {
		return data, nil
	}

	// 生成新密钥
	key := make([]byte, configMasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成主密钥失败: %w", err)
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, fmt.Errorf("创建密钥目录失败: %w", err)
	}

	// 写入密钥文件（仅本人可读写）
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("写入主密钥失败: %w", err)
	}

	slog.Info("config: 已生成本地主密钥", "路径", keyPath)
	return key, nil
}

// encryptPassword 使用本地主密钥加密密码。
func encryptPassword(plainPassword string) (string, error) {
	if plainPassword == "" {
		return "", nil
	}

	key, err := ensureMasterKey()
	if err != nil {
		return "", err
	}

	payload, err := pkgcrypto.Encrypt(key, []byte(plainPassword))
	if err != nil {
		return "", fmt.Errorf("加密密码失败: %w", err)
	}

	return pkgcrypto.EncodeToJSONString(payload)
}

// decryptPassword 使用本地主密钥解密密码。
func decryptPassword(encryptedJSON string) (string, error) {
	if encryptedJSON == "" {
		return "", nil
	}

	key, err := ensureMasterKey()
	if err != nil {
		return "", err
	}

	payload, err := pkgcrypto.DecodeFromJSONString(encryptedJSON)
	if err != nil {
		return "", fmt.Errorf("解析密码密文失败: %w", err)
	}

	plaintext, err := pkgcrypto.Decrypt(key, payload)
	if err != nil {
		return "", fmt.Errorf("解密密码失败: %w", err)
	}

	return string(plaintext), nil
}

// Load 从文件中读取 config，如果未找到则返回默认值。
func Load() *Config {
	cfg := DefaultConfig()
	cfgPath := filepath.Join(ConfigDir(), "config.json")
	cfg.FilePath = cfgPath

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, cfg)
	cfg.FilePath = cfgPath
	cfg.normalize()

	// 解密服务端密码：优先使用加密版本
	if cfg.PasswordEncrypted != "" {
		decrypted, err := decryptPassword(cfg.PasswordEncrypted)
		if err != nil {
			slog.Warn("config: 解密密码失败，密码将为空", "error", err)
			cfg.Password = ""
		} else {
			cfg.Password = decrypted
		}
	} else if cfg.LegacyPassword != "" {
		// 兼容旧版明文密码：读取后自动迁移到加密存储
		cfg.Password = cfg.LegacyPassword
		cfg.LegacyPassword = "" // 清除内存中的明文残留
		slog.Info("config: 检测到旧版明文密码，将在下次保存时自动迁移到加密存储")
	}

	// 解密控制面板密码
	if cfg.WebPasswordEncrypted != "" {
		decrypted, err := decryptPassword(cfg.WebPasswordEncrypted)
		if err != nil {
			slog.Warn("config: 解密控制面板密码失败", "error", err)
			cfg.WebPassword = ""
		} else {
			cfg.WebPassword = decrypted
		}
	}

	if envPassword := os.Getenv("CLIPCASCADE_PASSWORD"); envPassword != "" {
		cfg.Password = envPassword
	}
	return cfg
}

// Save 将 config 写入磁盘。密码使用本地主密钥加密存储。
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.FilePath), 0755); err != nil {
		return err
	}
	toSave := *c
	toSave.normalize()

	// 加密密码
	passwordToEncrypt := toSave.Password
	// 当使用环境变量注入密码时，避免将密码持久化到磁盘。
	if os.Getenv("CLIPCASCADE_PASSWORD") != "" {
		passwordToEncrypt = ""
	}

	if passwordToEncrypt != "" {
		encrypted, err := encryptPassword(passwordToEncrypt)
		if err != nil {
			slog.Warn("config: 加密密码失败，密码不会被保存", "error", err)
			toSave.PasswordEncrypted = ""
		} else {
			toSave.PasswordEncrypted = encrypted
		}
	} else {
		toSave.PasswordEncrypted = ""
	}

	// 加密控制面板密码
	if toSave.WebPassword != "" {
		encrypted, err := encryptPassword(toSave.WebPassword)
		if err != nil {
			slog.Warn("config: 加密控制面板密码失败", "error", err)
			toSave.WebPasswordEncrypted = ""
		} else {
			toSave.WebPasswordEncrypted = encrypted
		}
	} else {
		toSave.WebPasswordEncrypted = ""
	}

	// 确保旧版明文密码字段不会写入磁盘
	toSave.LegacyPassword = ""

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.FilePath, data, 0600)
}

// SaveServerURLOnly 仅持久化 server_url，避免无意覆盖用户名/密码等配置。
func (c *Config) SaveServerURLOnly(serverURL string) error {
	cfgPath := c.FilePath
	if cfgPath == "" {
		cfgPath = filepath.Join(ConfigDir(), "config.json")
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}

	// 使用 map 保留原始 JSON 中的所有字段（包括密码相关字段），只替换 server_url。
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(data, &raw)
	}

	normalized := NormalizeServerURL(serverURL)
	urlBytes, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	raw["server_url"] = urlBytes

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0600)
}

// NormalizeServerURL 规范化 server 地址，支持裸 host:port 自动补 http://。
func NormalizeServerURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return strings.TrimRight(s, "/")
	}
	if u.Scheme == "" {
		u.Scheme = "http"
	}
	normalized := u.String()
	return strings.TrimRight(normalized, "/")
}

func (c *Config) normalize() {
	if c == nil {
		return
	}
	c.ServerURL = NormalizeServerURL(c.ServerURL)
	if c.FileMemoryThresholdMiB <= 0 {
		c.FileMemoryThresholdMiB = constants.DefaultFileMemoryThresholdMiB
	}
	if c.FileMemoryThresholdMiB > constants.MaxFileMemoryThresholdMiB {
		c.FileMemoryThresholdMiB = constants.MaxFileMemoryThresholdMiB
	}
	if c.WebPort <= 0 || c.WebPort > 65535 {
		c.WebPort = constants.DefaultWebPort
	}
}

func (c *Config) FileMemoryThresholdBytes() int64 {
	if c == nil {
		return int64(constants.DefaultFileMemoryThresholdMiB) << 20
	}
	thresholdMiB := c.FileMemoryThresholdMiB
	if thresholdMiB <= 0 {
		thresholdMiB = constants.DefaultFileMemoryThresholdMiB
	}
	if thresholdMiB > constants.MaxFileMemoryThresholdMiB {
		thresholdMiB = constants.MaxFileMemoryThresholdMiB
	}
	return thresholdMiB << 20
}
