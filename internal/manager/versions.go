package manager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"ipadownstudio/internal/config"
	"ipadownstudio/internal/ipatool"
)

// VersionEntry pairs an ExternalVersionID with cached or freshly fetched metadata.
type VersionEntry struct {
	ExternalVersionID    string `json:"externalVersionId"`
	IsLatest             bool   `json:"isLatest"`
	DisplayVersion       string `json:"displayVersion,omitempty"`
	ReleaseDate          string `json:"releaseDate,omitempty"`
	ReleaseDateEstimated bool   `json:"releaseDateEstimated,omitempty"`
	SizeBytes            int64  `json:"sizeBytes,omitempty"`
	MinimumOSVersion     string `json:"minimumOsVersion,omitempty"`
	Cached               bool   `json:"cached"`
	Error                string `json:"error,omitempty"`
}

// AppVersionsResponse is returned for `GET /api/apps/{appId}/versions`.
type AppVersionsResponse struct {
	AppID                   int64          `json:"appId"`
	BundleID                string         `json:"bundleId"`
	Platform                string         `json:"platform"`
	LatestExternalVersionID string         `json:"latestExternalVersionId"`
	Total                   int            `json:"total"`
	CoreFPReady             bool           `json:"coreFpReady"`
	Versions                []VersionEntry `json:"versions"`
}

// VersionManager retrieves app version histories and hydrates version metadata with caching.
type VersionManager struct {
	mu       sync.Mutex
	store    *config.Store
	engine   *ipatool.Client
	accounts *AccountManager
	hub      *EventHub
}

// NewVersionManager creates a VersionManager.
func NewVersionManager(store *config.Store, engine *ipatool.Client, accounts *AccountManager, hub *EventHub) *VersionManager {
	return &VersionManager{
		store:    store,
		engine:   engine,
		accounts: accounts,
		hub:      hub,
	}
}

func cacheKey(appID int64, platform, extVerID string) string {
	return fmt.Sprintf("%d:%s:%s", appID, strings.ToLower(strings.TrimSpace(platform)), strings.TrimSpace(extVerID))
}

// ListVersions queries ipatool for all external version IDs, orders newest-first, and attaches cached metadata.
func (vm *VersionManager) ListVersions(ctx context.Context, profileID string, appID int64, bundleID, platform string) (*AppVersionsResponse, error) {
	rec, stateDir, err := vm.accounts.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if platform == "" {
		platform = "iphone"
	}

	res, err := vm.engine.ListVersions(ctx, rec.ProfileID, stateDir, appID, bundleID, platform)
	if err != nil {
		return nil, err
	}

	ids := append([]string{}, res.ExternalVersionIdentifiers...)
	// ipatool returns oldest-to-newest with latestExternalVersionID at the end; reverse so newest is first.
	if len(ids) > 1 && ids[0] != res.LatestExternalVersionID {
		for l, r := 0, len(ids)-1; l < r; l, r = l+1, r-1 {
			ids[l], ids[r] = ids[r], ids[l]
		}
	}

	effectiveAppID := appID
	if effectiveAppID == 0 {
		effectiveAppID = res.AppID
	}
	effectiveBundle := bundleID
	if effectiveBundle == "" {
		effectiveBundle = res.BundleID
	}

	vm.mu.Lock()
	cache, _ := vm.store.LoadVersionCache()
	vm.mu.Unlock()

	entries := make([]VersionEntry, 0, len(ids))
	for _, id := range ids {
		entry := VersionEntry{
			ExternalVersionID: id,
			IsLatest:          id == res.LatestExternalVersionID,
		}
		if c, ok := cache[cacheKey(effectiveAppID, platform, id)]; ok {
			entry.DisplayVersion = c.DisplayVersion
			entry.ReleaseDate = c.ReleaseDate
			entry.ReleaseDateEstimated = c.ReleaseDateEstimated
			entry.SizeBytes = c.SizeBytes
			entry.MinimumOSVersion = c.MinimumOSVersion
			entry.Cached = true
		}
		entries = append(entries, entry)
	}

	binInfo := vm.engine.Info()
	return &AppVersionsResponse{
		AppID:                   effectiveAppID,
		BundleID:                effectiveBundle,
		Platform:                platform,
		LatestExternalVersionID: res.LatestExternalVersionID,
		Total:                   len(entries),
		CoreFPReady:             binInfo.CoreFPReady,
		Versions:                entries,
	}, nil
}

