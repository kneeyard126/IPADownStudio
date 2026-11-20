package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ipadownstudio/internal/qrcode"
)

// LANInterface describes a usable local network adapter and IPv4 address.
type LANInterface struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// TransferLog records one HTTP file fetch from a mobile device.
type TransferLog struct {
	RemoteAddr string    `json:"remoteAddr"`
	UserAgent  string    `json:"userAgent"`
	Bytes      int64     `json:"bytes"`
	Timestamp  time.Time `json:"timestamp"`
}

// TransferState describes the active or stopped LAN IPA sharing server.
type TransferState struct {
	Active         bool          `json:"active"`
	FilePath       string        `json:"filePath,omitempty"`
	FileName       string        `json:"fileName,omitempty"`
	FileSize       int64         `json:"fileSize,omitempty"`
	BindIP         string        `json:"bindIp,omitempty"`
	Port           int           `json:"port,omitempty"`
	PageURL        string        `json:"pageUrl,omitempty"`
	DirectURL      string        `json:"directUrl,omitempty"`
	PageQRSVG      string        `json:"pageQrSvg,omitempty"`
	DirectQRSVG    string        `json:"directQrSvg,omitempty"`
	DownloadCount  int           `json:"downloadCount"`
	Logs           []TransferLog `json:"logs"`
}

// TransferManager serves a selected `.ipa` file over the local Wi-Fi/LAN with QR codes.
type TransferManager struct {
	mu       sync.Mutex
	hub      *EventHub
	server   *http.Server
	listener net.Listener
	state    TransferState
}

// NewTransferManager creates a TransferManager.
func NewTransferManager(hub *EventHub) *TransferManager {
	return &TransferManager{
		hub:   hub,
		state: TransferState{Logs: []TransferLog{}},
	}
}

// ListLANInterfaces returns active non-loopback, non-link-local IPv4 network adapters.
func ListLANInterfaces() ([]LANInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []LANInterface
	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, LANInterface{
				Name: iface.Name,
				IP:   ip4.String(),
			})
		}
	}
	return out, nil
}

// Status returns a snapshot of the current transfer server state.
func (tm *TransferManager) Status() TransferState {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	cp := tm.state
	cp.Logs = append([]TransferLog{}, tm.state.Logs...)
	return cp
}

// Start launches the LAN sharing server for the specified `.ipa` file and bind IPv4 address.
func (tm *TransferManager) Start(filePath, bindIP string) (*TransferState, error) {
	absPath, err := filepath.Abs(strings.TrimSpace(filePath))
	if err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}
	st, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("cannot access IPA file: %w", err)
	}
	if st.IsDir() || !strings.EqualFold(filepath.Ext(absPath), ".ipa") {
		return nil, errors.New("selected file must be an existing .ipa package")
	}

	bindIP = strings.TrimSpace(bindIP)
	if bindIP == "" {
		if ifaces, err := ListLANInterfaces(); err == nil && len(ifaces) > 0 {
			bindIP = ifaces[0].IP
		} else {
			bindIP = "127.0.0.1"
		}
	}
	parsedIP := net.ParseIP(bindIP)
	if parsedIP == nil || parsedIP.To4() == nil {
		return nil, fmt.Errorf("invalid IPv4 address: %s", bindIP)
	}

	if err := tm.Stop(); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp4", net.JoinHostPort(bindIP, "0"))
	if err != nil {
		ln, err = net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			return nil, fmt.Errorf("start LAN transfer listener: %w", err)
		}
	}

	port := ln.Addr().(*net.TCPAddr).Port
	tokBytes := make([]byte, 6)
	_, _ = rand.Read(tokBytes)
	token := hex.EncodeToString(tokBytes)
	fileName := filepath.Base(absPath)
	escapedName := url.PathEscape(fileName)

	pageURL := fmt.Sprintf("http://%s:%d/t/%s", bindIP, port, token)
	directURL := fmt.Sprintf("http://%s:%d/download/%s/%s", bindIP, port, token, escapedName)

	pageQR, err := qrcode.ToSVG(pageURL, 6)
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("generate landing QR code: %w", err)
	}
	directQR, err := qrcode.ToSVG(directURL, 6)
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("generate direct QR code: %w", err)
	}

	mux := http.NewServeMux()
	downloadPrefix := "/download/" + token + "/"
	landingPath := "/t/" + token

	serveLanding := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(renderMobileLandingHTML(fileName, st.Size(), "/download/"+token+"/"+escapedName)))
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == landingPath {
			serveLanding(w, r)
			return
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc(downloadPrefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		f, openErr := os.Open(absPath)
		if openErr != nil {
			http.Error(w, "IPA file unavailable", http.StatusNotFound)
			return
		}
		defer f.Close()

		info, statErr := f.Stat()
		if statErr != nil {
			http.Error(w, "IPA file unavailable", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
			strings.ReplaceAll(fileName, `"`, "_"), escapedName))

		if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
			tm.recordFetch(r.RemoteAddr, r.UserAgent(), info.Size())
		}
		http.ServeContent(w, r, fileName, info.ModTime(), f)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	tm.mu.Lock()
	tm.server = srv
	tm.listener = ln
	tm.state = TransferState{
		Active:      true,
		FilePath:    absPath,
		FileName:    fileName,
		FileSize:    st.Size(),
		BindIP:      bindIP,
		Port:        port,
		PageURL:     pageURL,
		DirectURL:   directURL,
		PageQRSVG:   pageQR,
		DirectQRSVG: directQR,
		Logs:        []TransferLog{},
	}
	snapshot := tm.state
	tm.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()

	if tm.hub != nil {
		tm.hub.Broadcast("transfer_event", snapshot)
	}
	return &snapshot, nil
}

