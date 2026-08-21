// Package store 实现 Loadout 的 JSON 数据存储：原子写入、进程内锁、
// AES-GCM 加密字段与本地密钥（.secret）。
//
// 设计原则（见 DESIGN.md 4.3、5 节）：
//   - 所有数据文件使用原子写入：先写 name.tmp 再 rename 覆盖 name；
//   - 读、写、删除、存在性判断都在进程内加锁，保证并发安全；
//   - .secret 为 32 字节随机密钥（权限 0600），首次自动生成，
//     供 AES-256-GCM 加密字段与 JWT 签名复用。
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrNotExist 文件不存在时 Read 返回的错误（errors.Is 可匹配）。
var ErrNotExist = errors.New("store: 文件不存在")

// secretName 本地密钥文件名。
const secretName = ".secret"

// secretSize 本地密钥长度（AES-256 需要的 32 字节）。
const secretSize = 32

// nonceSize AES-GCM nonce 长度（12 字节）。
const nonceSize = 12

// Store 封装一个 JSON 数据目录：原子写入 + 进程内锁 + AES-GCM 加密。
type Store struct {
	mu  sync.Mutex // mu 进程内锁，保护目录下文件的并发读写。
	dir string     // dir 数据目录路径。
	key []byte     // key 32 字节本地密钥，用于 AES-GCM 与 JWT 签名。
}

// New 打开数据目录（不存在则创建），并加载或初始化 .secret 本地密钥文件。
// .secret：32 字节随机密钥，权限 0600；首次自动生成。
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: 创建数据目录失败: %w", err)
	}

	secretPath := filepath.Join(dir, secretName)
	key, err := loadOrCreateSecret(secretPath)
	if err != nil {
		return nil, err
	}

	return &Store{dir: dir, key: key}, nil
}

// loadOrCreateSecret 读取 .secret 密钥文件；不存在则生成 32 字节随机密钥并写入。
func loadOrCreateSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != secretSize {
			return nil, fmt.Errorf("store: 密钥文件长度非法（%d，期望 %d）", len(data), secretSize)
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("store: 读取密钥文件失败: %w", err)
	}

	key := make([]byte, secretSize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("store: 生成随机密钥失败: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("store: 写入密钥文件失败: %w", err)
	}
	return key, nil
}

// Read 读取目录下 name 文件并 JSON 反序列化到 v；文件不存在返回 ErrNotExist。
func (s *Store) Read(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w", ErrNotExist)
		}
		return fmt.Errorf("store: 读取 %s 失败: %w", name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("store: 解析 %s 失败: %w", name, err)
	}
	return nil
}

// Write 原子写入：先写 name+".tmp" 再 rename，进程内加锁。
func (s *Store) Write(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: 序列化 %s 失败: %w", name, err)
	}

	tmp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("store: 写入 %s 临时文件失败: %w", name, err)
	}
	// 无论 rename 是否成功，都不在目录里残留 .tmp 文件。
	defer os.Remove(tmp)

	path := filepath.Join(s.dir, name)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store: 原子替换 %s 失败: %w", name, err)
	}
	return nil
}

// Exists 判断 name 文件是否存在。
func (s *Store) Exists(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := os.Stat(filepath.Join(s.dir, name))
	return err == nil
}

// Remove 删除 name 文件（不存在返回 nil）。
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: 删除 %s 失败: %w", name, err)
	}
	return nil
}

// Encrypt 用 AES-256-GCM 加密明文，返回 base64(nonce||密文) 字符串。
func (s *Store) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("store: 创建 AES 分组失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("store: 创建 GCM 失败: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("store: 生成 nonce 失败: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	// 输出格式：base64(nonce || 密文)。
	return base64.StdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// Decrypt 解密 Encrypt 的输出；密文非法返回错误。
func (s *Store) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("store: base64 解码失败: %w", err)
	}
	if len(raw) < nonceSize {
		return "", errors.New("store: 密文长度非法")
	}

	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("store: 创建 AES 分组失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("store: 创建 GCM 失败: %w", err)
	}

	nonce := raw[:nonceSize]
	sealed := raw[nonceSize:]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("store: 解密失败: %w", err)
	}
	return string(plain), nil
}

// SecretKey 返回 32 字节本地密钥（供 JWT 签名等复用，只读）。
// 返回的是密钥副本，调用者修改返回值不会影响 Store 内部状态。
func (s *Store) SecretKey() []byte {
	key := make([]byte, len(s.key))
	copy(key, s.key)
	return key
}

// Dir 返回数据目录路径。
func (s *Store) Dir() string {
	return s.dir
}
