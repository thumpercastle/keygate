package handler

import (
	"strings"
	"testing"

	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
)

// The theming settings reach every visitor's page, so each is checked
// on the way in: URLs are http(s) or a path on this site, the font
// family is a plain list of names, and theme tokens are CSS custom
// properties with inert values — no selectors, no url(), no markup.
func TestBrandingProblem_ThemeSettings(t *testing.T) {
	for _, tt := range []struct {
		key, val string
		ok       bool
	}{
		{"favicon_url", "/brand/mark.svg", true},
		{"favicon_url", "https://cdn.example.com/mark.svg", true},
		{"favicon_url", "javascript:alert(1)", false},
		{"favicon_url", "//evil.example/x.svg", false},
		{"font_stylesheet_url", "/brand/fonts.css", true},
		{"font_stylesheet_url", "data:text/css,body{}", false},
		{"font_family", `"Brand Sans", "Segoe UI", sans-serif`, true},
		{"font_family", `x; } body { display:none`, false},
		{"theme_tokens", "--color-primary: #4f46e5;\n--radius-lg: 0.5rem;\n\n--color-ring: rgb(79 70 229 / 0.5)", true},
		{"theme_tokens", "--color-primary: #4f46e5; } body { display:none", false},
		{"theme_tokens", "color: red;", false},
		{"theme_tokens", "--bg: url(https://evil.example/x.png);", false},
		{"theme_tokens", "--x: </style><script>", false},
		{"theme_tokens", strings.Repeat("--a: 1;\n", 201), false},
	} {
		got := brandingProblem(tt.key, tt.val)
		if (got == "") != tt.ok {
			t.Errorf("brandingProblem(%q, %q) = %q, want ok=%v", tt.key, tt.val, got, tt.ok)
		}
	}
}

func TestThemeSettingsAreWritableAndPublic(t *testing.T) {
	for _, key := range []string{"favicon_url", "font_stylesheet_url", "font_family", "theme_tokens"} {
		if !settingsWritable[key] {
			t.Errorf("%q is not writable", key)
		}
		if !store.IsPublicSetting(key) {
			t.Errorf("%q is not served by /config", key)
		}
	}
}

// The email look and every email template can be saved from the
// dashboard; the logo must be a link an email client can load.
func TestEmailSettingsAreWritable(t *testing.T) {
	for _, key := range []string{"email_logo_url", "email_footer_text"} {
		if !settingsWritable[key] {
			t.Errorf("%q is not writable", key)
		}
	}
	for key := range service.DefaultTemplates() {
		if !settingsWritable["email_template_"+key] {
			t.Errorf("email_template_%s is not writable", key)
		}
	}
	for _, tt := range []struct {
		key, val string
		ok       bool
	}{
		{"email_logo_url", "/brand/email-logo.png", true},
		{"email_logo_url", "https://cdn.example.com/logo.png", true},
		{"email_logo_url", "javascript:alert(1)", false},
		{"email_footer_text", "Acme Ltd\n1 High Street", true},
		{"email_footer_text", strings.Repeat("x", 1001), false},
	} {
		if got := brandingProblem(tt.key, tt.val); (got == "") != tt.ok {
			t.Errorf("brandingProblem(%q, %.20q) = %q, want ok=%v", tt.key, tt.val, got, tt.ok)
		}
	}
}
