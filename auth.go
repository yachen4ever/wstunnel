package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// 鉴权握手协议在 WS 连接建立后、数据转发前进行。
// 消息均为 BinaryMessage，第一字节为类型，剩余为 payload。
// 鉴权完成后，双方进入数据模式：所有 BinaryMessage 不带前缀，payload 即为 TCP 字节。
const (
	msgNonce    byte = 0x01 // server→client: [1][32B nonce]
	msgAuth     byte = 0x02 // client→server: [1][32B pubkey][64B signature]
	msgAuthOK   byte = 0x03 // server→client: [1]
	msgAuthFail byte = 0x04 // server→client: [1][errmsg...]
)

// 握手阶段消息长度。
const (
	nonceSize    = 32
	sigSize      = ed25519.SignatureSize
	lenNonceMsg  = 1 + nonceSize
	lenAuthMsg   = 1 + ed25519.PublicKeySize + sigSize
	lenAuthOKMsg = 1
	authDeadline = 10 * time.Second // 握手超时，防慢速攻击
)

// authError 表示握手失败的具体原因，方便 server 端打日志。
type authError struct {
	reason string
}

func (e *authError) Error() string { return e.reason }

// serverHandshake 执行 server 端鉴权握手。
// 成功返回客户端公钥（用于日志/审计）；失败返回具体错误。
//
// 流程:
//  1. 生成 32B 随机 nonce，发给 client
//  2. 等待 client 回 [pubkey][signature]
//  3. 校验公钥在白名单 + ed25519.Verify(pubkey, nonce, signature)
//  4. 发 OK，进入数据模式
func serverHandshake(ws *websocket.Conn, whitelist *publicKeyWhitelist) (ed25519.PublicKey, error) {
	if whitelist == nil || whitelist.count() == 0 {
		return nil, &authError{reason: "no authorized public keys configured on server"}
	}

	if err := ws.SetReadDeadline(time.Now().Add(authDeadline)); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}
	defer ws.SetReadDeadline(time.Time{}) // 清除，数据模式用单独的 deadline

	// 1. 生成并发 nonce
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	nonceMsg := make([]byte, lenNonceMsg)
	nonceMsg[0] = msgNonce
	copy(nonceMsg[1:], nonce[:])
	if err := ws.WriteMessage(websocket.BinaryMessage, nonceMsg); err != nil {
		return nil, fmt.Errorf("send nonce: %w", err)
	}

	// 2. 读 client 的认证消息
	_, buf, err := ws.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read auth: %w", err)
	}
	if len(buf) != lenAuthMsg || buf[0] != msgAuth {
		return nil, &authError{reason: "malformed auth message"}
	}
	pub := ed25519.PublicKey(buf[1 : 1+ed25519.PublicKeySize])
	sig := buf[1+ed25519.PublicKeySize:]

	// 3. 白名单 + 验签
	if !whitelist.contains(pub) {
		return pub, &authError{reason: "public key not in whitelist"}
	}
	if !ed25519.Verify(pub, nonce[:], sig) {
		return pub, &authError{reason: "signature verification failed"}
	}

	// 4. OK
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{msgAuthOK}); err != nil {
		return nil, fmt.Errorf("send ok: %w", err)
	}
	return pub, nil
}

// sendAuthFail 发送失败消息并关闭连接。失败本身不再返回错误给调用方。
func sendAuthFail(ws *websocket.Conn, reason string) {
	msg := make([]byte, 1+len(reason))
	msg[0] = msgAuthFail
	copy(msg[1:], reason)
	_ = ws.WriteMessage(websocket.BinaryMessage, msg)
	_ = ws.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason))
}

// clientHandshake 执行 client 端鉴权握手。
// 成功返回 nil；失败返回错误，调用方应关闭连接。
//
// 流程:
//  1. 收 server 的 nonce
//  2. 用私钥对 nonce 签名，发 [pubkey][signature]
//  3. 收 OK
func clientHandshake(ws *websocket.Conn, priv ed25519.PrivateKey) error {
	if err := ws.SetReadDeadline(time.Now().Add(authDeadline)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}
	defer ws.SetReadDeadline(time.Time{})

	// 1. 收 nonce
	_, buf, err := ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("read nonce: %w", err)
	}
	if len(buf) != lenNonceMsg || buf[0] != msgNonce {
		return &authError{reason: "malformed nonce message"}
	}
	var nonce [nonceSize]byte
	copy(nonce[:], buf[1:])

	// 2. 签名并发送
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, nonce[:])
	authMsg := make([]byte, lenAuthMsg)
	authMsg[0] = msgAuth
	copy(authMsg[1:1+ed25519.PublicKeySize], pub)
	copy(authMsg[1+ed25519.PublicKeySize:], sig)
	if err := ws.WriteMessage(websocket.BinaryMessage, authMsg); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	// 3. 收结果
	_, buf, err = ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth result: %w", err)
	}
	if len(buf) < 1 {
		return &authError{reason: "empty auth result"}
	}
	switch buf[0] {
	case msgAuthOK:
		return nil
	case msgAuthFail:
		reason := "rejected"
		if len(buf) > 1 {
			reason = string(buf[1:])
		}
		return &authError{reason: reason}
	default:
		return &authError{reason: "unexpected auth result"}
	}
}
