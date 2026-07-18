package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// keyFileName 是 genkey 默认生成的文件名。
const (
	privKeyFile = "private.pem"
	pubKeyFile  = "public.pem"
)

// generateKeyPair 生成一对 ed25519 密钥，并以 PEM 格式写入 dir。
// 返回私钥/公钥文件路径。
func generateKeyPair(dir string) (privPath, pubPath string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create key dir: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key: %w", err)
	}

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key: %w", err)
	}

	privPath = filepath.Join(dir, privKeyFile)
	pubPath = filepath.Join(dir, pubKeyFile)
	if err := writePEM(privPath, "PRIVATE KEY", privDER, 0o600); err != nil {
		return "", "", err
	}
	if err := writePEM(pubPath, "PUBLIC KEY", pubDER, 0o644); err != nil {
		return "", "", err
	}
	return privPath, pubPath, nil
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}

// loadPrivateKey 从 PEM 文件加载 ed25519 私钥。
func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block in private key file")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an ed25519 private key")
	}
	return ed, nil
}

// loadPublicKey 从 PEM 文件加载 ed25519 公钥。
func loadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block in public key file")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	ed, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an ed25519 public key")
	}
	return ed, nil
}

// publicKeyFingerprint 返回公钥的简短指纹，用于日志/审计。
// 形如: ed25519:A1B2C3D4 (公钥前 4 字节的 hex)。
func publicKeyFingerprint(pub ed25519.PublicKey) string {
	if len(pub) < 4 {
		return "ed25519:?"
	}
	return fmt.Sprintf("ed25519:%02X%02X%02X%02X", pub[0], pub[1], pub[2], pub[3])
}

// publicKeyWhitelist 是服务端持有的公钥白名单。
// 加载自一个目录下所有 .pem 文件，每文件包含一个 SPKI PEM 公钥。
type publicKeyWhitelist struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey // key = base64(pub)
}

// loadWhitelistFromDir 扫描 dir 下所有 .pem 文件，加载其中的公钥。
// 文件不存在或目录为空时返回空白名单（调用方决定是否允许无鉴权）。
func loadWhitelistFromDir(dir string) (*publicKeyWhitelist, int, error) {
	w := &publicKeyWhitelist{keys: make(map[string]ed25519.PublicKey)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return w, 0, nil
		}
		return nil, 0, fmt.Errorf("read authdir: %w", err)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		pub, err := loadPublicKey(path)
		if err != nil {
			return nil, 0, fmt.Errorf("load %s: %w", e.Name(), err)
		}
		w.keys[string(pub)] = pub
		count++
	}
	return w, count, nil
}

// contains 返回公钥是否在白名单中。
func (w *publicKeyWhitelist) contains(pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.keys[string(pub)]
	return ok
}

// count 返回白名单中公钥数量。
func (w *publicKeyWhitelist) count() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.keys)
}
