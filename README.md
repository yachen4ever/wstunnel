---
AIGC:
  ContentProducer: '001191110102MAD55U9H0F10002'
  ContentPropagator: '001191110102MAD55U9H0F10002'
  Label: '1'
  ProduceID: 'b562544e-a41e-4cfc-b326-998368b9d1f8'
  PropagateID: 'b562544e-a41e-4cfc-b326-998368b9d1f8'
  ReservedCode1: '33d486d3-1446-4ea5-96c2-d3989bc90f1a'
  ReservedCode2: '33d486d3-1446-4ea5-96c2-d3989bc90f1a'
---

# wstunnel

基于 WebSocket 的 TCP 隧道，使用 ed25519 挑战-响应进行身份认证。

```
[目标 TCP 服务]
 |
 |  <= TCP
 |
[wstunnel server]   <- 持有已授权客户端的公钥
 ||
 || <= WebSocket（ed25519 鉴权）
 ||
[反向代理 / CDN（可选）]
 ||
 || <= WebSocket
 ||
[wstunnel client]   <- 持有匹配的私钥
 |
 | <= TCP
 |
[TCP 客户端]
```

## 功能特性

- **TCP over WebSocket**：把任意 TCP 服务封装进 WebSocket 传输，便于穿过只放行 HTTP/WS 流量的网络。
- **ed25519 挑战-响应鉴权**：私钥永不上网，每次连接使用全新的随机 nonce，抓包无法重放。
- **服务端公钥白名单**：每个授权客户端放一个 `.pem` 公钥文件到鉴权目录；未授权的密钥在数据发送前就被拒绝。
- **心跳保活**：客户端每 30 秒发送 WebSocket Ping，双方在收到任意 Ping/Pong 时续约读超时，能穿过反向代理的空闲断连策略。
- **指数退避重连**：客户端 WS 拨号失败时按 1s→2s→4s→8s→16s（上限 30s）退避重试，最多 5 次。
- **安全默认**：服务端在未配置任何授权公钥时拒绝启动——不存在"无鉴权"模式。

## 编译

```
go build -o wstunnel .
```

需要 Go 1.21+（已在 Go 1.26.5 下测试）。仅依赖 `github.com/gorilla/websocket`。

## 使用方法

### 1. 生成密钥对

客户端保留 `private.pem`，服务端需要 `public.pem`。

```
wstunnel genkey -dir ./keys
```

该命令会在 `./keys/` 下生成 `private.pem` 和 `public.pem`。

### 2. 启动服务端

把每个已授权客户端的 `public.pem` 放进一个目录（一个客户端一个文件，文件名随意，只看 `.pem` 后缀）。

```
wstunnel server -bind 0.0.0.0:8888 -target 127.0.0.1:25565 -authdir ./server-keys
```

参数：
- `-bind`    监听 WebSocket 的地址（默认 `0.0.0.0:8888`）
- `-target`  要转发到的目标 TCP 服务地址（必填）
- `-authdir` 存放已授权 `*.pem` 公钥的目录（必填）

### 3. 启动客户端

```
wstunnel client -bind 127.0.0.1:25565 -url ws://server:8888/ws -key ./private.pem
```

参数：
- `-bind` 客户端本地监听的 TCP 地址（默认 `127.0.0.1:25565`）
- `-url`  服务端的 WebSocket URL（必填）
- `-key`  客户端私钥文件路径（默认 `./private.pem`）

### 4. 连接使用

客户端主机上任何 TCP 客户端访问 `127.0.0.1:25565`，流量都会被隧穿到服务端的 `-target` 地址。例如远端有 SSH 服务在 wstunnel 后面，`ssh -p 25565 user@127.0.0.1` 就像 SSH 服务在本地一样。

## 鉴权协议

握手在 WebSocket 建立后、数据转发前进行。所有握手帧都是 `BinaryMessage`，首字节为类型标识；握手通过后，后续所有 `BinaryMessage` 的 payload 都是纯 TCP 字节（无前缀、零开销）。

```
server -> client : [0x01][32 字节随机 nonce]
client -> server : [0x02][32 字节公钥][64 字节对 nonce 的 ed25519 签名]
server -> client : [0x03]                              // 通过，进入数据模式
                   或 [0x04][原因...]                   // 拒绝并断开
```

- nonce 来自 `crypto/rand` 共 32 字节，重放概率可忽略。
- 服务端先在白名单中查找声明的公钥，再调用 `ed25519.Verify` 验签。任一检查失败，客户端都能在 `[0x04]` 帧中看到具体原因（服务端日志也会记录公钥指纹）。
- 握手超时为 10 秒，用于防御慢速认证攻击。
- 握手失败时客户端**不会**重试：密钥错误或协议不匹配重试也没用。但网络层面的拨号失败会触发指数退避重试。

## 密钥格式

密钥以 PEM 格式（`PRIVATE KEY` / `PUBLIC KEY`）封装 PKCS#8 / SPKI DER 编码。纯 Go 标准库实现，不依赖 OpenSSH。使用自带的 `genkey` 子命令生成即可。

服务端日志中用于审计的公钥指纹形如 `ed25519:C2F2522C`（公钥前 4 字节的十六进制），够区分不同客户端，但不足以泄露密钥本身。

## 文件结构

- `main.go`    命令行入口（子命令：`genkey`、`server`、`client`）
- `keys.go`    密钥对的生成、加载与白名单管理
- `auth.go`    挑战-响应握手协议
- `server.go`  服务端：HTTP 升级 + 鉴权 + TCP 拨号 + 桥接 + 心跳
- `client.go`  客户端：TCP 监听 + WS 拨号(带重试) + 鉴权 + 桥接 + 心跳

## 已知限制

- 不内置 TLS。请用反向代理（nginx、Caddy）在前端终止 `wss://`。
- 不做连接多路复用，每个客户端 TCP 连接对应一条独立 WebSocket。
- 服务端会把每个字节方向的日志打到 stderr，生产环境若觉得吵可自行删减。