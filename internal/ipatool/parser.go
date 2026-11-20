package ipatool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrBinaryNotFound     = errors.New("ipatool binary not found")
	ErrNeeds2FA           = errors.New("two-factor authentication code required")
	ErrInvalidCredentials = errors.New("invalid Apple ID or password")
	ErrLicenseRequired    = errors.New("app license required")
	ErrAuthExpired        = errors.New("Apple ID session expired or not logged in")
)

// ErrorKind categorizes failures returned by ipatool so the UI can offer targeted recovery.
type ErrorKind string

const (
	KindNone               ErrorKind = ""
	KindBinaryNotFound     ErrorKind = "binary_not_found"
	KindNeeds2FA           ErrorKind = "needs_2fa"
	KindInvalidCredentials ErrorKind = "invalid_credentials"
	KindLicenseRequired    ErrorKind = "license_required"
	KindAuthExpired        ErrorKind = "auth_expired"
	KindCommandFailed      ErrorKind = "command_failed"
)

// EngineError wraps an ipatool error with a classified kind and original message.
type EngineError struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *EngineError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}

func (e *EngineError) Unwrap() error {
	return e.Cause
}

// AuthResult holds parsed output of `auth login` or `auth info`.
type AuthResult struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Success bool   `json:"success"`
}

// VersionListResult holds parsed output of `list-versions`.
type VersionListResult struct {
	AppID                      int64    `json:"appId"`
	BundleID                   string   `json:"bundleId"`
	ExternalVersionIdentifiers []string `json:"externalVersionIdentifiers"`
	LatestExternalVersionID    string   `json:"latestExternalVersionId"`
	Success                    bool     `json:"success"`
}

// VersionMetadataResult holds parsed output of `get-version-metadata`.
type VersionMetadataResult struct {
	ExternalVersionID    string `json:"externalVersionId"`
	DisplayVersion       string `json:"displayVersion"`
	ReleaseDate          string `json:"releaseDate,omitempty"`
	ReleaseDateEstimated bool   `json:"releaseDateEstimated,omitempty"`
	SizeBytes            int64  `json:"sizeBytes,omitempty"`
	MinimumOSVersion     string `json:"minimumOsVersion,omitempty"`
	Success              bool   `json:"success"`
}

