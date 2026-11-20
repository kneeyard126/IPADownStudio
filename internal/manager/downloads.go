package manager

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ipadownstudio/internal/config"
	"ipadownstudio/internal/ipatool"
)

// EnqueueRequest captures parameters for queuing an IPA download.
type EnqueueRequest struct {
	ProfileID         string `json:"profileId"`
	AppID             int64  `json:"appId"`
	BundleID          string `json:"bundleId"`
	AppName           string `json:"appName"`
	ArtworkURL        string `json:"artworkUrl"`
	Platform          string `json:"platform"`
	Storefront        string `json:"storefront"`
	ExternalVersionID string `json:"externalVersionId"`
	DisplayVersion    string `json:"displayVersion"`
	ExpectedSizeBytes int64  `json:"expectedSizeBytes"`
	AutoPurchase      *bool  `json:"autoPurchase"`
}

type activeJob struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	intent string // "", "pause", "cancel"
}

// DownloadManager orchestrates the persistent download queue, concurrency limit, live progress, and pause/resume.
type DownloadManager struct {
	mu       sync.Mutex
	store    *config.Store
	engine   *ipatool.Client
	accounts *AccountManager
	hub      *EventHub

	tasks     []config.DownloadTask
	active    map[string]*activeJob
	wakeCh    chan struct{}
	stopCh    chan struct{}
	wg        sync.WaitGroup
	shuttingD bool
}

// NewDownloadManager loads saved tasks, normalizes interrupted tasks to `paused`, and starts the queue worker.
func NewDownloadManager(store *config.Store, engine *ipatool.Client, accounts *AccountManager, hub *EventHub) (*DownloadManager, error) {
	tasks, err := store.LoadDownloads()
	if err != nil {
		return nil, err
	}
	dirty := false
	now := time.Now().UTC()
	for i := range tasks {
		if tasks[i].Status == "downloading" || tasks[i].Status == "verifying" {
			tasks[i].Status = "paused"
			tasks[i].SpeedBytesPerSec = 0
			tasks[i].ETASeconds = 0
			tasks[i].UpdatedAt = now
			dirty = true
		}
	}
	if dirty {
		_ = store.SaveDownloads(tasks)
	}

	dm := &DownloadManager{
		store:    store,
		engine:   engine,
		accounts: accounts,
		hub:      hub,
		tasks:    tasks,
		active:   make(map[string]*activeJob),
		wakeCh:   make(chan struct{}, 16),
		stopCh:   make(chan struct{}),
	}
	dm.wg.Add(1)
	go dm.schedulerLoop()
	dm.wake()
	return dm, nil
}

// List returns a copy of all download tasks ordered newest-first.
func (dm *DownloadManager) List() []config.DownloadTask {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	out := make([]config.DownloadTask, len(dm.tasks))
	for i := range dm.tasks {
		out[i] = dm.tasks[len(dm.tasks)-1-i]
	}
	return out
}

// Enqueue creates a new download task, saves it, and wakes the scheduler.
func (dm *DownloadManager) Enqueue(req EnqueueRequest) (*config.DownloadTask, error) {
	if req.AppID <= 0 && strings.TrimSpace(req.BundleID) == "" {
		return nil, errors.New("appId or bundleId is required")
	}
	rec, _, err := dm.accounts.GetProfile(req.ProfileID)
	if err != nil {
		return nil, err
	}
	cfg, err := dm.store.LoadConfig()
	if err != nil {
		return nil, err
	}
	if req.Platform == "" {
		req.Platform = cfg.DefaultPlatform
	}
	if req.Storefront == "" {
		req.Storefront = cfg.DefaultStorefront
	}
	autoPurchase := cfg.AutoPurchaseFree
	if req.AutoPurchase != nil {
		autoPurchase = *req.AutoPurchase
	}

	dlDir := cfg.DownloadDir
	if dlDir == "" {
		dlDir = dm.store.DefaultConfig().DownloadDir
	}
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return nil, fmt.Errorf("create download directory: %w", err)
	}

	targetPath := dm.buildTargetPath(dlDir, req)
	now := time.Now().UTC()
	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	task := config.DownloadTask{
		ID:                "dl_" + hex.EncodeToString(idBytes),
		ProfileID:         rec.ProfileID,
		AccountEmail:      rec.Email,
		AppID:             req.AppID,
		BundleID:          strings.TrimSpace(req.BundleID),
		AppName:           strings.TrimSpace(req.AppName),
		ArtworkURL:        strings.TrimSpace(req.ArtworkURL),
		Platform:          req.Platform,
		Storefront:        req.Storefront,
		ExternalVersionID: strings.TrimSpace(req.ExternalVersionID),
		DisplayVersion:    strings.TrimSpace(req.DisplayVersion),
		AutoPurchase:      autoPurchase,
		TargetPath:        targetPath,
		Status:            "queued",
		TotalBytes:        req.ExpectedSizeBytes,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if task.AppName == "" {
		if task.BundleID != "" {
			task.AppName = task.BundleID
		} else {
			task.AppName = fmt.Sprintf("App_%d", task.AppID)
		}
	}

	dm.mu.Lock()
	dm.tasks = append(dm.tasks, task)
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()

	dm.broadcastTask(task)
	dm.wake()
	return &task, nil
}

