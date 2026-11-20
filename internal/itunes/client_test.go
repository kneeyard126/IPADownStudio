package itunes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		in      string
		kind    QueryKind
		appID   int64
		bundle  string
		country string
	}{
		{"WeChat", QueryKeyword, 0, "", ""},
		{"414478124", QueryAppID, 414478124, "", ""},
		{"com.tencent.xin", QueryBundleID, 0, "com.tencent.xin", ""},
		{"https://apps.apple.com/us/app/wechat/id414478124", QueryAppID, 414478124, "", "us"},
		{"https://apps.apple.com/app/id835599320", QueryAppID, 835599320, "", ""},
		{"https://itunes.apple.com/jp/app/line/id443904275?mt=8", QueryAppID, 443904275, "", "jp"},
	}
	for _, tc := range tests {
		got := ParseQuery(tc.in)
		if got.Kind != tc.kind || got.AppID != tc.appID || got.BundleID != tc.bundle || got.Country != tc.country {
			t.Fatalf("ParseQuery(%q) = %+v, want kind=%s appID=%d bundle=%q country=%q",
				tc.in, got, tc.kind, tc.appID, tc.bundle, tc.country)
		}
	}
}

func TestMapPlatformToEntity(t *testing.T) {
	if got := MapPlatformToEntity("iphone"); got != "software" {
		t.Fatalf("expected software, got %s", got)
	}
	if got := MapPlatformToEntity("ipad"); got != "iPadSoftware" {
		t.Fatalf("expected iPadSoftware, got %s", got)
	}
	if got := MapPlatformToEntity("macos"); got != "macSoftware" {
		t.Fatalf("expected macSoftware, got %s", got)
	}
	if got := MapPlatformToEntity("appletv"); got != "tvSoftware" {
		t.Fatalf("expected tvSoftware, got %s", got)
	}
}

func TestSearchBundleFallbackAndBatchLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/lookup":
			if r.URL.Query().Get("bundleId") == "org.example.missing" {
				_, _ = w.Write([]byte(`{"resultCount":0,"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"resultCount":1,"results":[{"trackId":414478124,"bundleId":"com.tencent.xin","trackName":"WeChat","fileSizeBytes":"123456","minimumOsVersion":"14.0","artworkUrl100":"https://example.com/a.png"}]}`))
		case "/search":
			_, _ = w.Write([]byte(`{"resultCount":1,"results":[{"trackId":999,"bundleId":"org.example.found","trackName":"FoundByTerm","fileSizeBytes":"500"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	res, err := c.Search(context.Background(), "org.example.missing", "us", "iphone", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.QueryKind != string(QueryKeyword) || res.Count != 1 || res.Results[0].TrackID != 999 {
		t.Fatalf("unexpected fallback search result: %+v", res)
	}

	batch, err := c.BatchLookup(context.Background(), []int64{414478124}, "cn")
	if err != nil {
		t.Fatalf("batch lookup error: %v", err)
	}
	item, ok := batch[414478124]
	if !ok || item.FileSizeBytes != 123456 || item.BundleID != "com.tencent.xin" {
		t.Fatalf("unexpected batch item: %+v", item)
	}
}
