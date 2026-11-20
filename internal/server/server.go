package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ipadownstudio/internal/config"
	"ipadownstudio/internal/ipatool"
	"ipadownstudio/internal/itunes"
	"ipadownstudio/internal/manager"
	"ipadownstudio/internal/syswin"
	"ipadownstudio/web"
)

// Dependencies bundles the managers and clients needed by the HTTP API server.
type Dependencies struct {
	Store     *config.Store
	Engine    *ipatool.Client
	ITunes    *itunes.Client
	Hub       *manager.EventHub
	Accounts  *manager.AccountManager
	Versions  *manager.VersionManager
	Downloads *manager.DownloadManager
	Transfer  *manager.TransferManager
}

// Server exposes the embedded Web-Desktop UI and REST/SSE API on 127.0.0.1.
type Server struct {
	deps   Dependencies
	mux    *http.ServeMux
	httpSv *http.Server
}

// New creates a configured Server instance.
func New(deps Dependencies) *Server {
	s := &Server{
		deps: deps,
		mux:  http.NewServeMux(),
	}
	s.registerRoutes()
	s.httpSv = &http.Server{
		Handler:           s.originGuard(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handler returns the root HTTP handler (useful for httptest).
func (s *Server) Handler() http.Handler {
	return s.httpSv.Handler
}

// Serve begins serving HTTP requests on the given listener.
func (s *Server) Serve(ln net.Listener) error {
	return s.httpSv.Serve(ln)
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSv.Shutdown(ctx)
}

func (s *Server) registerRoutes() {
	// SSE stream
	s.mux.HandleFunc("GET /api/events", s.handleSSE)

	// System & diagnostics
	s.mux.HandleFunc("GET /api/system/status", s.handleSystemStatus)
	s.mux.HandleFunc("POST /api/system/pick-folder", s.handlePickFolder)
	s.mux.HandleFunc("POST /api/system/pick-ipa", s.handlePickIPA)
	s.mux.HandleFunc("POST /api/system/reveal", s.handleReveal)

	// Settings
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)

	// Accounts
	s.mux.HandleFunc("GET /api/accounts", s.handleGetAccounts)
	s.mux.HandleFunc("POST /api/accounts/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/accounts/login/2fa", s.handleLogin2FA)
	s.mux.HandleFunc("POST /api/accounts/login/cancel", s.handleLoginCancel)
	s.mux.HandleFunc("POST /api/accounts/{id}/activate", s.handleActivateAccount)
	s.mux.HandleFunc("POST /api/accounts/{id}/verify", s.handleVerifyAccount)
	s.mux.HandleFunc("DELETE /api/accounts/{id}", s.handleDeleteAccount)

	// Search, Purchases, Versions
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/purchases", s.handlePurchases)
	s.mux.HandleFunc("GET /api/apps/{appId}/versions", s.handleListVersions)
	s.mux.HandleFunc("POST /api/apps/{appId}/versions/metadata", s.handleHydrateMetadata)
	s.mux.HandleFunc("POST /api/apps/{appId}/purchase", s.handlePurchase)

	// Downloads
	s.mux.HandleFunc("GET /api/downloads", s.handleListDownloads)
	s.mux.HandleFunc("POST /api/downloads", s.handleEnqueueDownload)
	s.mux.HandleFunc("POST /api/downloads/{id}/pause", s.handlePauseDownload)
	s.mux.HandleFunc("POST /api/downloads/{id}/resume", s.handleResumeDownload)
	s.mux.HandleFunc("POST /api/downloads/{id}/cancel", s.handleCancelDownload)
	s.mux.HandleFunc("DELETE /api/downloads/{id}", s.handleDeleteDownload)

	// LAN Transfer
	s.mux.HandleFunc("GET /api/transfer/interfaces", s.handleTransferInterfaces)
	s.mux.HandleFunc("GET /api/transfer/status", s.handleTransferStatus)
	s.mux.HandleFunc("POST /api/transfer/start", s.handleTransferStart)
	s.mux.HandleFunc("POST /api/transfer/stop", s.handleTransferStop)

	// Embedded Static SPA
	staticSub, err := fs.Sub(web.StaticFS, ".")
	if err == nil {
		fileServer := http.FileServer(http.FS(staticSub))
		s.mux.Handle("GET /", fileServer)
	}
}

func (s *Server) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
					(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
					writeError(w, http.StatusForbidden, "origin_rejected", "cross-origin requests are forbidden")
					return
				}
				if _, reqPort, err := net.SplitHostPort(r.Host); err == nil && reqPort != "" && u.Port() != "" && u.Port() != reqPort {
					writeError(w, http.StatusForbidden, "origin_rejected", "cross-origin port mismatch")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.deps.Hub.Subscribe()
	defer unsub()

	sendEvent := func(evType string, data any) {
		b, err := json.Marshal(data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evType, b)
		flusher.Flush()
	}

	sendEvent("connected", map[string]any{"ok": true, "time": time.Now().UTC()})
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			sendEvent(ev.Type, ev.Data)
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	bin := s.deps.Engine.Info()
	snap, _ := s.deps.Accounts.Snapshot()
	var activeAcc *config.AccountRecord
	for i := range snap.Accounts {
		if snap.Accounts[i].ProfileID == snap.ActiveProfileID {
			cp := snap.Accounts[i]
			activeAcc = &cp
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"appVersion":     config.AppVersion,
		"dataDir":        s.deps.Store.DataDir(),
		"ipatoolFound":   bin.Found,
		"ipatoolPath":    bin.BinaryPath,
		"ipatoolVersion": bin.Version,
		"unicornFound":   bin.UnicornFound,
		"unicornPath":    bin.UnicornPath,
		"coreFpReady":    bin.CoreFPReady,
		"coreFpCacheDir": bin.CoreFPCacheDir,
		"unicornCached":  bin.UnicornCached,
		"activeAccount":  activeAcc,
		"storefronts":    itunes.SupportedStorefronts,
	})
}

func (s *Server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string `json:"title"`
		InitialDir string `json:"initialDir"`
	}
	_ = decodeOptionalJSON(r, &body)
	if body.Title == "" {
		body.Title = "选择文件夹 / Select Folder"
	}
	path, err := syswin.PickFolder(body.Title, body.InitialDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "dialog_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      path,
		"cancelled": path == "",
	})
}