// Pause stops a running download while keeping `<targetPath>.tmp` intact so Resume continues seamlessly.
func (dm *DownloadManager) Pause(id string) (*config.DownloadTask, error) {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(id)
	if idx == -1 {
		dm.mu.Unlock()
		return nil, fmt.Errorf("download task %q not found", id)
	}
	t := &dm.tasks[idx]
	if job, running := dm.active[id]; running {
		job.intent = "pause"
		if job.cmd != nil && job.cmd.Process != nil {
			_ = job.cmd.Process.Kill()
		}
		if job.cancel != nil {
			job.cancel()
		}
		copyTask := *t
		dm.mu.Unlock()
		return &copyTask, nil
	}
	if t.Status == "queued" {
		t.Status = "paused"
		t.SpeedBytesPerSec = 0
		t.ETASeconds = 0
		t.UpdatedAt = time.Now().UTC()
		copyTask := *t
		_ = dm.store.SaveDownloads(dm.tasks)
		dm.mu.Unlock()
		dm.broadcastTask(copyTask)
		return &copyTask, nil
	}
	copyTask := *t
	dm.mu.Unlock()
	return &copyTask, nil
}

// Resume re-queues a paused, failed, or cancelled download task (with optional autoPurchase override).
func (dm *DownloadManager) Resume(id string, forcePurchase *bool) (*config.DownloadTask, error) {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(id)
	if idx == -1 {
		dm.mu.Unlock()
		return nil, fmt.Errorf("download task %q not found", id)
	}
	t := &dm.tasks[idx]
	if _, running := dm.active[id]; running {
		copyTask := *t
		dm.mu.Unlock()
		return &copyTask, nil
	}
	if t.Status == "completed" {
		copyTask := *t
		dm.mu.Unlock()
		return &copyTask, nil
	}
	if forcePurchase != nil {
		t.AutoPurchase = *forcePurchase
	}
	t.Status = "queued"
	t.ErrorMessage = ""
	t.RequiresLicense = false
	t.SpeedBytesPerSec = 0
	t.ETASeconds = 0
	t.UpdatedAt = time.Now().UTC()
	copyTask := *t
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()

	dm.broadcastTask(copyTask)
	dm.wake()
	return &copyTask, nil
}

// Cancel stops a download and cleans up any partial `.tmp` file.
func (dm *DownloadManager) Cancel(id string) (*config.DownloadTask, error) {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(id)
	if idx == -1 {
		dm.mu.Unlock()
		return nil, fmt.Errorf("download task %q not found", id)
	}
	t := &dm.tasks[idx]
	if job, running := dm.active[id]; running {
		job.intent = "cancel"
		if job.cmd != nil && job.cmd.Process != nil {
			_ = job.cmd.Process.Kill()
		}
		if job.cancel != nil {
			job.cancel()
		}
		copyTask := *t
		dm.mu.Unlock()
		return &copyTask, nil
	}
	_ = os.Remove(t.TargetPath + ".tmp")
	t.Status = "cancelled"
	t.SpeedBytesPerSec = 0
	t.ETASeconds = 0
	t.UpdatedAt = time.Now().UTC()
	copyTask := *t
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()

	dm.broadcastTask(copyTask)
	return &copyTask, nil
}

