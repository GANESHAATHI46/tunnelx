package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxBody = 10 << 20 // 10 MiB limit for modern web apps
	version = "1.0.0"
)

var hopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

type Message struct {
	Type    string              `json:"type"`
	ID      string              `json:"id,omitempty"`
	Name    string              `json:"name,omitempty"`
	Method  string              `json:"method,omitempty"`
	Path    string              `json:"path,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
	Status  int                 `json:"status,omitempty"`
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".tunnelx")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "config.json")
}

func loadConfig() Config {
	cfg := Config{
		Server: "ws://127.0.0.1:8888/_tunnel/agent",
		Token:  "12345678901234567890123456789012",
	}
	f, err := os.Open(configPath())
	if err == nil {
		defer f.Close()
		_ = json.NewDecoder(f).Decode(&cfg)
	}
	if v := os.Getenv("TUNNELX_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("TUNNELX_TOKEN"); v != "" {
		cfg.Token = v
	}
	return cfg
}

func saveConfig(cfg Config) error {
	f, err := os.OpenFile(configPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg)
}

func generateRandomName() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "app-" + hex.EncodeToString(b)
}

func printHelp() {
	fmt.Println(`
TunnelX — Self-Hosted Ngrok Alternative (CLI)

Usage:
  tunnelx http <port>              Expose local HTTP port (e.g., tunnelx http 3000)
  tunnelx <port>                   Shortcut for http <port> (e.g., tunnelx 5173)
  tunnelx http <port> --name <subdomain>  Use custom tunnel name
  tunnelx set-server <url>         Set VPS server URL (e.g., wss://tunnel.yourdomain.com/_tunnel/agent)
  tunnelx set-token <secret>       Set shared auth token
  tunnelx config                   View current configuration
  tunnelx version                  Show version

Flags:
  --server string                  Override server WebSocket URL
  --token  string                  Override auth token
  --name   string                  Custom tunnel name (letters, numbers, hyphens)
`)
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	cfg := loadConfig()

	switch args[0] {
	case "help", "-h", "--help":
		printHelp()
		return

	case "version", "-v", "--version":
		fmt.Printf("tunnelx version %s\n", version)
		return

	case "config":
		fmt.Println("TunnelX Config (" + configPath() + "):")
		fmt.Printf("  Server: %s\n", cfg.Server)
		maskedToken := cfg.Token
		if len(maskedToken) > 8 {
			maskedToken = maskedToken[:4] + "..." + maskedToken[len(maskedToken)-4:]
		}
		fmt.Printf("  Token:  %s\n", maskedToken)
		return

	case "set-server":
		if len(args) < 2 {
			log.Fatal("Usage: tunnelx set-server <url> (e.g., wss://tunnel.yourdomain.com/_tunnel/agent)")
		}
		serverURL := args[1]
		if !strings.HasPrefix(serverURL, "ws://") && !strings.HasPrefix(serverURL, "wss://") {
			if strings.HasPrefix(serverURL, "https://") {
				serverURL = "wss://" + strings.TrimPrefix(serverURL, "https://")
			} else if strings.HasPrefix(serverURL, "http://") {
				serverURL = "ws://" + strings.TrimPrefix(serverURL, "http://")
			} else {
				serverURL = "wss://" + serverURL
			}
		}
		if !strings.HasSuffix(serverURL, "/_tunnel/agent") {
			serverURL = strings.TrimRight(serverURL, "/") + "/_tunnel/agent"
		}
		cfg.Server = serverURL
		if err := saveConfig(cfg); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		fmt.Printf("✔ Server updated to: %s\n", cfg.Server)
		return

	case "set-token":
		if len(args) < 2 {
			log.Fatal("Usage: tunnelx set-token <secret-token>")
		}
		cfg.Token = args[1]
		if err := saveConfig(cfg); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		fmt.Println("✔ Auth token saved successfully!")
		return
	}

	// Parsing tunnel run commands: 'http 3000' or '3000' or flags
	var portStr string
	remainingArgs := args

	if args[0] == "http" {
		if len(args) < 2 {
			log.Fatal("Usage: tunnelx http <port>")
		}
		portStr = args[1]
		remainingArgs = args[2:]
	} else if _, err := strconv.Atoi(args[0]); err == nil {
		portStr = args[0]
		remainingArgs = args[1:]
	}

	fs := flag.NewFlagSet("tunnelx", flag.ExitOnError)
	flagServer := fs.String("server", cfg.Server, "TunnelX agent WebSocket URL")
	flagName := fs.String("name", "", "Public tunnel name")
	flagTarget := fs.String("target", "", "Target URL (e.g. http://127.0.0.1:3000)")
	flagToken := fs.String("token", cfg.Token, "Shared agent token")
	_ = fs.Parse(remainingArgs)

	target := *flagTarget
	if target == "" && portStr != "" {
		target = "http://127.0.0.1:" + portStr
	} else if target == "" {
		target = "http://127.0.0.1:8000"
	}

	name := *flagName
	if name == "" {
		name = generateRandomName()
	}

	token := *flagToken
	if token == "" {
		log.Fatal("No token configured. Run: tunnelx set-token <your-secret-token>")
	}

	serverURL := *flagServer
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		log.Fatalf("Invalid server URL %q. Run: tunnelx set-server wss://your-vps.com/_tunnel/agent", serverURL)
	}

	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()

	targetURL, err := url.Parse(target)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.Host == "" {
		log.Fatalf("Invalid target URL %q", target)
	}

	// Compute public HTTP URL
	publicScheme := "https"
	if u.Scheme == "ws" {
		publicScheme = "http"
	}
	publicURL := fmt.Sprintf("%s://%s/t/%s/", publicScheme, u.Host, name)

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          50,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   35 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	renderBanner(name, publicURL, target, version)

	for ctx.Err() == nil {
		if err := runOnce(ctx, u.String(), token, targetURL, client); err != nil && ctx.Err() == nil {
			fmt.Printf("\r[!] Reconnecting: %v (retrying in 2s)...\n", err)
		}
		select {
		case <-ctx.Done():
			fmt.Println("\nTunnel closed.")
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func renderBanner(name, publicURL, target, ver string) {
	fmt.Printf("\n")
	fmt.Println("  ⚡ TunnelX — Online HTTP Tunnel                    (Ctrl+C to quit)")
	fmt.Println("  ═══════════════════════════════════════════════════════════════════")
	fmt.Printf("  Status       : \033[32mOnline\033[0m\n")
	fmt.Printf("  Version      : %s\n", ver)
	fmt.Printf("  Tunnel Name  : %s\n", name)
	fmt.Printf("  Forwarding   : \033[36m%s\033[0m -> %s\n", publicURL, target)
	fmt.Println("  ═══════════════════════════════════════════════════════════════════")
	fmt.Println("  HTTP Request Log:")
	fmt.Printf("  %-8s %-6s %-35s %-12s %s\n", "TIME", "METHOD", "PATH", "STATUS", "DURATION")
	fmt.Println("  -------------------------------------------------------------------")
}

var writeMutex sync.Mutex

func runOnce(ctx context.Context, server, token string, target *url.URL, client *http.Client) error {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, server, h)
	if err != nil {
		if resp != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			return fmt.Errorf("connect failed (%s): %s", resp.Status, strings.TrimSpace(string(b)))
		}
		return err
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	conn.SetReadLimit(15 << 20)

	for {
		var m Message
		if err := conn.ReadJSON(&m); err != nil {
			return err
		}
		if m.Type != "request" || m.ID == "" {
			continue
		}
		go handleRequest(conn, &writeMutex, m, target, client)
	}
}

func handleRequest(conn *websocket.Conn, mu *sync.Mutex, m Message, target *url.URL, client *http.Client) {
	start := time.Now()
	body, err := base64.StdEncoding.DecodeString(m.Body)
	if err != nil || len(body) > maxBody {
		sendResponse(conn, mu, Message{
			Type:    "response",
			ID:      m.ID,
			Status:  400,
			Headers: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:    base64.StdEncoding.EncodeToString([]byte("invalid or oversized request body")),
		})
		logRequest(m.Method, m.Path, 400, time.Since(start))
		return
	}

	path := m.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parsedPath, parseErr := url.ParseRequestURI(path)
	if parseErr != nil {
		sendResponse(conn, mu, Message{
			Type:   "response",
			ID:     m.ID,
			Status: 400,
			Body:   base64.StdEncoding.EncodeToString([]byte("invalid request path")),
		})
		logRequest(m.Method, m.Path, 400, time.Since(start))
		return
	}

	dest := *target
	dest.Path = strings.TrimRight(target.Path, "/") + parsedPath.Path
	dest.RawPath = ""
	dest.RawQuery = parsedPath.RawQuery

	req, err := http.NewRequest(m.Method, dest.String(), bytes.NewReader(body))
	if err != nil {
		sendResponse(conn, mu, Message{
			Type:   "response",
			ID:     m.ID,
			Status: 400,
			Body:   base64.StdEncoding.EncodeToString([]byte("invalid request")),
		})
		logRequest(m.Method, m.Path, 400, time.Since(start))
		return
	}

	for k, vals := range m.Headers {
		if hopHeaders[http.CanonicalHeaderKey(k)] || strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Host = target.Host

	resp, err := client.Do(req)
	if err != nil {
		sendResponse(conn, mu, Message{
			Type:    "response",
			ID:      m.ID,
			Status:  502,
			Headers: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:    base64.StdEncoding.EncodeToString([]byte("local target request failed")),
		})
		logRequest(m.Method, m.Path, 502, time.Since(start))
		return
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(out) > maxBody {
		sendResponse(conn, mu, Message{
			Type:   "response",
			ID:     m.ID,
			Status: 502,
			Body:   base64.StdEncoding.EncodeToString([]byte("local response too large (max 10 MiB)")),
		})
		logRequest(m.Method, m.Path, 502, time.Since(start))
		return
	}

	headers := map[string][]string{}
	for k, v := range resp.Header {
		if hopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		headers[k] = v
	}

	sendResponse(conn, mu, Message{
		Type:    "response",
		ID:      m.ID,
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    base64.StdEncoding.EncodeToString(out),
	})
	logRequest(m.Method, m.Path, resp.StatusCode, time.Since(start))
}

func logRequest(method, path string, status int, duration time.Duration) {
	timeStr := time.Now().Format("15:04:05")
	if len(path) > 35 {
		path = path[:32] + "..."
	}
	statusColor := "\033[32m" // green
	if status >= 400 && status < 500 {
		statusColor = "\033[33m" // yellow
	} else if status >= 500 {
		statusColor = "\033[31m" // red
	}
	fmt.Printf("  %-8s %-6s %-35s %s%-12d\033[0m %dms\n", timeStr, method, path, statusColor, status, duration.Milliseconds())
}

func sendResponse(conn *websocket.Conn, mu *sync.Mutex, m Message) {
	mu.Lock()
	defer mu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.WriteJSON(m)
}
