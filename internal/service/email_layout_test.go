package service

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/tabloy/keygate/internal/testsupport"
)

// Every outgoing mail is laid out the same way: the operator's logo on
// top, the message in a card with the brand colour along its edge, and
// the operator's footer and the attribution underneath. A message that
// arrives as a whole HTML document is unwrapped first, so the layout is
// never nested inside another <html>.
func TestEmailLayout_WrapsEveryMessage(t *testing.T) {
	s := openNotifyStore(t)
	testsupport.LockSettings(t, s.DB)
	ctx := context.Background()
	for k, v := range map[string]string{
		"site_name":         "Acme",
		"brand_color":       "#4f46e5",
		"email_logo_url":    "/brand/email-logo.png",
		"email_footer_text": "Acme Ltd · 1 High Street · support@acme.example",
	} {
		if err := s.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	e := NewEmailService("", "", "", "", "", slog.Default(), s)
	e.SetBaseURL("https://licences.acme.example")

	out := e.layout(`<!DOCTYPE html><html><body style="font-family: x"><h2>Hello</h2><p>Body text</p></body></html>`)
	for _, want := range []string{
		`src="https://licences.acme.example/brand/email-logo.png"`,
		`alt="Acme"`,
		`#4f46e5`,
		`<h2>Hello</h2><p>Body text</p>`,
		`Acme Ltd · 1 High Street · support@acme.example`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("layout is missing %s", want)
		}
	}
	if n := strings.Count(strings.ToLower(out), "<html"); n != 1 {
		t.Errorf("layout has %d <html> elements, want 1", n)
	}
	if !strings.Contains(withAttribution(out), emailFooter()) {
		t.Error("attribution footer missing after layout")
	}

	// No logo configured: the site name stands in, as text.
	_ = s.DeleteSetting(ctx, "email_logo_url")
	if out := e.layout("<p>x</p>"); strings.Contains(out, "<img") || !strings.Contains(out, ">Acme<") {
		t.Errorf("without a logo the header should be the site name as text:\n%s", out)
	}
}

// A template may set its own subject with {{define "subject"}}; without
// one the email keeps its built-in subject. Subjects are plain text.
func TestEmailSubject_FromTemplate(t *testing.T) {
	s := openNotifyStore(t)
	testsupport.LockSettings(t, s.DB)
	ctx := context.Background()
	e := NewEmailService("", "", "", "", "", slog.Default(), s)

	_ = s.DeleteSetting(ctx, "email_template_otp_code")
	subject, body := e.renderParts("otp_code", "Your login code", map[string]any{"Code": "123456"})
	if subject != "Your login code" || !strings.Contains(body, "123456") {
		t.Fatalf("default otp mail = %q / %q", subject, body)
	}

	custom := `{{define "subject"}}{{.Code}} is your {{.SiteName}} code & more{{end}}<p>Code: <b>{{.Code}}</b></p>`
	if err := ValidateTemplate(custom); err != nil {
		t.Fatalf("custom template refused: %v", err)
	}
	if err := s.SetSetting(ctx, "email_template_otp_code", custom); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "site_name", "Acme"); err != nil {
		t.Fatal(err)
	}
	subject, body = e.renderParts("otp_code", "Your login code", map[string]any{"Code": "654321"})
	if subject != "654321 is your Acme code & more" {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(body, "<b>654321</b>") || strings.Contains(body, "is your") {
		t.Errorf("body = %q", body)
	}
}

