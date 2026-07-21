package main

import (
	"flag"
	"fmt"
	"os"
)

// version 在编译时由 ldflags 注入，默认值用于 go build 不带 ldflags 的场景。
// 脚本: -ldflags "-X main.version=$(git describe --tags --always 2>/dev/null || echo dev)"
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, "wstunnel %s - TCP over WebSocket with ed25519 challenge-response auth\n", version)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  wstunnel genkey  -dir <dir>")
	fmt.Fprintln(os.Stderr, "  wstunnel server  -bind <addr> -target <addr> -authdir <dir> [-v]")
	fmt.Fprintln(os.Stderr, "  wstunnel client  -bind <addr> -url <(ws|wss)://...> -key <private.pem> [-v] [-insecure]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  genkey   Generate an ed25519 keypair into -dir (private.pem + public.pem).")
	fmt.Fprintln(os.Stderr, "  server   Run tunnel server: accepts WS on -bind, forwards TCP to -target,")
	fmt.Fprintln(os.Stderr, "           authorizes clients whose public keys are in -authdir/*.pem.")
	fmt.Fprintln(os.Stderr, "  client   Run tunnel client: listens TCP on -bind, forwards via WS -url,")
	fmt.Fprintln(os.Stderr, "           authenticates with -key private key.")
	fmt.Fprintln(os.Stderr, "           Use -insecure with wss:// to skip TLS cert verification (self-signed).")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Common flags:")
	fmt.Fprintln(os.Stderr, "  -v       Verbose: log per-byte traffic direction (off by default).")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version", "--version":
		fmt.Println("wstunnel", version)
		return

	case "genkey":
		fs := flag.NewFlagSet("genkey", flag.ExitOnError)
		dir := fs.String("dir", "./keys", "output directory for keypair")
		_ = fs.Parse(os.Args[2:])
		privPath, pubPath, err := generateKeyPair(*dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "genkey:", err)
			os.Exit(1)
		}
		fmt.Println("generated ed25519 keypair:")
		fmt.Println("  private:", privPath)
		fmt.Println("  public :", pubPath)
		fmt.Println()
		fmt.Println("Share the public key with the server (put it in the server's -authdir).")
		fmt.Println("Keep the private key on the client. Never share it.")

	case "server":
		fs := flag.NewFlagSet("server", flag.ExitOnError)
		bind := fs.String("bind", "0.0.0.0:8888", "address to listen for WS")
		target := fs.String("target", "", "destination TCP address (e.g. 127.0.0.1:25565)")
		authDir := fs.String("authdir", "./keys", "directory containing authorized *.pem public keys")
		v := fs.Bool("v", false, "verbose: log per-byte traffic direction (C→S/S→C)")
		_ = fs.Parse(os.Args[2:])
		verbose = *v
		if *target == "" {
			fmt.Fprintln(os.Stderr, "server: -target is required")
			os.Exit(2)
		}
		server(*bind, *target, *authDir)

	case "client":
		fs := flag.NewFlagSet("client", flag.ExitOnError)
		bind := fs.String("bind", "127.0.0.1:25565", "local TCP address to listen")
		url := fs.String("url", "", "websocket URL of server (e.g. ws://host:8888/ws)")
		keyPath := fs.String("key", "./private.pem", "client private key file")
		v := fs.Bool("v", false, "verbose: log per-byte traffic direction (L→R/R→L)")
		insecure := fs.Bool("insecure", false, "skip TLS certificate verification (for wss:// with self-signed certs)")
		_ = fs.Parse(os.Args[2:])
		verbose = *v
		if *url == "" {
			fmt.Fprintln(os.Stderr, "client: -url is required")
			os.Exit(2)
		}
		client(*bind, *url, *keyPath, *insecure)

	case "-h", "--help", "help":
		usage()

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}
