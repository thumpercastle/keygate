package service

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/tabloy/keygate/pkg/apperr"
)

// Settings that shape every outgoing mail. The logo is an image an email
// client can show (PNG or JPEG; most clients refuse SVG); a path on this
// site is made absolute with the public address. The footer is plain
// text, escaped.
const (
	settingEmailLogoURL    = "email_logo_url"
	settingEmailFooterText = "email_footer_text"
)

// layout puts a message into the house style: logo (or the site name)
// above, the message in a white card edged with the brand colour, and
// the operator's footer below. A message that is a whole document is
// unwrapped to its <body> first, so the result is one document. The
// attribution footer is added later, by withAttribution.
func (s *EmailService) layout(body string) string {
	setting := func(key string) string {
		if s.store == nil {
			return ""
		}
		v, _ := s.store.GetSetting(context.Background(), key)
		return strings.TrimSpace(v)
	}
	siteName := setting("site_name")
	accent := s.buttonColour("#111827")
	fontFamily := setting("font_family")
	if fontFamily == "" || strings.ContainsAny(fontFamily, `<>;{}`) {
		fontFamily = ""
	}
	fontStack := `-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif`
	if fontFamily != "" {
		fontStack = strings.ReplaceAll(fontFamily, `"`, `'`) + ", " + fontStack
	}

	header := ""
	if logo := s.absoluteURL(setting(settingEmailLogoURL)); logo != "" {
		header = `<img src="` + html.EscapeString(logo) + `" alt="` + html.EscapeString(siteName) +
			`" height="32" style="display:block;height:32px;width:auto;border:0;outline:none;">`
	} else if siteName != "" {
		header = `<span style="font-size:20px;font-weight:600;color:#18181b;">` + html.EscapeString(siteName) + `</span>`
	}
	footer := ""
	if text := setting(settingEmailFooterText); text != "" {
		footer = `<tr><td style="padding:20px 8px 0;font-size:12px;line-height:1.5;color:#71717a;">` +
			strings.ReplaceAll(html.EscapeString(text), "\n", "<br>") + `</td></tr>`
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>`)
	b.WriteString(`<body style="margin:0;padding:0;background:#f4f4f5;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f4f4f5;"><tr><td align="center" style="padding:32px 16px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;font-family:` + html.EscapeString(fontStack) + `;">`)
	if header != "" {
		b.WriteString(`<tr><td style="padding:0 8px 20px;">` + header + `</td></tr>`)
	}
	b.WriteString(`<tr><td style="background:#ffffff;border-radius:12px;border-top:4px solid ` + accent +
		`;padding:32px;font-size:15px;line-height:1.6;color:#18181b;">`)
	b.WriteString(emailInner(body))
	b.WriteString(`</td></tr>`)
	b.WriteString(footer)
	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

// emailInner is what sits inside a document's <body>, or the whole
// string when it is not a document.
func emailInner(body string) string {
	lower := strings.ToLower(body)
	start := strings.Index(lower, "<body")
	if start < 0 {
		return strings.TrimSpace(body)
	}
	open := strings.Index(lower[start:], ">")
	end := lastIndexASCIIFold(body, "</body>")
	if open < 0 || end < 0 || end < start+open+1 {
		return strings.TrimSpace(body)
	}
	return strings.TrimSpace(body[start+open+1 : end])
}

// absoluteURL turns a site path into a link that works from a mailbox.
// An http(s) URL is kept; anything else is dropped.
func (s *EmailService) absoluteURL(u string) string {
	switch {
	case u == "":
		return ""
	case strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//"):
		if s.baseURL == "" {
			return ""
		}
		return s.baseURL + u
	case apperr.ValidateHTTPURL("url", u) == nil:
		return u
	}
	return ""
}

// renderParts renders one email from its template: the operator's
// version if they saved one, otherwise the built-in default. A template
// may set its own subject with {{define "subject"}}…{{end}}; without one
// the email keeps defaultSubject. Data gets the shared values
// (SiteName, PortalURL, BrandColor) added.
func (s *EmailService) renderParts(key, defaultSubject string, data map[string]any) (subject, body string) {
	data = s.templateData(data)
	def := DefaultTemplates()[key]
	src := s.getTemplate(key, def)
	if src != def {
		if subj, out, err := executeParts(src, data); err == nil {
			return subjectOr(subj, defaultSubject), out
		} else {
			s.logger.Error("custom email template failed; sending the default", "template", key, "error", err)
		}
	}
	subj, out, err := executeParts(def, data)
	if err != nil {
		s.logger.Error("default email template failed", "template", key, "error", err)
		return defaultSubject, def
	}
	return subjectOr(subj, defaultSubject), out
}

func executeParts(src string, data any) (subject, body string, err error) {
	t, err := template.New("email").Parse(src)
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", "", err
	}
	if st := t.Lookup("subject"); st != nil {
		var sb bytes.Buffer
		if err := st.Execute(&sb, data); err != nil {
			return "", "", err
		}
		subject = sb.String()
	}
	return subject, buf.String(), nil
}