// Delete removes a download task and optionally deletes the downloaded `.ipa` file.
func (dm *DownloadManager) Delete(id string, deleteFile bool) error {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(id)
	if idx == -1 {
		dm.mu.Unlock()
		return fmt.Errorf("download task %q not found", id)
	}
	targetPath := dm.tasks[idx].TargetPath
	if job, running := dm.active[id]; running {
		job.intent = "cancel"
		if job.cmd != nil && job.cmd.Process != nil {
			_ = job.cmd.Process.Kill()
		}
		if job.cancel != nil {
			job.cancel()
		}
	}
	dm.tasks = append(dm.tasks[:idx], dm.tasks[idx+1:]...)
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()

	_ = os.Remove(targetPath + ".tmp")
	if deleteFile {
		_ = os.Remove(targetPath)
	}
	if dm.hub != nil {
		dm.hub.Broadcast("download_removed", map[string]string{"id": id})
	}
	return nil
}

// Shutdown pauses any active child download subprocesses cleanly so `.tmp` files remain resumable.
func (dm *DownloadManager) Shutdown() {
	dm.mu.Lock()
	dm.shuttingD = true
	for _, job := range dm.active {
		job.intent = "pause"
		if job.cmd != nil && job.cmd.Process != nil {
			_ = job.cmd.Process.Kill()
		}
		if job.cancel != nil {
			job.cancel()
		}
	}
	dm.mu.Unlock()

	close(dm.stopCh)
	dm.wg.Wait()
}

func (dm *DownloadManager) wake() {
	select {
	case dm.wakeCh <- struct{}{}:
	default:
	}
}

func (dm *DownloadManager) schedulerLoop() {
	defer dm.wg.Done()
	for {
		dm.dispatchAvailable()
		select {
		case <-dm.stopCh:
			return
		case <-dm.wakeCh:
		case <-time.After(1 * time.Second):
		}
	}
}

func (dm *DownloadManager) dispatchAvailable() {
	cfg, err := dm.store.LoadConfig()
	if err != nil {
		return
	}
	maxConc := cfg.MaxConcurrentDownloads
	if maxConc < 1 {
		maxConc = 1
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()
	if dm.shuttingD {
		return
	}
	for len(dm.active) < maxConc {
		var nextID string
		for i := range dm.tasks {
			if dm.tasks[i].Status == "queued" {
				if _, already := dm.active[dm.tasks[i].ID]; !already {
					nextID = dm.tasks[i].ID
					break
				}
			}
		}
		if nextID == "" {
			break
		}
		ctx, cancel := context.WithCancel(context.Background())
		job := &activeJob{cancel: cancel}
		dm.active[nextID] = job
		dm.wg.Add(1)
		go dm.runJob(ctx, nextID, job)
	}
}

func (dm *DownloadManager) runJob(ctx context.Context, taskID string, job *activeJob) {
	defer dm.wg.Done()
	defer func() {
		dm.mu.Lock()
		delete(dm.active, taskID)
		dm.mu.Unlock()
		dm.wake()
	}()

	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(taskID)
	if idx == -1 {
		dm.mu.Unlock()
		return
	}
	task := dm.tasks[idx]
	task.Status = "downloading"
	task.ErrorMessage = ""
	task.RequiresLicense = false
	task.UpdatedAt = time.Now().UTC()
	dm.tasks[idx] = task
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()
	dm.broadcastTask(task)

	rec, stateDir, err := dm.accounts.GetProfile(task.ProfileID)
	if err != nil {
		dm.finishFailed(taskID, err.Error(), false)
		return
	}

	cmd, stdout, stderr, err := dm.engine.StartDownload(ctx, rec.ProfileID, stateDir, ipatool.DownloadOpts{
		AppID:             task.AppID,
		BundleID:          task.BundleID,
		ExternalVersionID: task.ExternalVersionID,
		Platform:          task.Platform,
		OutputPath:        task.TargetPath,
		Purchase:          task.AutoPurchase,
	})
	if err != nil {
		dm.finishFailed(taskID, err.Error(), errors.Is(err, ipatool.ErrLicenseRequired))
		return
	}

	dm.mu.Lock()
	job.cmd = cmd
	dm.mu.Unlock()

	var logMu sync.Mutex
	var rawLines []string
	var detectedTotal int64

	readPipe := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			logMu.Lock()
			rawLines = append(rawLines, line)
			if strings.HasPrefix(line, "{") {
				var obj map[string]any
				if json.Unmarshal([]byte(line), &obj) == nil {
					for _, k := range []string{"sizeBytes", "size_bytes", "totalBytes", "total_bytes"} {
						if v, ok := obj[k].(float64); ok && int64(v) > detectedTotal {
							detectedTotal = int64(v)
						}
					}
				}
			}
			logMu.Unlock()
		}
	}

	var pipeWg sync.WaitGroup
	pipeWg.Add(2)
	go func() { defer pipeWg.Done(); readPipe(stdout) }()
	go func() { defer pipeWg.Done(); readPipe(stderr) }()

	stopMonitor := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		dm.monitorProgress(taskID, task.TargetPath, &logMu, &detectedTotal, stopMonitor)
	}()

	waitErr := cmd.Wait()
	pipeWg.Wait()
	close(stopMonitor)
	<-monitorDone

	dm.mu.Lock()
	intent := job.intent
	dm.mu.Unlock()

	if intent == "pause" {
		dm.updatePaused(taskID, task.TargetPath)
		return
	}
	if intent == "cancel" {
		_ = os.Remove(task.TargetPath + ".tmp")
		_ = os.Remove(task.TargetPath)
		dm.updateCancelled(taskID)
		return
	}

	if waitErr != nil {
		logMu.Lock()
		combined := strings.Join(rawLines, "\n")
		logMu.Unlock()
		objs, plain := ipatool.ParseNDJSONLines(combined)
		extracted := ipatool.ExtractError(objs, plain, waitErr)
		msg := waitErr.Error()
		reqLic := false
		if extracted != nil {
			msg = extracted.Error()
			reqLic = errors.Is(extracted, ipatool.ErrLicenseRequired)
		}
		dm.finishFailed(taskID, msg, reqLic)
		return
	}

	// Verify final IPA ZIP container integrity
	dm.setStatus(taskID, "verifying")
	st, statErr := os.Stat(task.TargetPath)
	if statErr != nil || st.Size() == 0 {
		dm.finishFailed(taskID, "downloaded IPA file is missing or empty", false)
		return
	}
	zr, zipErr := zip.OpenReader(task.TargetPath)
	if zipErr != nil {
		dm.finishFailed(taskID, fmt.Sprintf("downloaded IPA verification failed: %v", zipErr), false)
		return
	}
	_ = zr.Close()

	now := time.Now().UTC()
	dm.mu.Lock()
	idx = dm.findTaskIdxLocked(taskID)
	if idx != -1 {
		t := &dm.tasks[idx]
		t.Status = "completed"
		t.DownloadedBytes = st.Size()
		t.TotalBytes = st.Size()
		t.SpeedBytesPerSec = 0
		t.ETASeconds = 0
		t.UpdatedAt = now
		t.CompletedAt = &now
		copyTask := *t
		_ = dm.store.SaveDownloads(dm.tasks)
		dm.mu.Unlock()
		dm.broadcastTask(copyTask)
		return
	}
	dm.mu.Unlock()
}

