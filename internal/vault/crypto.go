package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
)

// ErrAuth 认证失败（密文被篡改/截断/重排）
var ErrAuth = errors.New("vault: 认证失败（数据被篡改或损坏）")

// hkdfSHA256 标准库实现的 HKDF-SHA256（RFC 5869；依赖铁律：零外部依赖）
func hkdfSHA256(secret, salt, info []byte, length int) []byte {
	// extract：salt 为空时按 RFC 用 HashLen 个零字节
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write(secret)
	prk := mac.Sum(nil)
	// expand
	out := make([]byte, 0, length)
	var t []byte
	for i := byte(1); len(out) < length; i++ {
		mac = hmac.New(sha256.New, prk)
		mac.Write(t)
		mac.Write(info)
		mac.Write([]byte{i})
		t = mac.Sum(nil)
		out = append(out, t...)
	}
	return out[:length]
}

// FileDataKey 每文件数据密钥：HKDF(master, salt=fileID, info="mc-data-v1")，32B
func FileDataKey(master []byte, fileID [16]byte) []byte {
	return hkdfSHA256(master, fileID[:], []byte("mc-data-v1"), 32)
}

// FileMetaKey 每文件元数据密钥：info="mc-meta-v1"
func FileMetaKey(master []byte, fileID [16]byte) []byte {
	return hkdfSHA256(master, fileID[:], []byte("mc-meta-v1"), 32)
}

// ManifestKey manifest 密钥：HKDF(master, salt=nil, info="mc-manifest-v1")
func ManifestKey(master []byte) []byte {
	return hkdfSHA256(master, nil, []byte("mc-manifest-v1"), 32)
}

// seal AES-256-GCM 加密（nonce 12B；返回密文含 tag）
func seal(key, nonce, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: AES 初始化失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: GCM 初始化失败: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

// open AES-256-GCM 解密；认证失败返回 ErrAuth 包装错误
func open(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: AES 初始化失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: GCM 初始化失败: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuth, err)
	}
	return plain, nil
}
