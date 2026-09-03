package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandlerServesStaticFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>disk index</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("const disk = true;"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newMemoryWebStore()
	auth, err := NewAuthService(store, "https://kepler.example", strings.Repeat("s", 32), time.Hour, []string{"U1"}, fakeIdentityProvider{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(auth, nil, store, dir)
	if err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "https://kepler.example/", nil))
	if page.Code != http.StatusOK || page.Body.String() != "<html>disk index</html>" {
		t.Fatalf("page = %d %q", page.Code, page.Body.String())
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "https://kepler.example/assets/app.js", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "const disk = true") {
		t.Fatalf("asset = %d %q", asset.Code, asset.Body.String())
	}

	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>updated</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	page = httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "https://kepler.example/", nil))
	if page.Body.String() != "<html>updated</html>" {
		t.Fatalf("expected hot reload, got %q", page.Body.String())
	}
}

func TestHandlerServesMarkdownVendorAssets(t *testing.T) {
	store := newMemoryWebStore()
	auth, err := NewAuthService(store, "https://kepler.example", strings.Repeat("s", 32), time.Hour, []string{"U1"}, fakeIdentityProvider{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(auth, nil, store, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/assets/vendor/marked.min.js",
		"/assets/vendor/purify.min.js",
		"/assets/markdown.js",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://kepler.example"+path, nil))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("%s = %d len=%d", path, rec.Code, rec.Body.Len())
		}
	}
}