// HydrateMetadata resolves metadata for up to 30 requested version IDs using a bounded worker pool (concurrency 3),
// persists newly fetched entries into version_cache.json, and emits `version_metadata` SSE events.
func (vm *VersionManager) HydrateMetadata(ctx context.Context, profileID string, appID int64, bundleID, platform string, versionIDs []string, force bool) ([]VersionEntry, error) {
	rec, stateDir, err := vm.accounts.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if platform == "" {
		platform = "iphone"
	}

	vm.mu.Lock()
	cache, err := vm.store.LoadVersionCache()
	if err != nil {
		cache = map[string]config.VersionCacheEntry{}
	}
	vm.mu.Unlock()

	results := make([]VersionEntry, len(versionIDs))
	type jobItem struct {
		idx int
		id  string
	}
	var pending []jobItem
	for i, id := range versionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !force {
			if c, ok := cache[cacheKey(appID, platform, id)]; ok {
				results[i] = VersionEntry{
					ExternalVersionID:    id,
					DisplayVersion:       c.DisplayVersion,
					ReleaseDate:          c.ReleaseDate,
					ReleaseDateEstimated: c.ReleaseDateEstimated,
					SizeBytes:            c.SizeBytes,
					MinimumOSVersion:     c.MinimumOSVersion,
					Cached:               true,
				}
				continue
			}
		}
		pending = append(pending, jobItem{idx: i, id: id})
	}

	if len(pending) > 0 {
		jobs := make(chan jobItem, len(pending))
		for _, p := range pending {
			jobs <- p
		}
		close(jobs)

		const workers = 3
		var wg sync.WaitGroup
		var writeMu sync.Mutex

		workerCount := workers
		if len(pending) < workerCount {
			workerCount = len(pending)
		}
		for w := 0; w < workerCount; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for item := range jobs {
					if ctx.Err() != nil {
						return
					}
					meta, fetchErr := vm.engine.GetVersionMetadata(ctx, rec.ProfileID, stateDir, appID, bundleID, item.id, platform)
					entry := VersionEntry{ExternalVersionID: item.id}
					if fetchErr != nil {
						entry.Error = fetchErr.Error()
					} else {
						entry.DisplayVersion = meta.DisplayVersion
						entry.ReleaseDate = meta.ReleaseDate
						entry.ReleaseDateEstimated = meta.ReleaseDateEstimated
						entry.SizeBytes = meta.SizeBytes
						entry.MinimumOSVersion = meta.MinimumOSVersion
						entry.Cached = true

						writeMu.Lock()
						vm.mu.Lock()
						cache[cacheKey(appID, platform, item.id)] = config.VersionCacheEntry{
							AppID:                appID,
							Platform:             platform,
							ExternalVersionID:    item.id,
							DisplayVersion:       meta.DisplayVersion,
							ReleaseDate:          meta.ReleaseDate,
							ReleaseDateEstimated: meta.ReleaseDateEstimated,
							SizeBytes:            meta.SizeBytes,
							MinimumOSVersion:     meta.MinimumOSVersion,
							FetchedAt:            time.Now().UTC(),
						}
						_ = vm.store.SaveVersionCache(cache)
						vm.mu.Unlock()
						writeMu.Unlock()
					}
					results[item.idx] = entry
					if vm.hub != nil {
						vm.hub.Broadcast("version_metadata", map[string]any{
							"appId":    appID,
							"platform": platform,
							"version":  entry,
						})
					}
				}
			}()
		}
		wg.Wait()
	}

	return results, nil
}

// Purchase obtains a free App Store license for an app using the active or specified Apple ID.
func (vm *VersionManager) Purchase(ctx context.Context, profileID string, appID int64, bundleID, platform string) (*ipatool.PurchaseResult, error) {
	rec, stateDir, err := vm.accounts.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if platform == "" {
		platform = "iphone"
	}
	return vm.engine.Purchase(ctx, rec.ProfileID, stateDir, appID, bundleID, platform)
}
