package itunes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Storefront describes a supported App Store region.
type Storefront struct {
	Code   string `json:"code"`
	NameZH string `json:"nameZh"`
	NameEN string `json:"nameEn"`
}

// SupportedStorefronts lists common App Store regions.
var SupportedStorefronts = []Storefront{
	{Code: "cn", NameZH: "中国大陆 (CN)", NameEN: "China Mainland (CN)"},
	{Code: "us", NameZH: "美国 (US)", NameEN: "United States (US)"},
	{Code: "jp", NameZH: "日本 (JP)", NameEN: "Japan (JP)"},
	{Code: "hk", NameZH: "中国香港 (HK)", NameEN: "Hong Kong (HK)"},
	{Code: "tw", NameZH: "中国台湾 (TW)", NameEN: "Taiwan (TW)"},
	{Code: "gb", NameZH: "英国 (GB)", NameEN: "United Kingdom (GB)"},
	{Code: "kr", NameZH: "韩国 (KR)", NameEN: "South Korea (KR)"},
	{Code: "sg", NameZH: "新加坡 (SG)", NameEN: "Singapore (SG)"},
	{Code: "ca", NameZH: "加拿大 (CA)", NameEN: "Canada (CA)"},
	{Code: "au", NameZH: "澳大利亚 (AU)", NameEN: "Australia (AU)"},
	{Code: "de", NameZH: "德国 (DE)", NameEN: "Germany (DE)"},
	{Code: "fr", NameZH: "法国 (FR)", NameEN: "France (FR)"},
	{Code: "tr", NameZH: "土耳其 (TR)", NameEN: "Turkey (TR)"},
	{Code: "in", NameZH: "印度 (IN)", NameEN: "India (IN)"},
	{Code: "br", NameZH: "巴西 (BR)", NameEN: "Brazil (BR)"},
}

// AppItem is a normalized App Store application summary returned by search/lookup.
type AppItem struct {
	TrackID          int64   `json:"trackId"`
	BundleID         string  `json:"bundleId"`
	TrackName        string  `json:"trackName"`
	ArtistName       string  `json:"artistName"`
	Version          string  `json:"version"`
	Price            float64 `json:"price"`
	FormattedPrice   string  `json:"formattedPrice"`
	Currency         string  `json:"currency"`
	FileSizeBytes    int64   `json:"fileSizeBytes"`
	MinimumOSVersion string  `json:"minimumOsVersion"`
	ArtworkURL       string  `json:"artworkUrl"`
	Description      string  `json:"description,omitempty"`
	ReleaseDate      string  `json:"releaseDate,omitempty"`
	CurrentVerDate   string  `json:"currentVersionReleaseDate,omitempty"`
	PrimaryGenre     string  `json:"primaryGenreName,omitempty"`
	StoreViewURL     string  `json:"trackViewUrl,omitempty"`
}

// QueryKind identifies how user search input was interpreted.
type QueryKind string

const (
	QueryKeyword  QueryKind = "keyword"
	QueryAppID    QueryKind = "app_id"
	QueryBundleID QueryKind = "bundle_id"
)

// ParsedQuery holds the extracted search target and optional storefront override.
type ParsedQuery struct {
	Kind       QueryKind
	Value      string
	AppID      int64
	BundleID   string
	Country    string
	Original   string
}

var (
	reAppleURL  = regexp.MustCompile(`(?i)(?:apps|itunes)\.apple\.com/(?:([a-z]{2})/)?[^?#]*?\bid(\d{5,15})\b`)
	reDigits    = regexp.MustCompile(`^\d{5,15}$`)
	reBundleID  = regexp.MustCompile(`^[a-zA-Z0-9-]+(\.[a-zA-Z0-9_-]+)+$`)
)

