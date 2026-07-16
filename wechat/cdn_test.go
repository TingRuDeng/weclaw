package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadFileFromCDNRoundTripOverHTTP(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := []byte("wechat attachment payload")
	encrypted, err := encryptAESECB(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/download" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("encrypted_query_param"); got != "download token" {
			t.Errorf("encrypted_query_param=%q", got)
		}
		_, _ = w.Write(encrypted)
	}))
	defer server.Close()

	got, err := downloadFileFromCDN(context.Background(), "download token", AESKeyToBase64(hex.EncodeToString(key)), server.URL, server.Client())
	if err != nil {
		t.Fatalf("DownloadFileFromCDN error: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("downloaded=%q, want %q", got, plaintext)
	}
}

func TestDownloadFileFromCDNRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "26214401")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := downloadFileFromCDN(context.Background(), "download", AESKeyToBase64(hex.EncodeToString([]byte("0123456789abcdef"))), server.URL, server.Client())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("DownloadFileFromCDN error=%v, want oversized rejection", err)
	}
}

func TestUploadToCDNUsesBinaryBodyAndEncryptedParam(t *testing.T) {
	payload := []byte("encrypted payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("request method/content-type=%s/%s", r.Method, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, payload) {
			t.Errorf("upload body=%q", body)
		}
		w.Header().Set("X-Encrypted-Param", "download-param")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got, err := uploadToCDNWithClient(context.Background(), payload, server.URL+"/upload", server.Client())
	if err != nil || got != "download-param" {
		t.Fatalf("uploadToCDN=(%q,%v)", got, err)
	}
}

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