// subjectOr is the template's subject as plain text on one line, or the
// fallback when the template set none.
func subjectOr(subject, fallback string) string {
	subject = strings.Join(strings.Fields(html.UnescapeString(subject)), " ")
	if subject == "" {
		return fallback
	}
	return subject
}

// Built-in templates for the emails that used to be fixed markup. Like
// the others they hold the message only; layout() adds the frame.
const (
	tmplSubscriptionCanceled = `{{if .Immediate}}<h2>Your {{.Product}} subscription has ended</h2>
<p>Your subscription was cancelled with immediate effect, and access has ended.</p>{{else}}<h2>Your {{.Product}} subscription is cancelled</h2>
<p>It stays active until the end of the current billing period, and won't renew after that.</p>{{end}}
<p>You can subscribe again at any time from <a href="{{.PortalURL}}">your account</a>.</p>`

	tmplPaymentOverdue = `<h2>Your {{.Product}} payment is still outstanding</h2>
<p>We haven't been able to take payment for a week. Please update your payment method to keep your access.</p>
<p><a href="{{.PortalURL}}" style="display:inline-block;background:{{.BrandColor}};color:#ffffff;padding:12px 20px;border-radius:8px;text-decoration:none;">Update payment method</a></p>`

	tmplPaymentFinalNotice = `<h2>Final notice: {{.Product}} access will be suspended</h2>
<p>Your payment has been overdue for 14 days. Access will be suspended soon unless it is paid.</p>
<p><a href="{{.PortalURL}}" style="display:inline-block;background:{{.BrandColor}};color:#ffffff;padding:12px 20px;border-radius:8px;text-decoration:none;">Update payment method</a></p>`

	tmplPaymentRecovered = `<h2>Payment received</h2>
<p>Your payment for {{.Product}} went through, and your subscription is active again. There's nothing else to do.</p>`

	tmplWelcome = `<h2>Welcome{{if .Name}}, {{.Name}}{{end}}</h2>
<p>Your account is ready. You can manage your licences and subscriptions from <a href="{{.PortalURL}}">your account</a>.</p>`

	tmplOTPCode = `<h2>Your sign-in code</h2>
<p>Enter this code to sign in:</p>
<p style="font-family:Consolas,Menlo,monospace;font-size:32px;font-weight:700;letter-spacing:8px;text-align:center;background:#f4f4f5;border-radius:8px;padding:16px;margin:16px 0;">{{.Code}}</p>
<p style="color:#71717a;font-size:14px;">It expires in 10 minutes. If you didn't ask for it, you can ignore this email.</p>`

	tmplPlanChanged = `<h2>Your {{.Product}} plan has changed</h2>
<p>You've moved from <strong>{{.OldPlan}}</strong> to <strong>{{.NewPlan}}</strong>.</p>`

	tmplRenewalReminder = `<h2>Your {{.Product}} subscription renews soon</h2>
<p>It renews on <strong>{{.RenewalDate}}</strong>. There's nothing to do if you'd like to continue.</p>`

	tmplPaymentActionRequired = `<h2>Please confirm your {{.Product}} payment</h2>
<p>Your bank needs you to confirm this payment before it can go through.</p>
<p><a href="{{.InvoiceURL}}" style="display:inline-block;background:{{.BrandColor}};color:#ffffff;padding:12px 20px;border-radius:8px;text-decoration:none;">Confirm payment</a></p>
<p style="color:#71717a;font-size:14px;">If it isn't confirmed, your subscription may be interrupted.</p>`

	tmplTrialEnding = `<h2>Your {{.Product}} trial ends soon</h2>
<p>It ends on <strong>{{.TrialEnd}}</strong>, and your subscription starts automatically after that.</p>
<p style="color:#71717a;font-size:14px;">To cancel before then, use <a href="{{.PortalURL}}">your account</a>.</p>`

	tmplDesktopSignin = `<h2>Sign in to {{.Product}}</h2>
<p>Someone asked to sign in to {{.Product}} on <strong>{{.Device}}</strong> with this email address.</p>
<p><a href="{{.Link}}" style="display:inline-block;background:{{.BrandColor}};color:#ffffff;padding:12px 20px;border-radius:8px;text-decoration:none;">Open the sign-in page</a></p>
<p style="color:#71717a;font-size:14px;">The link expires in 15 minutes. Signing in on a new computer signs you out of the previous one. If you didn't ask for this, you can ignore this email.</p>`
)