func (s *Server) handlePickIPA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string `json:"title"`
		InitialDir string `json:"initialDir"`
	}
	_ = decodeOptionalJSON(r, &body)
	if body.Title == "" {
		body.Title = "选择 IPA 文件 / Select IPA File"
	}
	if body.InitialDir == "" {
		if cfg, err := s.deps.Store.LoadConfig(); err == nil {
			body.InitialDir = cfg.DownloadDir
		}
	}
	path, err := syswin.PickIPAFile(body.Title, body.InitialDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "dialog_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      path,
		"cancelled": path == "",
	})
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}
	abs, err := filepath.Abs(strings.TrimSpace(body.Path))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "target file or folder does not exist")
		return
	}
	if err := syswin.RevealInExplorer(abs); err != nil {
		writeError(w, http.StatusInternalServerError, "reveal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.deps.Store.LoadConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var incoming config.AppConfig
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	cur, err := s.deps.Store.LoadConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error())
		return
	}
	// Preserve activeProfileId unless explicitly provided
	if incoming.ActiveProfileID == "" {
		incoming.ActiveProfileID = cur.ActiveProfileID
	}
	if err := s.deps.Store.SaveConfig(incoming); err != nil {
		writeError(w, http.StatusInternalServerError, "config_save_error", err.Error())
		return
	}
	saved, _ := s.deps.Store.LoadConfig()
	s.deps.Engine.SetCustomPath(saved.CustomIpatoolPath)
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleGetAccounts(w http.ResponseWriter, r *http.Request) {
	snap, err := s.deps.Accounts.Snapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "accounts_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Region   string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(body.Email) == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "missing_credentials", "email and password are required")
		return
	}
	res, err := s.deps.Accounts.StartLogin(r.Context(), body.Email, body.Password, body.Region)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleLogin2FA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string `json:"challengeId"`
		AuthCode    string `json:"authCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(body.ChallengeID) == "" || strings.TrimSpace(body.AuthCode) == "" {
		writeError(w, http.StatusBadRequest, "missing_2fa", "challengeId and authCode are required")
		return
	}
	res, err := s.deps.Accounts.Submit2FA(r.Context(), body.ChallengeID, body.AuthCode)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleLoginCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string `json:"challengeId"`
	}
	_ = decodeOptionalJSON(r, &body)
	if body.ChallengeID != "" {
		s.deps.Accounts.Cancel2FA(body.ChallengeID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleActivateAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.deps.Accounts.SwitchAccount(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "account_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleVerifyAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.deps.Accounts.VerifyAccount(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":   false,
			"account": rec,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":   true,
		"account": rec,
	})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.Accounts.RemoveAccount(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "remove_account_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing_query", "q parameter is required")
		return
	}
	cfg, _ := s.deps.Store.LoadConfig()
	country := r.URL.Query().Get("country")
	if country == "" {
		country = cfg.DefaultStorefront
	}
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		platform = cfg.DefaultPlatform
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 25
	}

	res, err := s.deps.ITunes.Search(r.Context(), q, country, platform, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "itunes_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type enrichedPurchase struct {
	ipatool.PurchasedApp
	ArtworkURL       string `json:"artworkUrl,omitempty"`
	ArtistName       string `json:"artistName,omitempty"`
	FileSizeBytes    int64  `json:"fileSizeBytes,omitempty"`
	MinimumOSVersion string `json:"minimumOsVersion,omitempty"`
	FormattedPrice   string `json:"formattedPrice,omitempty"`
}

func (s *Server) handlePurchases(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.deps.Store.LoadConfig()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 20
	}
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		platform = cfg.DefaultPlatform
	}
	country := r.URL.Query().Get("country")
	if country == "" {
		country = cfg.DefaultStorefront
	}
	profileID := r.URL.Query().Get("profileId")

	rec, stateDir, err := s.deps.Accounts.GetProfile(profileID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "no_active_account", err.Error())
		return
	}
	res, err := s.deps.Engine.ListPurchases(r.Context(), rec.ProfileID, stateDir, page, limit, platform)
	if err != nil {
		writeEngineError(w, err)
		return
	}

	ids := make([]int64, 0, len(res.Apps))
	for _, a := range res.Apps {
		if a.ID > 0 {
			ids = append(ids, a.ID)
		}
	}
	metaMap, _ := s.deps.ITunes.BatchLookup(r.Context(), ids, country)

	enriched := make([]enrichedPurchase, 0, len(res.Apps))
	for _, a := range res.Apps {
		item := enrichedPurchase{PurchasedApp: a}
		if info, ok := metaMap[a.ID]; ok {
			item.ArtworkURL = info.ArtworkURL
			item.ArtistName = info.ArtistName
			item.FileSizeBytes = info.FileSizeBytes
			item.MinimumOSVersion = info.MinimumOSVersion
			item.FormattedPrice = info.FormattedPrice
			if item.Name == "" {
				item.Name = info.TrackName
			}
			if item.BundleID == "" {
				item.BundleID = info.BundleID
			}
			if item.Version == "" {
				item.Version = info.Version
			}
		}
		enriched = append(enriched, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"count":      res.Count,
		"totalCount": res.TotalCount,
		"page":       res.Page,
		"maxResults": res.MaxResults,
		"platform":   res.Platform,
		"apps":       enriched,
	})
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	appID, err := strconv.ParseInt(r.PathValue("appId"), 10, 64)
	if err != nil || appID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_app_id", "invalid appId path parameter")
		return
	}
	bundleID := strings.TrimSpace(r.URL.Query().Get("bundleId"))
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))

	res, err := s.deps.Versions.ListVersions(r.Context(), profileID, appID, bundleID, platform)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHydrateMetadata(w http.ResponseWriter, r *http.Request) {
	appID, err := strconv.ParseInt(r.PathValue("appId"), 10, 64)
	if err != nil || appID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_app_id", "invalid appId path parameter")
		return
	}
	var body struct {
		BundleID           string   `json:"bundleId"`
		Platform           string   `json:"platform"`
		ProfileID          string   `json:"profileId"`
		ExternalVersionIDs []string `json:"externalVersionIds"`
		Force              bool     `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if len(body.ExternalVersionIDs) == 0 {
		writeError(w, http.StatusBadRequest, "missing_version_ids", "externalVersionIds is required")
		return
	}
	if len(body.ExternalVersionIDs) > 30 {
		body.ExternalVersionIDs = body.ExternalVersionIDs[:30]
	}

	entries, err := s.deps.Versions.HydrateMetadata(r.Context(), body.ProfileID, appID, body.BundleID, body.Platform, body.ExternalVersionIDs, body.Force)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": entries})
}

