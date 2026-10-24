package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/service"
)

func TestPreviewEmailTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AdminHandler{Email: service.NewEmailService("", "", "", "", "", slog.Default(), nil)}
	call := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/email-templates/preview", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.PreviewEmailTemplate(c)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call(`{"key":"otp_code","template":"{{define \"subject\"}}Code {{.Code}}{{end}}<p>{{.Code}}</p>"}`)
	data, _ := out["data"].(map[string]any)
	if code != http.StatusOK || data["subject"] != "Code 123456" || !strings.Contains(data["html"].(string), "<p>123456</p>") {
		t.Fatalf("preview = %d %v", code, out)
	}
	if code, _ := call(`{"key":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("unknown email: %d, want 400", code)
	}
	if code, _ := call(`{"key":"otp_code","template":"{{.Missing}}"}`); code != http.StatusBadRequest {
		t.Errorf("broken template: %d, want 400", code)
	}
}

// Send test goes to the signed-in admin unless an address is given, and
// says plainly when email is not set up rather than pretending to send.
func TestSendTestEmailTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AdminHandler{Email: service.NewEmailService("", "", "", "", "", slog.Default(), nil)}
	call := func(body, sessionEmail string) (int, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/email-templates/send-test", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		if sessionEmail != "" {
			c.Set("email", sessionEmail)
		}
		h.SendTestEmailTemplate(c)
		var out struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Error.Code
	}
	if code, _ := call(`{}`, "admin@example.com"); code != http.StatusBadRequest {
		t.Errorf("no key: %d, want 400", code)
	}
	if code, _ := call(`{"key":"otp_code"}`, ""); code != http.StatusBadRequest {
		t.Errorf("no recipient and no session email: %d, want 400", code)
	}
	if code, _ := call(`{"key":"otp_code","to":"not-an-email"}`, "admin@example.com"); code != http.StatusBadRequest {
		t.Errorf("bad address: %d, want 400", code)
	}
	if code, ec := call(`{"key":"otp_code"}`, "admin@example.com"); code != http.StatusServiceUnavailable || ec != "EMAIL_NOT_CONFIGURED" {
		t.Errorf("email not set up: %d %s, want 503 EMAIL_NOT_CONFIGURED", code, ec)
	}
}
