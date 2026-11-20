package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ipadownstudio/internal/config"
	"ipadownstudio/internal/ipatool"
	"ipadownstudio/internal/itunes"
	"ipadownstudio/internal/manager"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := config.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	hub := manager.NewEventHub()
	engine := ipatool.NewClient("")
	accounts := manager.NewAccountManager(store, engine, hub)
	versions := manager.NewVersionManager(store, engine, accounts, hub)
	downloads, err := manager.NewDownloadManager(store, engine, accounts, hub)
	if err != nil {
		t.Fatalf("NewDownloadManager error: %v", err)
	}
	t.Cleanup(func() {
		downloads.Shutdown()
	})
	transfer := manager.NewTransferManager(hub)
	t.Cleanup(func() {
		_ = transfer.Stop()
	})

	srv := New(Dependencies{
		Store:     store,
		Engine:    engine,
		ITunes:    itunes.NewClient(),
		Hub:       hub,
		Accounts:  accounts,
		Versions:  versions,
		Downloads: downloads,
		Transfer:  transfer,
	})
	return srv, tmpDir
}

func TestStaticAssetsAndOriginGuard(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, p := range []string{"/", "/app.css", "/i18n.js", "/app.js"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s error: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", p, resp.StatusCode)
		}
	}

	// Evil cross-origin POST must be rejected with 403
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/accounts/login", strings.NewReader(`{"email":"a","password":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("evil POST error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin POST, got %d", resp.StatusCode)
	}

	// Empty credentials must return 400
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/accounts/login", strings.NewReader(`{"email":"","password":""}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", ts.URL)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("same-origin empty login error: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty credentials, got %d", resp2.StatusCode)
	}
}

func TestSettingsAndSystemStatus(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/system/status")
	if err != nil {
		t.Fatalf("system status error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var sys map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sys); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if sys["appVersion"] == "" {
		t.Fatalf("missing appVersion in status")
	}

	putReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", strings.NewReader(`{"language":"en-US","theme":"dark","defaultStorefront":"us","defaultPlatform":"ipad","maxConcurrentDownloads":2,"autoPurchaseFree":false}`))
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT settings error: %v", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings status = %d", putResp.StatusCode)
	}
	var cfg config.AppConfig
	if err := json.NewDecoder(putResp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if cfg.Language != "en-US" || cfg.DefaultStorefront != "us" || cfg.MaxConcurrentDownloads != 2 {
		t.Fatalf("unexpected saved settings: %+v", cfg)
	}
}

func TestTransferFlow(t *testing.T) {
	srv, tmpDir := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create valid dummy .ipa (ZIP)
	ipaPath := filepath.Join(tmpDir, "Sample_1.0.ipa")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create("Payload/Sample.app/Info.plist")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	_, _ = fw.Write([]byte("dummy-plist-bytes-12345"))
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	expectedBytes := buf.Bytes()
	if err := os.WriteFile(ipaPath, expectedBytes, 0o644); err != nil {
		t.Fatalf("write dummy ipa: %v", err)
	}

	startBody, _ := json.Marshal(map[string]string{
		"filePath": ipaPath,
		"bindIp":   "127.0.0.1",
	})
	startResp, err := http.Post(ts.URL+"/api/transfer/start", "application/json", bytes.NewReader(startBody))
	if err != nil {
		t.Fatalf("transfer start error: %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("transfer start status = %d", startResp.StatusCode)
	}
	var trState manager.TransferState
	if err := json.NewDecoder(startResp.Body).Decode(&trState); err != nil {
		t.Fatalf("decode transfer state: %v", err)
	}
	if !trState.Active || !strings.Contains(trState.PageQRSVG, "<svg") || !strings.Contains(trState.DirectQRSVG, "</svg>") {
		t.Fatalf("unexpected transfer state: active=%v", trState.Active)
	}

	// Fetch mobile landing page
	pageResp, err := http.Get(trState.PageURL)
	if err != nil {
		t.Fatalf("GET landing page error: %v", err)
	}
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("landing page status = %d", pageResp.StatusCode)
	}

	// Fetch direct IPA file and verify exact bytes
	dlResp, err := http.Get(trState.DirectURL)
	if err != nil {
		t.Fatalf("GET direct IPA error: %v", err)
	}
	gotBytes, _ := io.ReadAll(dlResp.Body)
	dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		t.Fatalf("direct IPA status = %d", dlResp.StatusCode)
	}
	if !bytes.Equal(gotBytes, expectedBytes) {
		t.Fatalf("downloaded bytes mismatch: got %d bytes, want %d bytes", len(gotBytes), len(expectedBytes))
	}

	// Stop transfer server
	stopResp, err := http.Post(ts.URL+"/api/transfer/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("transfer stop error: %v", err)
	}
	stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("transfer stop status = %d", stopResp.StatusCode)
	}
}
