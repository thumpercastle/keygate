package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/tabloy/keygate/internal/testsupport"
)

// Email buttons take the site's brand colour when one is set, so the
// sign-in mail looks like the product it signs in to.
func TestEmailButtonColour(t *testing.T) {
	s := openNotifyStore(t)
	testsupport.LockSettings(t, s.DB)
	ctx := context.Background()
	e := NewEmailService("", "", "", "", "", slog.Default(), s)

	_ = s.DeleteSetting(ctx, "brand_color")
	if got := e.buttonColour("#18181b"); got != "#18181b" {
		t.Fatalf("no brand colour: %q, want the fallback", got)
	}
	if err := s.SetSetting(ctx, "brand_color", "#4f46e5"); err != nil {
		t.Fatal(err)
	}
	if got := e.buttonColour("#18181b"); got != "#4f46e5" {
		t.Fatalf("brand colour set: %q, want #4f46e5", got)
	}
	if err := s.SetSetting(ctx, "brand_color", `red;"><script>`); err != nil {
		t.Fatal(err)
	}
	if got := e.buttonColour("#18181b"); got != "#18181b" {
		t.Fatalf("a stored value that is not a hex colour must not reach the mail: %q", got)
	}
	if got := (&EmailService{}).buttonColour("#18181b"); got != "#18181b" {
		t.Fatalf("no store: %q", got)
	}
}