func (s *Server) handlePurchase(w http.ResponseWriter, r *http.Request) {
	appID, err := strconv.ParseInt(r.PathValue("appId"), 10, 64)
	if err != nil || appID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_app_id", "invalid appId path parameter")
		return
	}
	var body struct {
		BundleID  string `json:"bundleId"`
		Platform  string `json:"platform"`
		ProfileID string `json:"profileId"`
	}
	_ = decodeOptionalJSON(r, &body)
	res, err := s.deps.Versions.Purchase(r.Context(), body.ProfileID, appID, body.BundleID, body.Platform)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": s.deps.Downloads.List(),
	})
}

func (s *Server) handleEnqueueDownload(w http.ResponseWriter, r *http.Request) {
	var req manager.EnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	task, err := s.deps.Downloads.Enqueue(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "enqueue_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	task, err := s.deps.Downloads.Pause(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AutoPurchase *bool `json:"autoPurchase"`
	}
	_ = decodeOptionalJSON(r, &body)
	task, err := s.deps.Downloads.Resume(r.PathValue("id"), body.AutoPurchase)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	task, err := s.deps.Downloads.Cancel(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	deleteFile := strings.EqualFold(r.URL.Query().Get("deleteFile"), "true") || r.URL.Query().Get("deleteFile") == "1"
	if err := s.deps.Downloads.Delete(r.PathValue("id"), deleteFile); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTransferInterfaces(w http.ResponseWriter, r *http.Request) {
	ifaces, err := manager.ListLANInterfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "network_error", err.Error())
		return
	}
	if ifaces == nil {
		ifaces = []manager.LANInterface{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": ifaces})
}

func (s *Server) handleTransferStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Transfer.Status())
}

