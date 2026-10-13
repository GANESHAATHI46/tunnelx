package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const maxBody = 10 << 20 // 10 MiB per request/response for modern apps

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,31}$`)
var hopHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
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

type Tunnel struct {
	name      string
	conn      *websocket.Conn
	writeMu   sync.Mutex
	pending   sync.Map // request ID -> chan Message
	closed    chan struct{}
	closeOnce sync.Once
}

func (t *Tunnel) send(m Message) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return t.conn.WriteJSON(m)
}
func (t *Tunnel) close() { t.closeOnce.Do(func() { close(t.closed); _ = t.conn.Close() }) }

type LogEntry struct {
	Time       string `json:"time"`
	Tunnel     string `json:"tunnel"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type App struct {
	token, adminUser, adminPass string
	mu                          sync.RWMutex
	tunnels                     map[string]*Tunnel
	logs                        []LogEntry
	logFile                     *os.File
	seq                         atomic.Uint64
}

func main() {
	token := os.Getenv("TUNNELX_TOKEN")
	user, pass := os.Getenv("TUNNELX_ADMIN_USER"), os.Getenv("TUNNELX_ADMIN_PASS")
	if len(token) < 32 || user == "" || pass == "" {
		log.Fatal("Set TUNNELX_TOKEN (at least 32 chars), TUNNELX_ADMIN_USER and TUNNELX_ADMIN_PASS")
	}
	addr := env("TUNNELX_LISTEN", "127.0.0.1:8080")
	dataDir := env("TUNNELX_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("create data directory: %v", err)
	}
	logFile, err := os.OpenFile(dataDir+string(os.PathSeparator)+"requests.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatalf("open request log: %v", err)
	}
	a := &App{token: token, adminUser: user, adminPass: pass, tunnels: map[string]*Tunnel{}, logFile: logFile}
	if err := a.loadLogs(dataDir + string(os.PathSeparator) + "requests.jsonl"); err != nil {
		log.Printf("warning: could not load old request logs: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_tunnel/agent", a.agentHandler)
	mux.HandleFunc("/api/status", a.admin(a.statusHandler))
	mux.HandleFunc("/api/logs", a.admin(a.logsHandler))
	mux.HandleFunc("/api/metrics", a.admin(a.metricsHandler))
	mux.HandleFunc("/healthz", a.healthHandler)
	mux.HandleFunc("/", a.rootHandler)
	server := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	log.Printf("TunnelX server listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
func (a *App) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(a.adminUser)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(a.adminPass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="TunnelX dashboard"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
func (a *App) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		a.publicHandler(w, r)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, dashboardHTML)
	})(w, r)
}
func (a *App) agentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(a.token) || subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	name := r.URL.Query().Get("name")
	if !nameRE.MatchString(name) {
		http.Error(w, "invalid tunnel name", 400)
		return
	}
	up := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(15 << 20)
	t := &Tunnel{name: name, conn: conn, closed: make(chan struct{})}
	a.mu.Lock()
	if old := a.tunnels[name]; old != nil {
		old.close()
	}
	a.tunnels[name] = t
	a.mu.Unlock()
	log.Printf("tunnel connected: %s", name)
	defer func() {
		t.close()
		a.mu.Lock()
		if a.tunnels[name] == t {
			delete(a.tunnels, name)
		}
		a.mu.Unlock()
		log.Printf("tunnel disconnected: %s", name)
	}()
	for {
		var m Message
		if err := conn.ReadJSON(&m); err != nil {
			break
		}
		if m.Type != "response" || m.ID == "" {
			continue
		}
		if v, ok := t.pending.LoadAndDelete(m.ID); ok {
			ch := v.(chan Message)
			select {
			case ch <- m:
			default:
			}
		}
	}
}
func (a *App) publicHandler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/t/") {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/t/"), "/", 2)
	name := parts[0]
	if !nameRE.MatchString(name) {
		http.Error(w, "invalid tunnel name", 400)
		return
	}
	if r.Method == "CONNECT" {
		http.Error(w, "CONNECT is not supported", 405)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request body too large or unreadable (max 1 MiB)", 413)
		return
	}
	a.mu.RLock()
	t := a.tunnels[name]
	a.mu.RUnlock()
	if t == nil {
		http.Error(w, "tunnel offline", http.StatusBadGateway)
		return
	}
	path := "/"
	if len(parts) == 2 && parts[1] != "" {
		path += parts[1]
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	id := fmt.Sprintf("%s-%d", name, a.seq.Add(1))
	ch := make(chan Message, 1)
	t.pending.Store(id, ch)
	started := time.Now()
	headers := cleanHeaders(r.Header)
	forwardedProto := "http"
	if r.TLS != nil {
		forwardedProto = "https"
	}
	headers["X-Forwarded-Host"] = []string{r.Host}
	headers["X-Forwarded-Proto"] = []string{forwardedProto}
	msg := Message{Type: "request", ID: id, Name: name, Method: r.Method, Path: path, Headers: headers, Body: base64.StdEncoding.EncodeToString(body)}
	if err := t.send(msg); err != nil {
		t.pending.Delete(id)
		a.addLog(LogEntry{Time: time.Now().UTC().Format(time.RFC3339), Tunnel: name, Method: r.Method, Path: path, Status: 502, DurationMS: time.Since(started).Milliseconds(), Error: "agent write failed"})
		http.Error(w, "tunnel connection failed", 502)
		return
	}
	select {
	case resp := <-ch:
		respBody, err := base64.StdEncoding.DecodeString(resp.Body)
		if err != nil || len(respBody) > maxBody {
			http.Error(w, "invalid agent response", 502)
			return
		}
		for k, vals := range resp.Headers {
			if hopHeaders[http.CanonicalHeaderKey(k)] || strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		status := resp.Status
		if status < 100 || status > 599 {
			status = 502
		}
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		a.addLog(LogEntry{Time: time.Now().UTC().Format(time.RFC3339), Tunnel: name, Method: r.Method, Path: path, Status: status, DurationMS: time.Since(started).Milliseconds()})
	case <-time.After(35 * time.Second):
		t.pending.Delete(id)
		a.addLog(LogEntry{Time: time.Now().UTC().Format(time.RFC3339), Tunnel: name, Method: r.Method, Path: path, Status: 504, DurationMS: time.Since(started).Milliseconds(), Error: "request timeout"})
		http.Error(w, "tunnel request timed out", 504)
	case <-t.closed:
		t.pending.Delete(id)
		http.Error(w, "tunnel disconnected", 502)
	case <-r.Context().Done():
		t.pending.Delete(id)
	}
}
func cleanHeaders(in http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range in {
		ck := http.CanonicalHeaderKey(k)
		if hopHeaders[ck] || strings.EqualFold(ck, "Host") || strings.EqualFold(ck, "Content-Length") {
			continue
		}
		out[ck] = v
	}
	return out
}
func (a *App) addLog(e LogEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logs = append(a.logs, e)
	if len(a.logs) > 500 {
		a.logs = append([]LogEntry(nil), a.logs[len(a.logs)-500:]...)
	}
	if a.logFile != nil {
		if err := json.NewEncoder(a.logFile).Encode(e); err != nil {
			log.Printf("persist request log: %v", err)
		} else if err := a.logFile.Sync(); err != nil {
			log.Printf("sync request log: %v", err)
		}
	}
}
func (a *App) loadLogs(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Read JSONL records line by line and retain the most recent 500 entries.
	dec := json.NewDecoder(f)
	var all []LogEntry
	for {
		var e LogEntry
		err := dec.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		all = append(all, e)
		if len(all) > 500 {
			all = append([]LogEntry(nil), all[len(all)-500:]...)
		}
	}
	a.logs = all
	return nil
}
func (a *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"status": "ok", "service": "TunnelX"})
}
func (a *App) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, map[string]any{"active_tunnels": len(a.tunnels), "retained_request_logs": len(a.logs), "max_retained_request_logs": 500})
}
func (a *App) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.mu.RLock()
	list := make([]map[string]any, 0, len(a.tunnels))
	for n := range a.tunnels {
		list = append(list, map[string]any{"name": n, "status": "online", "public_path": "/t/" + n + "/"})
	}
	a.mu.RUnlock()
	writeJSON(w, map[string]any{"service": "TunnelX", "tunnels": list, "log_capacity": 500})
}
func (a *App) logsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.mu.RLock()
	logs := append([]LogEntry(nil), a.logs...)
	a.mu.RUnlock()
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	if len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
	writeJSON(w, map[string]any{"logs": logs})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

const dashboardHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TunnelX Dashboard</title><style>
:root{color-scheme:light;--bg:#f7f8fa;--card:#fff;--ink:#17202a;--muted:#687386;--line:#e3e7ed;--green:#16834a}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 system-ui,-apple-system,Segoe UI,sans-serif}header{background:#fff;border-bottom:1px solid var(--line);padding:18px max(20px,calc((100vw - 1100px)/2));display:flex;align-items:center;justify-content:space-between}h1{font-size:20px;margin:0}main{max-width:1100px;margin:28px auto;padding:0 20px}.sub{color:var(--muted);margin-top:4px}.grid{display:grid;grid-template-columns:1fr 2fr;gap:16px;margin:22px 0}.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:18px;min-width:0}.card h2{font-size:15px;margin:0 0 14px}.pill{display:inline-block;padding:3px 8px;border-radius:99px;background:#e8f7ee;color:var(--green);font-size:12px}.muted{color:var(--muted)}.tunnel{padding:12px 0;border-top:1px solid var(--line)}code{background:#f0f3f6;padding:3px 6px;border-radius:5px;overflow-wrap:anywhere}table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;padding:10px 8px;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--muted);font-weight:600}td.path{max-width:300px;overflow-wrap:anywhere}.tablewrap{overflow:auto}.empty{color:var(--muted);padding:14px 0}@media(max-width:700px){.grid{grid-template-columns:1fr}header{padding:16px 20px}main{margin-top:18px}}
</style></head><body><header><h1>◈ TunnelX <span class="muted">/ Dashboard</span></h1><span class="pill">Live status</span></header><main><div class="sub">Your active development tunnels and recent HTTP requests. Refreshes every 5 seconds.</div><div class="grid"><section class="card"><h2>Active tunnels</h2><div id="tunnels" class="empty">Loading…</div></section><section class="card"><h2>Connection model</h2><p>Traffic is routed through your VPS to a connected TunnelX agent. Agents connect outbound over WebSocket, so your laptop does not need an inbound port open.</p><p class="muted">Starter limits: 1 MiB request/response body, 35-second request timeout, latest 500 logs in memory.</p></section></div><section class="card"><h2>Recent requests</h2><div class="tablewrap"><table><thead><tr><th>Time (UTC)</th><th>Tunnel</th><th>Method</th><th>Path</th><th>Status</th><th>Duration</th></tr></thead><tbody id="logs"><tr><td colspan="6" class="empty">Loading…</td></tr></tbody></table></div></section></main><script>
async function refresh(){try{const [s,l]=await Promise.all([fetch('/api/status'),fetch('/api/logs?limit=100')]);if(!s.ok||!l.ok)throw Error('API request failed');const status=await s.json(),logs=(await l.json()).logs;const t=document.getElementById('tunnels');t.innerHTML=status.tunnels.length?status.tunnels.map(x=>'<div class="tunnel"><b>'+esc(x.name)+'</b> <span class="pill">online</span><p><code>'+esc(location.origin+x.public_path)+'</code></p></div>').join(''):'<div class="empty">No connected tunnels yet.</div>';document.getElementById('logs').innerHTML=logs.length?logs.map(x=>'<tr><td>'+esc(x.time)+'</td><td>'+esc(x.tunnel)+'</td><td>'+esc(x.method)+'</td><td class="path">'+esc(x.path)+'</td><td>'+esc(String(x.status))+'</td><td>'+esc(String(x.duration_ms))+' ms</td></tr>').join(''):'<tr><td colspan="6" class="empty">No requests logged yet.</td></tr>'}catch(e){document.getElementById('tunnels').textContent='Unable to load dashboard data.'}}function esc(s){return String(s).replace(/[&<>"']/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})}refresh();setInterval(refresh,5000);
</script></body></html>`