// ParseQuery classifies user input into App Store URL, numeric App ID, Bundle ID, or keyword search.
func ParseQuery(raw string) ParsedQuery {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ParsedQuery{Kind: QueryKeyword, Original: raw}
	}
	if m := reAppleURL.FindStringSubmatch(trimmed); len(m) == 3 {
		id, _ := strconv.ParseInt(m[2], 10, 64)
		return ParsedQuery{
			Kind:     QueryAppID,
			Value:    m[2],
			AppID:    id,
			Country:  strings.ToLower(m[1]),
			Original: raw,
		}
	}
	if reDigits.MatchString(trimmed) {
		id, _ := strconv.ParseInt(trimmed, 10, 64)
		return ParsedQuery{
			Kind:     QueryAppID,
			Value:    trimmed,
			AppID:    id,
			Original: raw,
		}
	}
	if !strings.Contains(trimmed, " ") && !strings.Contains(trimmed, "/") && reBundleID.MatchString(trimmed) {
		return ParsedQuery{
			Kind:     QueryBundleID,
			Value:    trimmed,
			BundleID: trimmed,
			Original: raw,
		}
	}
	return ParsedQuery{
		Kind:     QueryKeyword,
		Value:    trimmed,
		Original: raw,
	}
}

// MapPlatformToEntity converts an ipatool platform name to an Apple iTunes entity parameter.
func MapPlatformToEntity(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ipad":
		return "iPadSoftware"
	case "macos":
		return "macSoftware"
	case "appletv":
		return "tvSoftware"
	default:
		return "software"
	}
}

// SearchResult wraps the resolved query and resulting apps.
type SearchResult struct {
	QueryKind string    `json:"queryKind"`
	Country   string    `json:"country"`
	Platform  string    `json:"platform"`
	Count     int       `json:"count"`
	Results   []AppItem `json:"results"`
}

// Client queries Apple's public iTunes Search and Lookup endpoints.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a new iTunes API client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 12 * time.Second},
		baseURL:    "https://itunes.apple.com",
	}
}

type itunesRawResponse struct {
	ResultCount int             `json:"resultCount"`
	Results     []itunesRawItem `json:"results"`
}

type itunesRawItem struct {
	TrackID                   int64   `json:"trackId"`
	BundleID                  string  `json:"bundleId"`
	TrackName                 string  `json:"trackName"`
	ArtistName                string  `json:"artistName"`
	Version                   string  `json:"version"`
	Price                     float64 `json:"price"`
	FormattedPrice            string  `json:"formattedPrice"`
	Currency                  string  `json:"currency"`
	FileSizeBytes             any     `json:"fileSizeBytes"`
	MinimumOSVersion          string  `json:"minimumOsVersion"`
	ArtworkURL512             string  `json:"artworkUrl512"`
	ArtworkURL100             string  `json:"artworkUrl100"`
	ArtworkURL60              string  `json:"artworkUrl60"`
	Description               string  `json:"description"`
	ReleaseDate               string  `json:"releaseDate"`
	CurrentVersionReleaseDate string  `json:"currentVersionReleaseDate"`
	PrimaryGenreName          string  `json:"primaryGenreName"`
	TrackViewURL              string  `json:"trackViewUrl"`
}

func (r itunesRawItem) toAppItem() AppItem {
	art := r.ArtworkURL100
	if art == "" {
		art = r.ArtworkURL512
	}
	if art == "" {
		art = r.ArtworkURL60
	}
	return AppItem{
		TrackID:          r.TrackID,
		BundleID:         r.BundleID,
		TrackName:        r.TrackName,
		ArtistName:       r.ArtistName,
		Version:          r.Version,
		Price:            r.Price,
		FormattedPrice:   r.FormattedPrice,
		Currency:         r.Currency,
		FileSizeBytes:    parseAnyInt64(r.FileSizeBytes),
		MinimumOSVersion: r.MinimumOSVersion,
		ArtworkURL:       art,
		Description:      r.Description,
		ReleaseDate:      r.ReleaseDate,
		CurrentVerDate:   r.CurrentVersionReleaseDate,
		PrimaryGenre:     r.PrimaryGenreName,
		StoreViewURL:     r.TrackViewURL,
	}
}

func parseAnyInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

// Search performs smart search (App Store URL, App ID, Bundle ID with keyword fallback, or keyword).
func (c *Client) Search(ctx context.Context, rawQuery, country, platform string, limit int) (*SearchResult, error) {
	parsed := ParseQuery(rawQuery)
	if parsed.Country != "" {
		country = parsed.Country
	}
	country = strings.ToLower(strings.TrimSpace(country))
	if country == "" {
		country = "cn"
	}
	if platform == "" {
		platform = "iphone"
	}
	if limit <= 0 {
		limit = 25
	} else if limit > 50 {
		limit = 50
	}

	var items []AppItem
	var err error
	kind := parsed.Kind

	switch parsed.Kind {
	case QueryAppID:
		items, err = c.lookupByParams(ctx, url.Values{
			"id":      {strconv.FormatInt(parsed.AppID, 10)},
			"country": {country},
		})
	case QueryBundleID:
		items, err = c.lookupByParams(ctx, url.Values{
			"bundleId": {parsed.BundleID},
			"country":  {country},
		})
		if err == nil && len(items) == 0 {
			kind = QueryKeyword
			items, err = c.searchKeyword(ctx, parsed.Value, country, platform, limit)
		}
	default:
		items, err = c.searchKeyword(ctx, parsed.Value, country, platform, limit)
	}
	if err != nil {
		return nil, err
	}

	return &SearchResult{
		QueryKind: string(kind),
		Country:   country,
		Platform:  platform,
		Count:     len(items),
		Results:   items,
	}, nil
}

// BatchLookup fetches AppItem metadata for multiple App IDs keyed by TrackID.
func (c *Client) BatchLookup(ctx context.Context, appIDs []int64, country string) (map[int64]AppItem, error) {
	out := make(map[int64]AppItem, len(appIDs))
	if len(appIDs) == 0 {
		return out, nil
	}
	country = strings.ToLower(strings.TrimSpace(country))
	if country == "" {
		country = "cn"
	}
	const chunkSize = 50
	for i := 0; i < len(appIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(appIDs) {
			end = len(appIDs)
		}
		parts := make([]string, 0, end-i)
		for _, id := range appIDs[i:end] {
			if id > 0 {
				parts = append(parts, strconv.FormatInt(id, 10))
			}
		}
		if len(parts) == 0 {
			continue
		}
		items, err := c.lookupByParams(ctx, url.Values{
			"id":      {strings.Join(parts, ",")},
			"country": {country},
		})
		if err != nil {
			return out, err
		}
		for _, it := range items {
			if it.TrackID > 0 {
				out[it.TrackID] = it
			}
		}
	}
	return out, nil
}

func (c *Client) lookupByParams(ctx context.Context, params url.Values) ([]AppItem, error) {
	u := fmt.Sprintf("%s/lookup?%s", strings.TrimRight(c.baseURL, "/"), params.Encode())
	return c.fetchItems(ctx, u)
}

func (c *Client) searchKeyword(ctx context.Context, term, country, platform string, limit int) ([]AppItem, error) {
	params := url.Values{
		"term":    {term},
		"country": {country},
		"entity":  {MapPlatformToEntity(platform)},
		"limit":   {strconv.Itoa(limit)},
	}
	u := fmt.Sprintf("%s/search?%s", strings.TrimRight(c.baseURL, "/"), params.Encode())
	return c.fetchItems(ctx, u)
}

func (c *Client) fetchItems(ctx context.Context, targetURL string) ([]AppItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "IPADownStudio/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iTunes request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("iTunes API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed itunesRawResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode iTunes response: %w", err)
	}
	items := make([]AppItem, 0, len(parsed.Results))
	for _, raw := range parsed.Results {
		if raw.TrackID == 0 && raw.BundleID == "" {
			continue
		}
		items = append(items, raw.toAppItem())
	}
	return items, nil
}