func (s *Server) handleTransferStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FilePath string `json:"filePath"`
		BindIP   string `json:"bindIp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(body.FilePath) == "" {
		writeError(w, http.StatusBadRequest, "missing_file_path", "filePath is required")
		return
	}
	state, err := s.deps.Transfer.Start(body.FilePath, body.BindIP)
	if err != nil {
		writeError(w, http.StatusBadRequest, "transfer_start_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleTransferStop(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Transfer.Stop(); err != nil {
		writeError(w, http.StatusInternalServerError, "transfer_stop_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Transfer.Status())
}

func decodeOptionalJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, map[string]any{
		"error":     msg,
		"errorKind": kind,
	})
}

func writeEngineError(w http.ResponseWriter, err error) {
	var eng *ipatool.EngineError
	if errors.As(err, &eng) {
		status := http.StatusBadRequest
		switch eng.Kind {
		case ipatool.KindBinaryNotFound:
			status = http.StatusServiceUnavailable
		case ipatool.KindInvalidCredentials, ipatool.KindAuthExpired:
			status = http.StatusUnauthorized
		case ipatool.KindLicenseRequired:
			status = http.StatusForbidden
		case ipatool.KindCommandFailed:
			status = http.StatusBadGateway
		}
		writeError(w, status, string(eng.Kind), eng.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "error", err.Error())
}