// Stop shuts down the active LAN transfer server.
func (tm *TransferManager) Stop() error {
	tm.mu.Lock()
	srv := tm.server
	ln := tm.listener
	wasActive := tm.state.Active
	tm.server = nil
	tm.listener = nil
	tm.state = TransferState{Logs: []TransferLog{}}
	snapshot := tm.state
	tm.mu.Unlock()

	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if wasActive && tm.hub != nil {
		tm.hub.Broadcast("transfer_event", snapshot)
	}
	return nil
}

func (tm *TransferManager) recordFetch(remoteAddr, userAgent string, size int64) {
	tm.mu.Lock()
	entry := TransferLog{
		RemoteAddr: remoteAddr,
		UserAgent:  userAgent,
		Bytes:      size,
		Timestamp:  time.Now().UTC(),
	}
	tm.state.DownloadCount++
	tm.state.Logs = append([]TransferLog{entry}, tm.state.Logs...)
	if len(tm.state.Logs) > 30 {
		tm.state.Logs = tm.state.Logs[:30]
	}
	snapshot := tm.state
	snapshot.Logs = append([]TransferLog{}, tm.state.Logs...)
	tm.mu.Unlock()

	if tm.hub != nil {
		tm.hub.Broadcast("transfer_event", snapshot)
	}
}

func formatBytesHuman(n int64) string {
	const (
		kb = 1024.0
		mb = 1024.0 * 1024.0
		gb = 1024.0 * 1024.0 * 1024.0
	)
	f := float64(n)
	switch {
	case f >= gb:
		return fmt.Sprintf("%.2f GB", f/gb)
	case f >= mb:
		return fmt.Sprintf("%.2f MB", f/mb)
	case f >= kb:
		return fmt.Sprintf("%.1f KB", f/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func renderMobileLandingHTML(fileName string, sizeBytes int64, downloadPath string) string {
	escName := html.EscapeString(fileName)
	escSize := html.EscapeString(formatBytesHuman(sizeBytes))
	escHref := html.EscapeString(downloadPath)
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>%s - IPADownStudio</title>
<style>
  :root { color-scheme: light dark; }
  body {
    margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
    padding: 20px; font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", sans-serif;
    background: #f5f5f7; color: #1d1d1f;
  }
  @media (prefers-color-scheme: dark) {
    body { background: #111214; color: #f5f5f7; }
    .card { background: #1c1e22 !important; border-color: #2f333b !important; }
    .muted { color: #9ca3af !important; }
  }
  .card {
    width: 100%%; max-width: 420px; background: #fff; border: 1px solid #e5e7eb;
    border-radius: 20px; padding: 28px 24px; box-shadow: 0 12px 32px rgba(0,0,0,0.08);
    text-align: center; box-sizing: border-box;
  }
  .badge {
    display: inline-block; font-size: 12px; font-weight: 600; padding: 4px 10px;
    border-radius: 999px; background: rgba(0,113,227,0.12); color: #0071e3; margin-bottom: 14px;
  }
  h1 { font-size: 19px; line-height: 1.35; margin: 0 0 8px; word-break: break-all; }
  .muted { font-size: 14px; color: #6b7280; margin: 0 0 22px; }
  .btn {
    display: block; width: 100%%; box-sizing: border-box; padding: 14px 18px;
    border-radius: 14px; background: #0071e3; color: #fff; text-decoration: none;
    font-size: 16px; font-weight: 600;
  }
  .note {
    margin-top: 18px; font-size: 12px; line-height: 1.5; color: #6b7280; text-align: left;
    background: rgba(128,128,128,0.08); padding: 12px; border-radius: 12px;
  }
</style>
</head>
<body>
  <div class="card">
    <div class="badge">IPADownStudio LAN Transfer</div>
    <h1>%s</h1>
    <p class="muted">大小 / Size: %s</p>
    <a class="btn" href="%s" download="%s">下载 IPA 文件 / Download IPA</a>
    <div class="note">
      此页面仅通过局域网传输原始 IPA 文件，不会在 iOS 上直接签名或安装应用。下载后请在“文件”App 中保存，或使用你的自签/安装工具导入。
    </div>
  </div>
</body>
</html>`, escName, escName, escSize, escHref, escName)
}
