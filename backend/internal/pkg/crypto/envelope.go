// Package crypto 提供书源 header 的应用层 AES-256-GCM 信封加密。
//
// 信封格式（v1.4 / ADR-010）：
//
//	v1:<kid>:<base64(nonce || ciphertext || tag)>
//
// 其中 nonce 固定 12 字节随机，AAD 为 "<user_id>|<source_id>"，密文使用
// 32 字节密钥的 AES-256-GCM。数据库中的 JSONB 呈现为 {"__enc":"v1:..."}。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
)

const (
	// EnvelopeKey 是 JSONB 中信封字段的键名。
	EnvelopeKey = "__enc"
	// EnvelopePrefix 是信封版本前缀。
	EnvelopePrefix = "v1"

	nonceSize = 12
	tagSize   = 16
	KeySize   = 32
)

// 哨兵错误。
var (
	ErrNoKey      = errors.New("crypto: no header key configured")
	ErrMalformed  = errors.New("crypto: malformed header envelope")
	ErrUnknownKID = errors.New("crypto: unknown key id")
	ErrDecrypt    = errors.New("crypto: header decrypt failed")
)

// Keyring 保存多代 kid → 密钥，current 用于新写入。
type Keyring struct {
	current string
	keys    map[string][]byte
}

// NewKeyring 校验并构造密钥环。
func NewKeyring(current string, keys map[string][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, ErrNoKey
	}
	for kid, key := range keys {
		if kid == "" || strings.ContainsAny(kid, ":") {
			return nil, fmt.Errorf("crypto: invalid kid %q", kid)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("crypto: key %q must be %d bytes, got %d", kid, KeySize, len(key))
		}
	}
	if current == "" {
		if len(keys) == 1 {
			for kid := range keys {
				current = kid
			}
		} else {
			names := make([]string, 0, len(keys))
			for kid := range keys {
				names = append(names, kid)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("crypto: multiple keys configured, YREADER_HEADER_KEY_KID required (available: %s)", strings.Join(names, ","))
		}
	}
	if _, ok := keys[current]; !ok {
		return nil, fmt.Errorf("crypto: current kid %q not found", current)
	}
	return &Keyring{current: current, keys: keys}, nil
}

// LoadKeyringFromEnv 从环境变量装配密钥环：
//
//	YREADER_HEADER_KEY_<KID>=base64(32B)   一个或多个
//	YREADER_HEADER_KEY_KID=<kid>           多密钥时必填，指定新写入使用的 kid
func LoadKeyringFromEnv() (*Keyring, error) {
	const prefix = "YREADER_HEADER_KEY_"
	keys := make(map[string][]byte)
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, prefix) || name == prefix+"KID" {
			continue
		}
		kid := strings.ToLower(strings.TrimPrefix(name, prefix))
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("crypto: decode %s: %w", name, err)
		}
		keys[kid] = raw
	}
	if len(keys) == 0 {
		return nil, ErrNoKey
	}
	return NewKeyring(strings.ToLower(os.Getenv(prefix+"KID")), keys)
}

// CurrentKID 返回新写入使用的 kid。
func (k *Keyring) CurrentKID() string { return k.current }

// Seal 加密明文并返回信封字符串。
func (k *Keyring) Seal(plain []byte, aad string) (string, error) {
	gcm, err := k.aead(k.current)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	out := gcm.Seal(nil, nonce, plain, []byte(aad))
	buf := make([]byte, 0, nonceSize+len(out))
	buf = append(buf, nonce...)
	buf = append(buf, out...)
	return EnvelopePrefix + ":" + k.current + ":" + base64.StdEncoding.EncodeToString(buf), nil
}

// Open 解密信封；kid 未知、密文损坏或 AAD 不匹配均返回错误（绝不回退明文）。
func (k *Keyring) Open(envelope, aad string) ([]byte, error) {
	parts := strings.SplitN(envelope, ":", 3)
	if len(parts) != 3 || parts[0] != EnvelopePrefix || parts[1] == "" || parts[2] == "" {
		return nil, ErrMalformed
	}
	key, ok := k.keys[parts[1]]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKID, parts[1])
	}
	buf, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: base64", ErrMalformed)
	}
	if len(buf) < nonceSize+tagSize {
		return nil, ErrMalformed
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, buf[:nonceSize], buf[nonceSize:], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

func (k *Keyring) aead(kid string) (cipher.AEAD, error) {
	key, ok := k.keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKID, kid)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// AAD 生成绑定「用户 + 书源」的附加认证数据，跨租户/跨源重放会解密失败。
func AAD(userID, sourceID uuid.UUID) string {
	return userID.String() + "|" + sourceID.String()
}

// defaultRing 是进程级默认密钥环（main 启动时注入；未注入表示未启用信封加密）。
var defaultRing atomic.Pointer[Keyring]

// SetDefaultKeyring 注入进程级密钥环（main 与测试使用）。
func SetDefaultKeyring(k *Keyring) { defaultRing.Store(k) }

// DefaultKeyring 返回进程级密钥环，未配置时为 nil。
func DefaultKeyring() *Keyring { return defaultRing.Load() }
