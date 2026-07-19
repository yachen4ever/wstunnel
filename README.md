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

### 当前平台

```
go build -o wstunnel .
```

需要 Go 1.21+（已在 Go 1.26.5 下测试）。仅依赖 `github.com/gorilla/websocket`。

### 交叉编译三个平台

仓库自带两个等价的交叉编译脚本，用 Go 原生 `GOOS/GOARCH` 交叉编译，无需 `gox` 等外部工具。固定目标：

| 平台 | 文件 |
|------|------|
| Linux amd64 | `binaries/wstunnel-linux-amd64` |
| Windows amd64 | `binaries/wstunnel-windows-amd64.exe` |
| macOS arm64 | `binaries/wstunnel-darwin-arm64` |

```
# macOS / Linux
./crossbuild.sh            # 编译
./crossbuild.sh clean      # 清理 binaries/

# Windows PowerShell
.\crossbuild.ps1           # 编译
.\crossbuild.ps1 -Clean    # 清理 binaries\
```

脚本会从 `git describe --tags --always` 读取版本号，通过 `-ldflags` 注入到 `main.version`，可用 `wstunnel version` 查看。`-trimpath -s -w` 去掉本地路径和调试符号，产物更小。`CGO_ENABLED=0` 保证纯静态、跨容器/跨发行版可用。

GitHub Actions 在每次 push 到 `master` 和打 tag 时会自动跑这套脚本（见 `.github/workflows/`）。

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
- `-v`       打印每个字节方向的流量日志（默认关闭，详见下文「日志」一节）

### 3. 启动客户端

```
wstunnel client -bind 127.0.0.1:25565 -url ws://server:8888/ws -key ./private.pem
```

参数：
- `-bind` 客户端本地监听的 TCP 地址（默认 `127.0.0.1:25565`）
- `-url`  服务端的 WebSocket URL（必填）
- `-key`  客户端私钥文件路径（默认 `./private.pem`）
- `-v`    打印每个字节方向的流量日志（默认关闭，详见下文「日志」一节）

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

## 日志

日志默认输出到 stderr，分两档：

- **默认（安静模式）**：只打运维必要事件——服务启动、隧道建立/断开、鉴权成功/失败、拨号重试、错误。适合生产环境。
- **`-v` 详细模式**：额外打印每个字节方向的流量日志，服务端为 `C→S <字节数>` / `S→C <字节数>`，客户端为 `L→R <字节数>` / `R→L <字节数>`。适合调试。

示例（默认）：
```
2026/07/19 01:49:11 wstunnel server listening on 0.0.0.0:8888, forwarding to 127.0.0.1:25565
2026/07/19 01:49:38 tunnel established: 1.2.3.4:54321 <-> 127.0.0.1:25565 (client=ed25519:C2F2522C)
2026/07/19 01:49:58 tunnel closed: 1.2.3.4:54321 <-> 127.0.0.1:25565 (client=ed25519:C2F2522C)
```

示例（`-v` 详细模式，会多出流量行）：
```
2026/07/19 01:49:38 C→S 52
2026/07/19 01:49:38 S→C 128
```

## 部署：nginx 反向代理

wstunnel 不内置 TLS，生产环境建议在前端放 nginx 终止 `wss://`。配置时有几个关键的坑要注意。

### 必须的三件套

WebSocket 握手是 HTTP/1.1 的 `Upgrade` 机制，nginx 默认用 HTTP/1.0 代理后端，不传 Upgrade 头，wstunnel 收不到握手就会直接断。这三行**缺一不可**：

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```

### 超时配合心跳

wstunnel 的心跳参数：client 每 30s 发 Ping，双方读超时 90s。

nginx 默认 `proxy_read_timeout 60s` 偏短——虽然 WS 握手后 nginx 是透传，Ping/Pong 帧会续约 nginx 超时，但建议调大到 120s，留一次心跳丢失的余量：

```nginx
proxy_read_timeout 120s;
proxy_send_timeout 120s;
```

记住一条规则：**nginx 读超时 > wstunnel 读超时 > 2 × 心跳间隔**。当前 120 > 90 > 60，成立。

### 关掉 buffering

隧道是实时字节流，nginx 缓冲有害无益，显式关掉：

```nginx
proxy_buffering off;
```

### 完整示例

```nginx
server {
    listen 443 ssl;
    server_name tunnel.example.com;

    ssl_certificate     /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location /ws {
        proxy_pass http://127.0.0.1:8888;

        # WebSocket 三件套（必须）
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # 超时配合 wstunnel 心跳
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;

        # 实时流量,不缓冲
        proxy_buffering off;

        # 透传客户端信息（可选）
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

客户端连接：`wstunnel client -url wss://tunnel.example.com/ws -key ./private.pem ...`

### 注意事项

- **路径必须对上**：wstunnel server 只注册了 `/ws` 路径，nginx 的 `location` 要和它一致，否则会 404。
- **日志里的 client IP 是 nginx 的**：经反代后 wstunnel 看到的 `r.RemoteAddr` 是 `127.0.0.1`。nginx 配的 `X-Real-IP` / `X-Forwarded-For` 头可用于审计（当前 wstunnel 尚未读取这些头）。
- **慎用 Cloudflare 等 CDN**：免费版对 WS 有空闲断连限制且会限制子协议，wstunnel 走 CDN 多半不行，建议直连或自建反代。

## 已知限制

- 不内置 TLS。请用反向代理（nginx、Caddy）在前端终止 `wss://`。
- **单进程单目标**：一个 server 进程的 `-target` 在启动时固定，只能转发到唯一一个 TCP 服务。想同时转发多个服务（比如 SSH 和 RDP），需要起多个 server 进程，各绑不同端口、各指向自己的 `-target`。客户端同理，一个 client 进程只连一个 `-url`。
- **不做连接多路复用**：client 端每接受一个本地 TCP 连接，都会向 server 新拨一条独立 WebSocket，而不是把多条 TCP 流复用到同一条 WS 上。10 个本地连接 = 10 条 WS 连接。并发本身不受限（每条连接在独立 goroutine 中处理），但连接数较多时 WS 握手开销会比多路复用方案高。