func (dm *DownloadManager) monitorProgress(taskID, targetPath string, logMu *sync.Mutex, detectedTotal *int64, stopCh <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	type sample struct {
		at    time.Time
		bytes int64
	}
	var samples []sample

	for {
		select {
		case <-stopCh:
			return
		case now := <-ticker.C:
			var curBytes int64
			if st, err := os.Stat(targetPath + ".tmp"); err == nil {
				curBytes = st.Size()
			} else if st, err := os.Stat(targetPath); err == nil {
				curBytes = st.Size()
			}

			logMu.Lock()
			knownTotal := *detectedTotal
			logMu.Unlock()

			samples = append(samples, sample{at: now, bytes: curBytes})
			cutoff := now.Add(-3 * time.Second)
			for len(samples) > 1 && samples[0].at.Before(cutoff) {
				samples = samples[1:]
			}

			var speed float64
			if len(samples) >= 2 {
				dt := samples[len(samples)-1].at.Sub(samples[0].at).Seconds()
				db := samples[len(samples)-1].bytes - samples[0].bytes
				if dt > 0.1 && db >= 0 {
					speed = float64(db) / dt
				}
			}

			dm.mu.Lock()
			idx := dm.findTaskIdxLocked(taskID)
			if idx != -1 {
				t := &dm.tasks[idx]
				if knownTotal > t.TotalBytes {
					t.TotalBytes = knownTotal
				}
				t.DownloadedBytes = curBytes
				t.SpeedBytesPerSec = speed
				if speed > 0 && t.TotalBytes > curBytes {
					t.ETASeconds = int64(float64(t.TotalBytes-curBytes) / speed)
				} else {
					t.ETASeconds = 0
				}
				t.UpdatedAt = time.Now().UTC()
				copyTask := *t
				dm.mu.Unlock()
				dm.broadcastTask(copyTask)
			} else {
				dm.mu.Unlock()
			}
		}
	}
}

