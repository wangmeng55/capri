package ochost

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardHTTPCachesOnlyHashedAssets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte(strings.Repeat("const app = true;\n", 128))
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	proxy := NewLocalProxy(upstream.URL, "", "")
	for _, test := range []struct {
		path  string
		cache string
	}{
		{path: "/assets/index-BSGOzYzn.js", cache: "private, max-age=31536000, immutable"},
		{path: "/api/session?limit=50", cache: ""},
		{path: "/assets/index.js", cache: ""},
	} {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			response := httptest.NewRecorder()
			proxy.ForwardHTTP(response, req)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cache)
			}
			if strings.Contains(test.path, "/assets/") && test.cache != "" &&
				!strings.Contains(response.Header().Get("Content-Encoding"), "gzip") {
				t.Fatalf("hashed asset was not gzip-compressed: headers %v", response.Header())
			}
		})
	}
}

func TestIsHashedFrontendAsset(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/assets/index-BSGOzYzn.js", want: true},
		{path: "/assets/sprite-B9ixRF_T.svg", want: true},
		{path: "/assets/index.js", want: false},
		{path: "/api/assets/index-BSGOzYzn.js", want: false},
		{path: "/assets/index-short.js", want: false},
	} {
		t.Run(strings.ReplaceAll(test.path, "/", "_"), func(t *testing.T) {
			if got := isHashedFrontendAsset(test.path); got != test.want {
				t.Fatalf("isHashedFrontendAsset(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}
