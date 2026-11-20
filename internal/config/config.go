package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const AppVersion = "1.0.0"

// AppConfig holds user preferences persisted in config.json.
type AppConfig struct {
	Language               string `json:"language"`
	Theme                  string `json:"theme"`
	DefaultStorefront      string `json:"defaultStorefront"`
	DefaultPlatform        string `json:"defaultPlatform"`
	DownloadDir            string `json:"downloadDir"`
	MaxConcurrentDownloads int    `json:"maxConcurrentDownloads"`
	AutoPurchaseFree       bool   `json:"autoPurchaseFree"`
	CustomIpatoolPath      string `json:"customIpatoolPath"`
	ActiveProfileID        string `json:"activeProfileId"`
}

// AccountRecord stores non-secret metadata for an Apple ID profile.
// Passwords and 2FA codes are never persisted to disk.
type AccountRecord struct {
	ProfileID  string    `json:"profileId"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	Region     string    `json:"region,omitempty"`
	Status     string    `json:"status"` // "valid", "expired", "unknown"
	AddedAt    time.Time `json:"addedAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// VersionCacheEntry caches version metadata retrieved from Apple via ipatool.
type VersionCacheEntry struct {
	AppID                int64     `json:"appId"`
	Platform             string    `json:"platform"`
	ExternalVersionID    string    `json:"externalVersionId"`
	DisplayVersion       string    `json:"displayVersion"`
	ReleaseDate          string    `json:"releaseDate,omitempty"`
	ReleaseDateEstimated bool      `json:"releaseDateEstimated,omitempty"`
	SizeBytes            int64     `json:"sizeBytes,omitempty"`
	MinimumOSVersion     string    `json:"minimumOsVersion,omitempty"`
	FetchedAt            time.Time `json:"fetchedAt"`
}

// DownloadTask represents a single queued, running, paused, or completed IPA download.
type DownloadTask struct {
	ID               string     `json:"id"`
	ProfileID        string     `json:"profileId"`
	AccountEmail     string     `json:"accountEmail"`
	AppID            int64      `json:"appId"`
	BundleID         string     `json:"bundleId"`
	AppName          string     `json:"appName"`
	ArtworkURL       string     `json:"artworkUrl,omitempty"`
	Platform         string     `json:"platform"`
	Storefront       string     `json:"storefront"`
	ExternalVersionID string    `json:"externalVersionId"`
	DisplayVersion   string     `json:"displayVersion,omitempty"`
	AutoPurchase     bool       `json:"autoPurchase"`
	TargetPath       string     `json:"targetPath"`
	Status           string     `json:"status"` // queued, downloading, verifying, paused, completed, failed, cancelled
	DownloadedBytes  int64      `json:"downloadedBytes"`
	TotalBytes       int64      `json:"totalBytes"`
	SpeedBytesPerSec float64    `json:"speedBytesPerSec"`
	ETASeconds       int64      `json:"etaSeconds"`
	ErrorMessage     string     `json:"errorMessage,omitempty"`
	RequiresLicense  bool       `json:"requiresLicense,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
}

// Store manages all persistent JSON state files under DataDir.
type Store struct {
	mu      sync.RWMutex
	dataDir string
}

// ResolveDataDir returns the absolute data directory path, defaulting to <exeDir>/data.
func ResolveDataDir(override string) (string, error) {
	dir := override
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			cwd, cwErr := os.Getwd()
			if cwErr != nil {
				return "", fmt.Errorf("resolve executable or working dir: %w", err)
			}
			dir = filepath.Join(cwd, "data")
		} else {
			dir = filepath.Join(filepath.Dir(exe), "data")
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute data dir: %w", err)
	}
	return abs, nil
}

// NewStore initializes the persistent directory structure and returns a Store.
func NewStore(dataDir string) (*Store, error) {
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"", "profiles", "downloads"} {
		if err := os.MkdirAll(filepath.Join(absDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create data directory %q: %w", sub, err)
		}
	}
	s := &Store{dataDir: absDir}
	// Ensure default config exists.
	cfg, err := s.LoadConfig()
	if err != nil {
		return nil, err
	}
	if err := s.SaveConfig(cfg); err != nil {
		return nil, err
	}
	return s, nil
}

// DataDir returns the root data directory path.
func (s *Store) DataDir() string {
	return s.dataDir
}

// ProfileStateDir returns the absolute isolated ipatool state directory for a profile.
func (s *Store) ProfileStateDir(profileID string) string {
	return filepath.Join(s.dataDir, "profiles", profileID)
}

func (s *Store) DefaultConfig() AppConfig {
	return AppConfig{
		Language:               "zh-CN",
		Theme:                  "system",
		DefaultStorefront:      "cn",
		DefaultPlatform:        "iphone",
		DownloadDir:            filepath.Join(s.dataDir, "downloads"),
		MaxConcurrentDownloads: 1,
		AutoPurchaseFree:       true,
	}
}

func (s *Store) normalizeConfig(cfg AppConfig) AppConfig {
	def := s.DefaultConfig()
	if cfg.Language != "zh-CN" && cfg.Language != "en-US" {
		cfg.Language = def.Language
	}
	if cfg.Theme != "system" && cfg.Theme != "light" && cfg.Theme != "dark" {
		cfg.Theme = def.Theme
	}
	if cfg.DefaultStorefront == "" {
		cfg.DefaultStorefront = def.DefaultStorefront
	}
	if cfg.DefaultPlatform == "" {
		cfg.DefaultPlatform = def.DefaultPlatform
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = def.DownloadDir
	}
	if cfg.MaxConcurrentDownloads < 1 {
		cfg.MaxConcurrentDownloads = 1
	} else if cfg.MaxConcurrentDownloads > 3 {
		cfg.MaxConcurrentDownloads = 3
	}
	return cfg
}

// LoadConfig reads config.json or returns normalized defaults if absent.
func (s *Store) LoadConfig() (AppConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg := s.DefaultConfig()
	path := filepath.Join(s.dataDir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config.json: %w", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return s.DefaultConfig(), fmt.Errorf("parse config.json: %w", err)
		}
	}
	return s.normalizeConfig(cfg), nil
}

// SaveConfig atomically writes config.json after normalizing fields.
func (s *Store) SaveConfig(cfg AppConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg = s.normalizeConfig(cfg)
	if cfg.DownloadDir != "" {
		_ = os.MkdirAll(cfg.DownloadDir, 0o755)
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, "config.json"), cfg)
}

// LoadAccounts reads accounts.json.
func (s *Store) LoadAccounts() ([]AccountRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []AccountRecord
	err := readJSONFile(filepath.Join(s.dataDir, "accounts.json"), &list)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []AccountRecord{}
	}
	return list, nil
}

// SaveAccounts atomically writes accounts.json.
func (s *Store) SaveAccounts(list []AccountRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if list == nil {
		list = []AccountRecord{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, "accounts.json"), list)
}

// LoadVersionCache reads version_cache.json as a map keyed by "<appId>:<platform>:<extVerId>".
func (s *Store) LoadVersionCache() (map[string]VersionCacheEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var m map[string]VersionCacheEntry
	err := readJSONFile(filepath.Join(s.dataDir, "version_cache.json"), &m)
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]VersionCacheEntry{}
	}
	return m, nil
}

// SaveVersionCache atomically writes version_cache.json.
func (s *Store) SaveVersionCache(m map[string]VersionCacheEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if m == nil {
		m = map[string]VersionCacheEntry{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, "version_cache.json"), m)
}

// LoadDownloads reads downloads.json.
func (s *Store) LoadDownloads() ([]DownloadTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []DownloadTask
	err := readJSONFile(filepath.Join(s.dataDir, "downloads.json"), &list)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []DownloadTask{}
	}
	return list, nil
}

// SaveDownloads atomically writes downloads.json.
func (s *Store) SaveDownloads(list []DownloadTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if list == nil {
		list = []DownloadTask{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, "downloads.json"), list)
}

func readJSONFile(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// WriteJSONAtomic serializes v as indented JSON to a temp file and atomically renames it into place.
func WriteJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", path, err)
	}
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	payload = append(payload, '\n')
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