func (dm *DownloadManager) setStatus(taskID, status string) {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(taskID)
	if idx == -1 {
		dm.mu.Unlock()
		return
	}
	dm.tasks[idx].Status = status
	dm.tasks[idx].UpdatedAt = time.Now().UTC()
	copyTask := dm.tasks[idx]
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()
	dm.broadcastTask(copyTask)
}

func (dm *DownloadManager) updatePaused(taskID, targetPath string) {
	var curBytes int64
	if st, err := os.Stat(targetPath + ".tmp"); err == nil {
		curBytes = st.Size()
	}
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(taskID)
	if idx == -1 {
		dm.mu.Unlock()
		return
	}
	t := &dm.tasks[idx]
	t.Status = "paused"
	if curBytes > 0 {
		t.DownloadedBytes = curBytes
	}
	t.SpeedBytesPerSec = 0
	t.ETASeconds = 0
	t.UpdatedAt = time.Now().UTC()
	copyTask := *t
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()
	dm.broadcastTask(copyTask)
}

func (dm *DownloadManager) updateCancelled(taskID string) {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(taskID)
	if idx == -1 {
		dm.mu.Unlock()
		return
	}
	t := &dm.tasks[idx]
	t.Status = "cancelled"
	t.SpeedBytesPerSec = 0
	t.ETASeconds = 0
	t.UpdatedAt = time.Now().UTC()
	copyTask := *t
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()
	dm.broadcastTask(copyTask)
}

func (dm *DownloadManager) finishFailed(taskID, errMsg string, reqLic bool) {
	dm.mu.Lock()
	idx := dm.findTaskIdxLocked(taskID)
	if idx == -1 {
		dm.mu.Unlock()
		return
	}
	t := &dm.tasks[idx]
	t.Status = "failed"
	t.ErrorMessage = errMsg
	t.RequiresLicense = reqLic
	t.SpeedBytesPerSec = 0
	t.ETASeconds = 0
	t.UpdatedAt = time.Now().UTC()
	copyTask := *t
	_ = dm.store.SaveDownloads(dm.tasks)
	dm.mu.Unlock()
	dm.broadcastTask(copyTask)
}

func (dm *DownloadManager) findTaskIdxLocked(id string) int {
	for i := range dm.tasks {
		if dm.tasks[i].ID == id {
			return i
		}
	}
	return -1
}

func (dm *DownloadManager) broadcastTask(t config.DownloadTask) {
	if dm.hub != nil {
		dm.hub.Broadcast("download_update", t)
	}
}

func (dm *DownloadManager) buildTargetPath(dlDir string, req EnqueueRequest) string {
	appPart := sanitizeFilenamePart(req.AppName)
	if appPart == "" {
		appPart = sanitizeFilenamePart(req.BundleID)
	}
	if appPart == "" {
		appPart = fmt.Sprintf("App_%d", req.AppID)
	}
	verPart := sanitizeFilenamePart(req.DisplayVersion)
	extPart := sanitizeFilenamePart(req.ExternalVersionID)
	if extPart == "" {
		extPart = "latest"
	}

	var base string
	if verPart != "" {
		base = fmt.Sprintf("%s_%s_%s", appPart, verPart, extPart)
	} else {
		base = fmt.Sprintf("%s_%s", appPart, extPart)
	}

	cand := filepath.Join(dlDir, base+".ipa")
	for suffix := 1; suffix < 100; suffix++ {
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			if _, tmpErr := os.Stat(cand + ".tmp"); errors.Is(tmpErr, os.ErrNotExist) {
				return cand
			}
		}
		cand = filepath.Join(dlDir, fmt.Sprintf("%s_(%d).ipa", base, suffix))
	}
	return filepath.Join(dlDir, fmt.Sprintf("%s_%d.ipa", base, time.Now().Unix()))
}

func sanitizeFilenamePart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"<", "_", ">", "_", ":", "_", "\"", "_", "/", "_",
		"\\", "_", "|", "_", "?", "_", "*", "_", "\r", "", "\n", "", "\t", " ",
	)
	clean := strings.TrimSpace(replacer.Replace(s))
	clean = strings.Trim(clean, ". ")
	runes := []rune(clean)
	if len(runes) > 50 {
		clean = string(runes[:50])
	}
	return strings.TrimSpace(clean)
}
