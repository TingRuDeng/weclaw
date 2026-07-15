package wechat

import (
	"bytes"
	"crypto/aes"
	"testing"
)

func TestAESECBRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	tests := []struct {
		name string
		data []byte
	}{
		{name: "空明文", data: nil},
		{name: "单字节", data: []byte("a")},
		{name: "块边界", data: bytes.Repeat([]byte("b"), aes.BlockSize)},
		{name: "跨块", data: bytes.Repeat([]byte("c"), aes.BlockSize+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypted, err := encryptAESECB(tt.data, key)
			if err != nil {
				t.Fatalf("encryptAESECB error: %v", err)
			}
			if len(encrypted) != aesECBPaddedSize(len(tt.data)) {
				t.Fatalf("cipher size=%d, want %d", len(encrypted), aesECBPaddedSize(len(tt.data)))
			}
			decrypted, err := decryptAESECB(encrypted, key)
			if err != nil {
				t.Fatalf("decryptAESECB error: %v", err)
			}
			if !bytes.Equal(decrypted, tt.data) {
				t.Fatalf("decrypted=%q, want %q", decrypted, tt.data)
			}
		})
	}
}

func TestAESECBRejectsInvalidPaddingBytes(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := make([]byte, aes.BlockSize)
	copy(plaintext, []byte("payload"))
	plaintext[aes.BlockSize-2] = 1
	plaintext[aes.BlockSize-1] = 2
	ciphertext := encryptRawAESBlocks(t, plaintext, key)

	decrypted, err := decryptAESECB(ciphertext, key)

	if err == nil {
		t.Fatalf("decryptAESECB returned %q, want invalid PKCS7 padding error", decrypted)
	}
}

func TestAESECBRejectsInvalidInputs(t *testing.T) {
	validKey := []byte("0123456789abcdef")
	tests := []struct {
		name       string
		ciphertext []byte
		key        []byte
	}{
		{name: "空密文", ciphertext: nil, key: validKey},
		{name: "非法密钥长度", ciphertext: make([]byte, aes.BlockSize), key: []byte("short")},
		{name: "非整块密文", ciphertext: []byte("not-a-full-block"), key: validKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decryptAESECB(tt.ciphertext, tt.key); err == nil {
				t.Fatal("decryptAESECB error = nil, want rejection")
			}
		})
	}
}

// encryptRawAESBlocks 生成不经过 PKCS#7 修正的密文，用于构造损坏填充输入。
func encryptRawAESBlocks(t *testing.T, plaintext []byte, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher error: %v", err)
	}
	ciphertext := make([]byte, len(plaintext))
	for offset := 0; offset < len(plaintext); offset += aes.BlockSize {
		block.Encrypt(ciphertext[offset:offset+aes.BlockSize], plaintext[offset:offset+aes.BlockSize])
	}
	return ciphertext
}