// previewSubjects are the built-in subjects, as the sample data makes
// them, for emails whose template sets none.
var previewSubjects = map[string]string{
	"license_created": "Your license for Product", "license_expiring": "Product trial ending soon",
	"updates_ending": "Product updates ending soon", "license_expired": "Product license expired",
	"trial_expired": "Product trial has ended", "license_suspended": "Product license suspended",
	"quota_warning": "Product: api_calls quota at 80%", "seat_invite": "You've been invited to Product",
	"admin_invite": "You've been added to Site", "payment_failed": "Product payment failed",
	"subscription_canceled": "Product subscription canceled", "payment_overdue": "Product — payment still outstanding",
	"payment_final_notice": "Product — final payment notice", "payment_recovered": "Product — payment recovered",
	"welcome": "Welcome", "otp_code": "Your login code", "plan_changed": "Product plan changed",
	"renewal_reminder": "Product renewal coming up", "payment_action_required": "Product — payment authentication required",
	"trial_ending": "Product trial ending soon", "desktop_signin": "Sign in to Product",
}

// Preview renders one email as it would be sent, with the sample data:
// src when given (a draft, validated first), otherwise the saved or
// built-in template. It returns the subject and the full page, layout
// and attribution included.
func (s *EmailService) Preview(key, src string) (subject, page string, err error) {
	def, ok := DefaultTemplates()[key]
	if !ok {
		return "", "", fmt.Errorf("unknown email %q", key)
	}
	if src == "" {
		src = s.getTemplate(key, def)
	}
	if err := ValidateTemplate(src); err != nil {
		return "", "", err
	}
	data := map[string]any{}
	for k, v := range templateSample {
		data[k] = v
	}
	delete(data, "SiteName")
	delete(data, "PortalURL")
	delete(data, "BrandColor")
	subj, body, err := executeParts(src, s.templateData(data))
	if err != nil {
		return "", "", err
	}
	return subjectOr(subj, previewSubjects[key]), withAttribution(s.layout(body)), nil
}

// SendTest sends one email to `to` exactly as a customer would get it,
// filled with the sample data and with "[Test] " before the subject: src
// when given (a draft, validated first), otherwise the saved or built-in
// template. Unlike the customer sends it waits for delivery and reports
// failure, including email not being set up.
func (s *EmailService) SendTest(key, src, to string) error {
	def, ok := DefaultTemplates()[key]
	if !ok {
		return fmt.Errorf("unknown email %q", key)
	}
	if src == "" {
		src = s.getTemplate(key, def)
	}
	if err := ValidateTemplate(src); err != nil {
		return err
	}
	data := map[string]any{}
	for k, v := range templateSample {
		data[k] = v
	}
	delete(data, "SiteName")
	delete(data, "PortalURL")
	delete(data, "BrandColor")
	subj, body, err := executeParts(src, s.templateData(data))
	if err != nil {
		return err
	}
	if s.sendHook == nil {
		ok, err := s.Configured()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("email is not set up")
		}
	}
	return s.Send(to, "[Test] "+subjectOr(subj, previewSubjects[key]), body)
}
