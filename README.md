---
AIGC:
  ContentProducer: '001191110102MAD55U9H0F10002'
  ContentPropagator: '001191110102MAD55U9H0F10002'
  Label: '1'
  ProduceID: 'dca56721-8bcf-4b44-acf8-06e169e9e454'
  PropagateID: 'dca56721-8bcf-4b44-acf8-06e169e9e454'
  ReservedCode1: 'd54da6c7-5428-4245-9ea3-cf42c6ade0bd'
  ReservedCode2: 'd54da6c7-5428-4245-9ea3-cf42c6ade0bd'
---

# wstunnel

TCP over WebSocket with ed25519 challenge-response authentication.

```
[something tcp server]
 |
 |  <= TCP
 |
[wstunnel server]   <- holds authorized client public keys
 ||
 || <= WebSocket (ed25519 authenticated)
 ||
[reverse proxy / CDN (optional)]
 ||
 || <= WebSocket
 ||
[wstunnel client]   <- holds the matching private key
 |
 | <= TCP
 |
[something tcp client]
```

## Features

- **TCP over WebSocket**: tunnel any TCP service through a WebSocket connection.
- **ed25519 challenge-response auth**: private key never transmitted; each
  connection uses a fresh random nonce, so captures are useless for replay.
- **Server-side public key whitelist**: drop a `.pem` file per authorized
  client into the auth dir; unknown keys are rejected before any data flows.
- **Heartbeat**: client sends WebSocket Ping every 30s; both sides reset their
  read deadline on any Ping/Pong. Survives reverse proxies with idle timeouts.
- **Reconnect with exponential backoff**: when a client-side WS dial fails, it
  retries with 1s→2s→4s→8s→16s backoff (capped at 30s), up to 5 attempts.
- **Safe defaults**: the server refuses to start if no authorized public keys
  are configured — there is no "no auth" mode.

## Build

```
go build -o wstunnel .
```

Requires Go 1.21+ (tested on Go 1.26.5). Only depends on
`github.com/gorilla/websocket`.

## Usage

### 1. Generate a keypair

The client keeps `private.pem`; the server needs `public.pem`.

```
wstunnel genkey -dir ./keys
```

This produces `./keys/private.pem` and `./keys/public.pem`.

### 2. Start the server

Place each authorized client's `public.pem` into a directory (one file per
client; the filename is irrelevant, only the `.pem` extension matters).

```
wstunnel server -bind 0.0.0.0:8888 -target 127.0.0.1:25565 -authdir ./server-keys
```

Flags:
- `-bind`    address to listen for WebSocket connections (default `0.0.0.0:8888`)
- `-target`  destination TCP service to forward traffic to (required)
- `-authdir` directory containing authorized `*.pem` public keys (required)

### 3. Start the client

```
wstunnel client -bind 127.0.0.1:25565 -url ws://server:8888/ws -key ./private.pem
```

Flags:
- `-bind` local TCP address the client listens on (default `127.0.0.1:25565`)
- `-url`  WebSocket URL of the server (required)
- `-key`  path to the client's private key (default `./private.pem`)

### 4. Connect

Any TCP client talking to `127.0.0.1:25565` on the client host is now tunneled
to the server's `-target` address. For example, with `wstunnel` in front of an
SSH server on the remote side, `ssh -p 25565 user@127.0.0.1` works as if the
SSH server were local.

## Auth protocol

The handshake runs after the WebSocket is established and before any data
crosses the wire. All handshake frames are `BinaryMessage` with a 1-byte type
prefix; once the handshake succeeds, all subsequent `BinaryMessage` payloads
are raw TCP bytes (no prefix, no overhead).

```
server -> client : [0x01][32-byte random nonce]
client -> server : [0x02][32-byte public key][64-byte ed25519 signature of nonce]
server -> client : [0x03]                              // OK, enter data mode
                   or [0x04][reason...]                 // reject and close
```

- The nonce is 32 bytes from `crypto/rand` — replay probability is negligible.
- The server looks the advertised public key up in its whitelist before
  calling `ed25519.Verify`. Whichever check fails, the client sees the exact
  reason in the `[0x04]` frame (also logged with the key fingerprint server-side).
- Handshake deadline is 10s to resist slow-auth attacks.
- If the handshake fails the client does **not** reconnect: a bad key or a
  protocol mismatch will not fix itself by retrying. Network-level dial
  failures, on the other hand, do trigger the backoff retry loop.

## Key format

Keys are stored as PEM (`PRIVATE KEY` / `PUBLIC KEY`) wrapping PKCS#8 / SPKI
DER. This is plain Go standard library, no OpenSSH dependency. Use the bundled
`genkey` subcommand to create them.

Server-side fingerprints logged for audit look like `ed25519:C2F2522C` (the
first 4 bytes of the public key in hex) — enough to tell clients apart, not
enough to be a secret.

## File layout

- `main.go`    CLI entrypoint (subcommands: `genkey`, `server`, `client`)
- `keys.go`    keypair generation, loading, and whitelist
- `auth.go`    challenge-response handshake protocol
- `server.go`  server side: HTTP upgrade + auth + TCP dial + bridge + heartbeat
- `client.go`  client side: TCP listen + WS dial w/ retry + auth + bridge + heartbeat

## Caveats

- No TLS. Put a reverse proxy (nginx, Caddy) in front to terminate `wss://`.
- No multiplexing: each TCP connection from a client opens one WebSocket.
- The server sends logs of every byte direction to stderr; trim this for
  production if you find it noisy.