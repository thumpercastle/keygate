package main

import (
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// brandAssetTypes are the only files BRAND_DIR serves: images, fonts
// and stylesheets for an operator's own logo and typeface. Anything
// else in the folder stays private.
var brandAssetTypes = map[string]string{
	".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".webp": "image/webp",
	".woff2": "font/woff2", ".woff": "font/woff", ".css": "text/css; charset=utf-8",
}

// brandFileHandler serves BRAND_DIR under /brand/. Same-origin assets
// load under a strict content security policy (img-src and font-src
// 'self'), where a logo or font on another host would be blocked.
func brandFileHandler(dir string) gin.HandlerFunc {
	root, err := filepath.Abs(dir)
	if err != nil {
		root = dir
	}
	return func(c *gin.Context) {
		rel := filepath.Clean("/" + c.Param("file"))
		ctype, known := brandAssetTypes[strings.ToLower(filepath.Ext(rel))]
		p := filepath.Join(root, rel)
		info, err := os.Stat(p)
		if !known || err != nil || info.IsDir() || !strings.HasPrefix(p, root+string(filepath.Separator)) {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Content-Type", ctype)
		c.Header("Cache-Control", "public, max-age=3600")
		c.Header("X-Content-Type-Options", "nosniff")
		// An SVG opened on its own is a document; it gets no script.
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src 'self'; img-src 'self' data:")
		c.File(p)
	}
}

// brandShell is what the page shell needs from the branding settings
// before the app has loaded: the name, the tab icon and the browser
// UI colour.
type brandShell struct{ SiteName, FaviconURL, Color string }

var (
	shellTitle    = regexp.MustCompile(`<title>[^<]*</title>`)
	shellNameMeta = regexp.MustCompile(`(<meta (?:property="og:(?:title|site_name)"|name="(?:twitter:title|apple-mobile-web-app-title|author)") content=")[^"]*(")`)
	shellIcon     = regexp.MustCompile(`(<link rel="icon"[^>]*href=")[^"]*(")`)
	shellColor    = regexp.MustCompile(`(<meta name="(?:theme-color|msapplication-TileColor)" content=")[^"]*(")`)
)

// brandIndexHTML puts the operator's name, icon and colour into the
// page shell, so tabs, bookmarks and link previews never show the
// upstream product. The generator meta (the AGPL attribution) is left
// as it is. An empty field leaves its part of the shell untouched.
func brandIndexHTML(page []byte, b brandShell) []byte {
	out := page
	replace := func(re *regexp.Regexp, value string) {
		v := strings.ReplaceAll(html.EscapeString(value), "$", "$$")
		out = re.ReplaceAll(out, []byte("${1}"+v+"${2}"))
	}
	if b.SiteName != "" {
		out = shellTitle.ReplaceAllLiteral(out, []byte("<title>"+html.EscapeString(b.SiteName)+"</title>"))
		replace(shellNameMeta, b.SiteName)
	}
	if b.FaviconURL != "" {
		replace(shellIcon, b.FaviconURL)
	}
	if b.Color != "" {
		replace(shellColor, b.Color)
	}
	return out
}

// cachedBrandShell reads the branding settings at most every ttl, so
// serving the page shell doesn't query the database on every load.
func cachedBrandShell(ttl time.Duration, read func() map[string]string) func() brandShell {
	var (
		mu   sync.Mutex
		at   time.Time
		last brandShell
	)
	return func() brandShell {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < ttl {
			return last
		}
		s := read()
		favicon := s["favicon_url"]
		if favicon == "" {
			favicon = s["logo_url"]
		}
		last, at = brandShell{SiteName: s["site_name"], FaviconURL: favicon, Color: s["brand_color"]}, time.Now()
		return last
	}
}
