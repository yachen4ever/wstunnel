package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// ioBufSize 同时用作 TCP 读缓冲和 WS 单帧上限：大结果集/COPY 场景下
	// 32KB 相比 1KB 显著减少 WS 帧头开销和系统调用次数。
	ioBufSize = 32 * 1024
	// maxMessageSize 须大于对端单帧上限（ioBufSize），防止未鉴权对端
	// 发送超大帧触发无界内存分配。调整 ioBufSize 时需同步调整。
	maxMessageSize    = 64 * 1024
	heartbeatInterval = 10 * time.Second // client 发 Ping 的间隔
	readTimeout       = 30 * time.Second // 任意方向无消息即断开（含心跳）；DB 场景宁可快速失败让应用重连
	writeWait         = 10 * time.Second // 单次 WriteControl 的超时
	targetDialTimeout = 5 * time.Second  // 拨号 -target 的超时；net.Dial 默认无超时，会挂到 OS 级 TCP 超时
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  ioBufSize,
	WriteBufferSize: ioBufSize,
	// 隧道服务端不校验 Origin：鉴权由 ed25519 握手承担。
	CheckOrigin: func(r *http.Request) bool { return true },
}

// verbose 控制是否打印每个字节方向的流量日志（C→S/S→C、L→R/R→L）。
// 默认 false：只打隧道建立/断开、鉴权、错误等运维必要事件。
// 由 -v 命令行开关打开。
var verbose bool

// Server 是 wstunnel 服务端。
type Server struct {
	DestAddress string
	Whitelist   *publicKeyWhitelist
}

func (s *Server) handler(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade from %s: %v", r.RemoteAddr, err)
		return
	}
	defer ws.Close()
	// 鉴权握手前即生效：握手消息最长 97 字节，64KB 上限只拦异常大帧
	ws.SetReadLimit(maxMessageSize)

	// 1. 鉴权握手
	pub, err := serverHandshake(ws, s.Whitelist)
	if err != nil {
		fp := "unknown"
		if pub != nil {
			fp = publicKeyFingerprint(pub)
		}
		if ae, ok := err.(*authError); ok {
			log.Printf("auth failed from %s (%s): %s", r.RemoteAddr, fp, ae.reason)
			sendAuthFail(ws, ae.reason)
		} else {
			log.Printf("auth error from %s (%s): %v", r.RemoteAddr, fp, err)
		}
		return
	}
	log.Printf("tunnel established: %s <-> %s (client=%s)",
		r.RemoteAddr, s.DestAddress, publicKeyFingerprint(pub))

	// 2. 拨号目标 TCP
	tcp, err := (&net.Dialer{Timeout: targetDialTimeout}).Dial("tcp", s.DestAddress)
	if err != nil {
		log.Printf("dial target %s: %v", s.DestAddress, err)
		return
	}
	defer tcp.Close()

	// 3. 设置心跳：续约读超时 + 自动回 Pong
	installHeartbeat(ws)

	// 4. 双向桥接
	bridge(ws, tcp, true) // true: server 端，关闭时主动断 TCP
	log.Printf("tunnel closed: %s <-> %s (client=%s)",
		r.RemoteAddr, s.DestAddress, publicKeyFingerprint(pub))
}

// installHeartbeat 为 WS 连接安装心跳处理：
//   - 设置初始读超时
//   - 收到 Ping 自动回 Pong 并续约
//   - 收到 Pong 续约
//   - 数据模式期间任何 ReadMessage 出错即退出
func installHeartbeat(ws *websocket.Conn) {
	_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
	ws.SetPingHandler(func(appData string) error {
		_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
		return ws.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(writeWait))
	})
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})
}

// bridge 在 WS 与 TCP 之间双向转发数据。
// 鉴权已完成，所有 BinaryMessage 的 payload 即为 TCP 字节。
// 任一方向出错即关闭两端。
func bridge(ws *websocket.Conn, tcp net.Conn, serverSide bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 标签日志：server 端记 C→S/S→C，client 端记 L→R/R→L（Local/Remote）
	dir1, dir2 := "C\u2192S", "S\u2192C"
	if !serverSide {
		dir1, dir2 = "L\u2192R", "R\u2192L"
	}

	var writeMu sync.Mutex // 串行化 ws 写（数据 + 控制帧）

	// 协程 1: TCP -> WS
	go func() {
		buf := make([]byte, ioBufSize)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				writeMu.Lock()
				werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n])
				writeMu.Unlock()
				if werr != nil {
					log.Printf("%s write err: %v", dir1, werr)
					cancel()
					tcp.Close()
					return
				}
				if verbose {
					log.Printf("%s %d", dir1, n)
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("%s read err: %v", dir1, err)
				}
				cancel()
				tcp.Close()
				return
			}
		}
	}()

	// 主协程: WS -> TCP
	for {
		select {
		case <-ctx.Done():
			ws.Close()
			return
		default:
		}
		_, buf, err := ws.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("%s read err: %v", dir2, err)
			}
			cancel()
			ws.Close()
			return
		}
		if _, err := tcp.Write(buf); err != nil {
			log.Printf("%s write err: %v", dir2, err)
			cancel()
			ws.Close()
			return
		}
		if verbose {
			log.Printf("%s %d", dir2, len(buf))
		}
	}
}

// server 启动服务端。tlsCert/tlsKey 同时非空时以原生 wss 监听（TLS 由自身终结），
// 默认均为空、监听明文 ws——加密交给前置反代。
func server(bindAddr, destAddr, authDir, tlsCert, tlsKey string) {
	if (tlsCert == "") != (tlsKey == "") {
		log.Fatal("server: -tlscert and -tlskey must be provided together to enable native wss")
	}
	wl, n, err := loadWhitelistFromDir(authDir)
	if err != nil {
		log.Fatalf("load authdir %s: %v", authDir, err)
	}
	if n == 0 {
		log.Fatalf("no public keys found in %s; refusing to start without authentication", authDir)
	}
	log.Printf("loaded %d authorized public key(s) from %s", n, authDir)

	s := &Server{
		DestAddress: destAddr,
		Whitelist:   wl,
	}
	http.HandleFunc("/ws", s.handler)
	if tlsCert != "" {
		log.Printf("wstunnel server listening on %s (wss), forwarding to %s", bindAddr, destAddr)
		log.Fatal(http.ListenAndServeTLS(bindAddr, tlsCert, tlsKey, nil))
	}
	log.Printf("wstunnel server listening on %s (ws), forwarding to %s", bindAddr, destAddr)
	log.Fatal(http.ListenAndServe(bindAddr, nil))
}