// PurchasedApp represents one item in `list-purchases` output.
type PurchasedApp struct {
	ID           int64    `json:"id"`
	BundleID     string   `json:"bundleId"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Price        float64  `json:"price"`
	PurchaseDate string   `json:"purchaseDate,omitempty"`
	Platforms    []string `json:"platforms,omitempty"`
}

// PurchasesResult holds parsed output of `list-purchases`.
type PurchasesResult struct {
	Count      int            `json:"count"`
	TotalCount int            `json:"totalCount"`
	Page       int            `json:"page"`
	MaxResults int            `json:"maxResults"`
	Platform   string         `json:"platform,omitempty"`
	Apps       []PurchasedApp `json:"apps"`
}

// PurchaseResult holds parsed output of `purchase`.
type PurchaseResult struct {
	AlreadyOwned bool `json:"alreadyOwned"`
	Success      bool `json:"success"`
}

// ParseNDJSONLines splits combined or individual command output into JSON objects and plain text lines.
func ParseNDJSONLines(raw string) ([]map[string]any, []string) {
	var objs []map[string]any
	var plain []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				objs = append(objs, m)
				continue
			}
		}
		plain = append(plain, line)
	}
	return objs, plain
}

// ExtractError inspects parsed JSON records and plain text lines for errors and classifies them.
func ExtractError(objs []map[string]any, plain []string, cmdErr error) error {
	var msg string
	for i := len(objs) - 1; i >= 0; i-- {
		o := objs[i]
		level := strings.ToLower(strVal(o, "level"))
		errStr := firstNonEmpty(strVal(o, "error"), strVal(o, "err"), strVal(o, "errorMessage"))
		if errStr != "" {
			msg = errStr
			break
		}
		if level == "error" || level == "fatal" {
			if m := strVal(o, "message"); m != "" {
				msg = m
				break
			}
		}
	}
	if msg == "" && len(plain) > 0 {
		msg = strings.Join(plain, " | ")
	}
	if msg == "" && cmdErr != nil {
		msg = cmdErr.Error()
	}
	if msg == "" {
		return nil
	}
	return ClassifyErrorMessage(msg, cmdErr)
}

// ClassifyErrorMessage maps an ipatool error text to a typed EngineError.
func ClassifyErrorMessage(msg string, cause error) error {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "auth code is required") ||
		strings.Contains(lower, "enter 2fa code") ||
		strings.Contains(lower, "two-factor") ||
		strings.Contains(lower, "2fa") ||
		strings.Contains(lower, "verification code"):
		return &EngineError{Kind: KindNeeds2FA, Message: msg, Cause: ErrNeeds2FA}

	case strings.Contains(lower, "invalid email") ||
		strings.Contains(lower, "invalid password") ||
		strings.Contains(lower, "bad credentials") ||
		strings.Contains(lower, "your apple id or password was incorrect") ||
		strings.Contains(lower, "incorrect password"):
		return &EngineError{Kind: KindInvalidCredentials, Message: msg, Cause: ErrInvalidCredentials}

	case strings.Contains(lower, "license is required") ||
		strings.Contains(lower, "password token is required") && strings.Contains(lower, "purchase") ||
		strings.Contains(lower, "not owned") ||
		strings.Contains(lower, "must be purchased") ||
		strings.Contains(lower, "9610"):
		return &EngineError{Kind: KindLicenseRequired, Message: msg, Cause: ErrLicenseRequired}

	case strings.Contains(lower, "could not be found in the keyring") ||
		strings.Contains(lower, "password token") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "no account") ||
		strings.Contains(lower, "failed to get account"):
		return &EngineError{Kind: KindAuthExpired, Message: msg, Cause: ErrAuthExpired}

	default:
		if cause != nil {
			return &EngineError{Kind: KindCommandFailed, Message: msg, Cause: cause}
		}
		return &EngineError{Kind: KindCommandFailed, Message: msg}
	}
}

// ParseAuthOutput parses `auth login` or `auth info` NDJSON output.
func ParseAuthOutput(raw string) (*AuthResult, error) {
	objs, plain := ParseNDJSONLines(raw)
	for i := len(objs) - 1; i >= 0; i-- {
		o := objs[i]
		email := firstNonEmpty(strVal(o, "email"), strVal(o, "appleId"))
		name := firstNonEmpty(strVal(o, "name"), strVal(o, "accountName"), strVal(o, "account_name"))
		succ, hasSucc := boolVal(o, "success")
		if (hasSucc && succ) || email != "" {
			if hasSucc && !succ {
				continue
			}
			return &AuthResult{
				Email:   email,
				Name:    name,
				Success: true,
			}, nil
		}
	}
	if err := ExtractError(objs, plain, nil); err != nil {
		return nil, err
	}
	return nil, &EngineError{Kind: KindCommandFailed, Message: "unexpected empty auth output"}
}

// ParseListVersionsOutput parses `list-versions` NDJSON output.
func ParseListVersionsOutput(raw string) (*VersionListResult, error) {
	objs, plain := ParseNDJSONLines(raw)
	for i := len(objs) - 1; i >= 0; i-- {
		o := objs[i]
		ids := strSliceVal(o, "externalVersionIdentifiers", "external_version_identifiers")
		latest := firstNonEmpty(strVal(o, "latestExternalVersionID"), strVal(o, "latestExternalVersionId"), strVal(o, "latest_external_version_id"))
		bundleID := firstNonEmpty(strVal(o, "bundleID"), strVal(o, "bundleId"), strVal(o, "bundle_identifier"))
		appID := int64Val(o, "appId", "app_id", "trackId")
		succ, hasSucc := boolVal(o, "success")
		if len(ids) > 0 || latest != "" || (hasSucc && succ) {
			return &VersionListResult{
				AppID:                      appID,
				BundleID:                   bundleID,
				ExternalVersionIdentifiers: ids,
				LatestExternalVersionID:    latest,
				Success:                    true,
			}, nil
		}
	}
	if err := ExtractError(objs, plain, nil); err != nil {
		return nil, err
	}
	return nil, &EngineError{Kind: KindCommandFailed, Message: "no version identifiers returned"}
}

// ParseVersionMetadataOutput parses `get-version-metadata` NDJSON output.
func ParseVersionMetadataOutput(raw string) (*VersionMetadataResult, error) {
	objs, plain := ParseNDJSONLines(raw)
	for i := len(objs) - 1; i >= 0; i-- {
		o := objs[i]
		dv := firstNonEmpty(strVal(o, "displayVersion"), strVal(o, "display_version"), strVal(o, "version"))
		rd := firstNonEmpty(strVal(o, "releaseDate"), strVal(o, "release_date"))
		rde, _ := boolVal(o, "releaseDateEstimated", "release_date_estimated")
		sz := int64Val(o, "sizeBytes", "size_bytes")
		minOS := firstNonEmpty(strVal(o, "minimumOSVersion"), strVal(o, "minimumOsVersion"), strVal(o, "minimum_os_version"), strVal(o, "minOSVersion"))
		extID := firstNonEmpty(strVal(o, "externalVersionID"), strVal(o, "external_version_id"))
		succ, hasSucc := boolVal(o, "success")
		if dv != "" || sz > 0 || rd != "" || (hasSucc && succ) {
			return &VersionMetadataResult{
				ExternalVersionID:    extID,
				DisplayVersion:       dv,
				ReleaseDate:          rd,
				ReleaseDateEstimated: rde,
				SizeBytes:            sz,
				MinimumOSVersion:     minOS,
				Success:              true,
			}, nil
		}
	}
	if err := ExtractError(objs, plain, nil); err != nil {
		return nil, err
	}
	return nil, &EngineError{Kind: KindCommandFailed, Message: "version metadata unavailable"}
}

// ParsePurchasesOutput parses `list-purchases` NDJSON output.
func ParsePurchasesOutput(raw string) (*PurchasesResult, error) {
	objs, plain := ParseNDJSONLines(raw)
	for i := len(objs) - 1; i >= 0; i-- {
		o := objs[i]
		if _, ok := o["apps"]; ok || o["totalCount"] != nil || o["count"] != nil {
			res := &PurchasesResult{
				Count:      int(int64Val(o, "count")),
				TotalCount: int(int64Val(o, "totalCount", "total_count")),
				Page:       int(int64Val(o, "page")),
				MaxResults: int(int64Val(o, "max_results", "maxResults")),
				Platform:   strVal(o, "platform"),
				Apps:       []PurchasedApp{},
			}
			if rawApps, ok := o["apps"].([]any); ok {
				for _, item := range rawApps {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					app := PurchasedApp{
						ID:           int64Val(m, "trackId", "id", "appId"),
						BundleID:     firstNonEmpty(strVal(m, "bundleId"), strVal(m, "bundleID")),
						Name:         firstNonEmpty(strVal(m, "trackName"), strVal(m, "name")),
						Version:      strVal(m, "version"),
						Price:        floatVal(m, "price"),
						PurchaseDate: firstNonEmpty(strVal(m, "purchaseDate"), strVal(m, "purchase_date")),
						Platforms:    strSliceVal(m, "platforms"),
					}
					if app.ID != 0 || app.BundleID != "" || app.Name != "" {
						res.Apps = append(res.Apps, app)
					}
				}
			}
			if res.Count == 0 {
				res.Count = len(res.Apps)
			}
			return res, nil
		}
	}
	if err := ExtractError(objs, plain, nil); err != nil {
		return nil, err
	}
	return nil, &EngineError{Kind: KindCommandFailed, Message: "unexpected list-purchases output"}
}

// ParsePurchaseOutput parses `purchase` NDJSON output.
func ParsePurchaseOutput(raw string) (*PurchaseResult, error) {
	objs, plain := ParseNDJSONLines(raw)
	for i := len(objs) - 1; i >= 0; i-- {
		o := objs[i]
		succ, hasSucc := boolVal(o, "success")
		owned, hasOwned := boolVal(o, "alreadyOwned", "already_owned")
		if (hasSucc && succ) || hasOwned {
			if hasSucc && !succ {
				continue
			}
			return &PurchaseResult{
				AlreadyOwned: owned,
				Success:      true,
			}, nil
		}
	}
	if err := ExtractError(objs, plain, nil); err != nil {
		return nil, err
	}
	return nil, &EngineError{Kind: KindCommandFailed, Message: "purchase command produced no confirmation"}
}

func strVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func int64Val(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int64(t)
		case int64:
			return t
		case int:
			return int64(t)
		case json.Number:
			if n, err := t.Int64(); err == nil {
				return n
			}
		case string:
			var n int64
			if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

func floatVal(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

func boolVal(m map[string]any, keys ...string) (bool, bool) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
}

func strSliceVal(m map[string]any, keys ...string) []string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			switch t := item.(type) {
			case string:
				if t != "" {
					out = append(out, t)
				}
			case float64:
				out = append(out, fmt.Sprintf("%.0f", t))
			}
		}
		return out
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
