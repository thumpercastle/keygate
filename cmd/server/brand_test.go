package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// BRAND_DIR files are served same-origin under /brand/, so a strict
// content security policy (img-src/font-src 'self') still lets an
// operator's logo and fonts load. Only asset types are served, never a
// directory listing or anything outside the folder.
func TestBrandFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "logo.svg"), []byte("<svg/>"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("private"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "fonts"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "fonts", "a.woff2"), []byte("wOF2"), 0o644))
	must(os.WriteFile(filepath.Join(t.TempDir(), "secret.svg"), []byte("x"), 0o644))

	r := gin.New()
	r.GET("/brand/*file", brandFileHandler(dir))
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}
	if w := get("/brand/logo.svg"); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "svg") ||
		w.Header().Get("Cache-Control") == "" {
		t.Errorf("logo: %d %q cache=%q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
	}
	if w := get("/brand/fonts/a.woff2"); w.Code != http.StatusOK {
		t.Errorf("font in a subfolder: %d", w.Code)
	}
	for _, p := range []string{"/brand/notes.txt", "/brand/", "/brand/fonts", "/brand/../secret.svg", "/brand/%2e%2e/secret.svg", "/brand/missing.svg"} {
		if w := get(p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
}

// The page shell carries the operator's name and icon from the first
// byte, so tabs, bookmarks and link previews never show the upstream
// product. The attribution meta stays.
func TestBrandIndexHTML(t *testing.T) {
	html := []byte(`<link rel="icon" type="image/svg+xml" href="/favicon.svg" />
<link rel="icon" type="image/png" sizes="32x32" href="/favicon.svg" />
<title>Keygate — Open Source License Management</title>
<meta name="author" content="Keygate" />
<meta name="generator" content="Keygate (https://keygate.app)" />
<meta property="og:title" content="Keygate — Open Source License Management" />
<meta property="og:site_name" content="Keygate" />
<meta name="theme-color" content="#7c3aed" />
<meta name="apple-mobile-web-app-title" content="Keygate" />`)
	out := string(brandIndexHTML(html, brandShell{SiteName: `Acme <Licences>`, FaviconURL: "/brand/mark.svg", Color: "#4f46e5"}))
	for _, want := range []string{
		`<title>Acme &lt;Licences&gt;</title>`,
		`<meta property="og:title" content="Acme &lt;Licences&gt;" />`,
		`<meta property="og:site_name" content="Acme &lt;Licences&gt;" />`,
		`<meta name="apple-mobile-web-app-title" content="Acme &lt;Licences&gt;" />`,
		`href="/brand/mark.svg"`,
		`<meta name="theme-color" content="#4f46e5" />`,
		`<meta name="generator" content="Keygate (https://keygate.app)" />`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `href="/favicon.svg"`) {
		t.Error("upstream favicon still linked")
	}
	if got := string(brandIndexHTML(html, brandShell{})); got != string(html) {
		t.Error("an unbranded install must get the shell unchanged")
	}
}