// Every email a customer or operator can receive is an editable
// template: it is listed for the dashboard, renders with the sample
// data, and its setting key is accepted.
func TestEveryEmailIsATemplate(t *testing.T) {
	want := []string{
		"license_created", "license_expiring", "updates_ending", "license_expired", "trial_expired",
		"license_suspended", "quota_warning", "seat_invite", "admin_invite", "payment_failed",
		"subscription_canceled", "payment_overdue", "payment_final_notice", "payment_recovered",
		"welcome", "otp_code", "plan_changed", "renewal_reminder", "payment_action_required",
		"trial_ending", "desktop_signin",
	}
	defaults := DefaultTemplates()
	for _, key := range want {
		src, ok := defaults[key]
		if !ok {
			t.Errorf("%s has no default template", key)
			continue
		}
		if err := ValidateTemplate(src); err != nil {
			t.Errorf("%s default template does not render with the sample data: %v", key, err)
		}
	}
	// Buttons can take the brand colour inside a style attribute.
	if err := ValidateTemplate(`<a href="{{.PortalURL}}" style="background:{{.BrandColor}}">Go</a>`); err != nil {
		t.Errorf("BrandColor in a style attribute: %v", err)
	}
}

// Values from customers never become markup: a product or person name
// is escaped in the mails that used to paste it in.
func TestEmailValuesAreEscaped(t *testing.T) {
	s := openNotifyStore(t)
	e := NewEmailService("", "", "", "", "", slog.Default(), s)
	_, body := e.renderParts("plan_changed", "x", map[string]any{
		"Product": "<script>x</script>", "OldPlan": "A", "NewPlan": "B",
	})
	if strings.Contains(body, "<script>") {
		t.Errorf("product name was not escaped: %s", body)
	}
}

// The dashboard previews a template exactly as it would be sent: subject,
// layout and attribution, filled with the sample data. A draft that has
// not been saved previews too; a broken one or an unknown email is an
// error, not a blank page.
func TestEmailPreview(t *testing.T) {
	s := openNotifyStore(t)
	testsupport.LockSettings(t, s.DB)
	ctx := context.Background()
	if err := s.SetSetting(ctx, "email_footer_text", "Footer line"); err != nil {
		t.Fatal(err)
	}
	e := NewEmailService("", "", "", "", "", slog.Default(), s)

	subject, page, err := e.Preview("otp_code", "")
	if err != nil || subject != "Your login code" || !strings.Contains(page, "123456") ||
		!strings.Contains(page, "Footer line") || !strings.Contains(page, emailFooter()) {
		t.Fatalf("default preview = %q, err %v:\n%s", subject, err, page)
	}
	subject, page, err = e.Preview("license_created", `{{define "subject"}}Key for {{.Product}}{{end}}<p>{{.LicenseKey}}</p>`)
	if err != nil || subject != "Key for Product" || !strings.Contains(page, "<p>KEY</p>") {
		t.Fatalf("draft preview = %q, err %v", subject, err)
	}
	if _, _, err := e.Preview("license_created", `{{.Nope}}`); err == nil {
		t.Error("a template with an unknown variable previewed without error")
	}
	if _, _, err := e.Preview("no_such_email", ""); err == nil {
		t.Error("an unknown email previewed without error")
	}
}

// A test send is the real thing: the email as a customer gets it, sent
// through the configured provider, with "[Test]" in front of the subject
// so nobody mistakes it. It is laid out once, not twice.
func TestSendTest(t *testing.T) {
	s := openNotifyStore(t)
	testsupport.LockSettings(t, s.DB)
	e := NewEmailService("", "", "", "", "", slog.Default(), s)
	var gotTo, gotSubject, gotBody string
	e.sendHook = func(to, subject, body string) error {
		gotTo, gotSubject, gotBody = to, subject, body
		return nil
	}
	if err := e.SendTest("otp_code", "", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if gotTo != "owner@example.com" || gotSubject != "[Test] Your login code" || !strings.Contains(gotBody, "123456") {
		t.Fatalf("sent %q %q", gotTo, gotSubject)
	}
	if n := strings.Count(strings.ToLower(gotBody), "<html"); n != 1 {
		t.Errorf("test email has %d <html> elements, want 1", n)
	}
	if err := e.SendTest("no_such_email", "", "owner@example.com"); err == nil {
		t.Error("unknown email sent without error")
	}
}
