package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 重连参数：每个本地 TCP 连接独立拨 WS，失败时退避重试。
const (
	dialRetryInitial = 1 * time.Second
	dialRetryMax     = 30 * time.Second
	dialRetryMaxNum  = 5 // 1+2+4+8+16 = 31s 上限
	dialTimeout      = 10 * time.Second
)

// dialWithRetry 带指数退避地拨号 WS 并完成鉴权握手。
// 成功返回已通过鉴权的 WS 连接；失败返回最后一次错误。
// insecure=true 时跳过 TLS 证书验证（用于 wss:// + 自签名证书场景）。
func dialWithRetry(websocketURL string, priv ed25519.PrivateKey, insecure bool) (*websocket.Conn, error) {
	var lastErr error
	backoff := dialRetryInitial
	for attempt := 1; attempt <= dialRetryMaxNum; attempt++ {
		d := &websocket.Dialer{
			HandshakeTimeout: dialTimeout,
		}
		if insecure {
			d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		ws, _, err := d.Dial(websocketURL, nil)
		if err == nil {
			if err = clientHandshake(ws, priv); err == nil {
				return ws, nil
			}
			// 握手失败通常不可重试（密钥错误/协议不符），直接返回
			ws.Close()
			return nil, err
		}
		lastErr = err
		log.Printf("dial attempt %d/%d failed: %v", attempt, dialRetryMaxNum, err)
		if attempt < dialRetryMaxNum {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > dialRetryMax {
				backoff = dialRetryMax
			}
		}
	}
	return nil, lastErr
}

// startHeartbeat 在已鉴权的 WS 上定期发 Ping。
// 通过 ctx 退出。
func startHeartbeat(ws *websocket.Conn, writeMu *sync.Mutex, ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			writeMu.Lock()
			err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
			writeMu.Unlock()
			if err != nil {
				log.Printf("heartbeat ping failed: %v", err)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// handleLocalConn 处理单个本地 TCP 连接：
// 拨 WS + 鉴权 + 心跳 + 双向桥接。
func handleLocalConn(tcp net.Conn, websocketURL string, priv ed25519.PrivateKey, insecure bool) {
	defer tcp.Close()

	ws, err := dialWithRetry(websocketURL, priv, insecure)
	if err != nil {
		log.Printf("establish tunnel failed: %v", err)
		return
	}
	defer ws.Close()

	installHeartbeat(ws) // 续约读超时 + 自动回 Pong

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var writeMu sync.Mutex
	go startHeartbeat(ws, &writeMu, ctx)

	log.Printf("client tunnel up: %s <-> %s", tcp.RemoteAddr(), websocketURL)
	bridgeClient(ws, tcp, &writeMu, ctx)
	log.Printf("client tunnel down: %s", tcp.RemoteAddr())
}

// bridgeClient 是 client 侧的桥接，与 server 端 bridge 对称。
// 共享 writeMu 以串行化数据写与心跳 Ping 写。
func bridgeClient(ws *websocket.Conn, tcp net.Conn, writeMu *sync.Mutex, ctx context.Context) {
	dir1, dir2 := "L\u2192R", "R\u2192L"

	// 协程: TCP -> WS
	go func() {
		buf := make([]byte, serverReadBufferSize)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				writeMu.Lock()
				werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n])
				writeMu.Unlock()
				if werr != nil {
					log.Printf("%s write err: %v", dir1, werr)
					tcp.Close()
					return
				}
				if verbose {
					log.Printf("%s %d", dir1, n)
				}
			}
			if err != nil {
				tcp.Close()
				return
			}
		}
	}()

	// 主: WS -> TCP
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, buf, err := ws.ReadMessage()
		if err != nil {
			log.Printf("%s read err: %v", dir2, err)
			return
		}
		if _, err := tcp.Write(buf); err != nil {
			log.Printf("%s write err: %v", dir2, err)
			return
		}
		if verbose {
			log.Printf("%s %d", dir2, len(buf))
		}
	}
}

func client(bindAddr, websocketURL, keyPath string, insecure bool) {
	priv, err := loadPrivateKey(keyPath)
	if err != nil {
		log.Fatalf("load private key %s: %v", keyPath, err)
	}
	log.Printf("client identity: %s", publicKeyFingerprint(priv.Public().(ed25519.PublicKey)))

	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", bindAddr, err)
	}
	log.Printf("wstunnel client listening on %s, forwarding to %s", bindAddr, websocketURL)

	for {
		tcp, err := listener.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go handleLocalConn(tcp, websocketURL, priv, insecure)
	}
}
