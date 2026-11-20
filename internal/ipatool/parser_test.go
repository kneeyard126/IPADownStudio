package ipatool

import (
	"errors"
	"testing"
)

func TestParseAuthOutput(t *testing.T) {
	raw := `{"level":"info","email":"demo@example.com","name":"Demo User","success":true,"time":"2026-01-01T00:00:00Z"}`
	res, err := ParseAuthOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Email != "demo@example.com" || res.Name != "Demo User" || !res.Success {
		t.Fatalf("unexpected parsed auth result: %+v", res)
	}
}

func TestParseAuthNeeds2FA(t *testing.T) {
	raw := `{"level":"error","error":"auth code is required","success":false}`
	_, err := ParseAuthOutput(raw)
	if !errors.Is(err, ErrNeeds2FA) {
		t.Fatalf("expected ErrNeeds2FA, got: %v", err)
	}
}

func TestParseAuthKeyringMissing(t *testing.T) {
	raw := `{"level":"error","error":"failed to get account: failed to get item: The specified item could not be found in the keyring","success":false}`
	_, err := ParseAuthOutput(raw)
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("expected ErrAuthExpired, got: %v", err)
	}
}

func TestParseListVersionsOutput(t *testing.T) {
	raw := `{"level":"info","app_id":414478124,"bundleID":"com.tencent.xin","externalVersionIdentifiers":["812000","823000","845000"],"latestExternalVersionID":"845000","success":true}`
	res, err := ParseListVersionsOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.BundleID != "com.tencent.xin" || res.LatestExternalVersionID != "845000" || len(res.ExternalVersionIdentifiers) != 3 {
		t.Fatalf("unexpected version list: %+v", res)
	}
}

func TestParseVersionMetadataOutput(t *testing.T) {
	raw := `{"level":"info","external_version_id":"845000","displayVersion":"8.0.50","releaseDate":"2024-06-15T00:00:00Z","releaseDateEstimated":false,"sizeBytes":345678901,"minimumOSVersion":"14.0","success":true}`
	res, err := ParseVersionMetadataOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DisplayVersion != "8.0.50" || res.SizeBytes != 345678901 || res.MinimumOSVersion != "14.0" {
		t.Fatalf("unexpected metadata: %+v", res)
	}
}

func TestParsePurchasesOutput(t *testing.T) {
	raw := `{"level":"info","count":1,"totalCount":42,"page":1,"max_results":20,"platform":"iphone","apps":[{"trackId":414478124,"bundleId":"com.tencent.xin","trackName":"WeChat","version":"8.0.50","price":0,"purchaseDate":"2023-05-01T12:00:00Z"}]}`
	res, err := ParsePurchasesOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TotalCount != 42 || len(res.Apps) != 1 || res.Apps[0].ID != 414478124 || res.Apps[0].Name != "WeChat" {
		t.Fatalf("unexpected purchases: %+v", res)
	}
}

func TestClassifyLicenseError(t *testing.T) {
	err := ClassifyErrorMessage("license is required to download this item", nil)
	if !errors.Is(err, ErrLicenseRequired) {
		t.Fatalf("expected ErrLicenseRequired, got: %v", err)
	}
}
