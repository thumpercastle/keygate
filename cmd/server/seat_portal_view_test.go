package main

import (
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
)

// The licence key is a credential: with seat sign-in a member never
// needs it, so the portal hands it to the owner and seat admins only,
// and a member sees only the devices signed in with their own seat.
func TestPortalLicenseForScopesKeyAndDevices(t *testing.T) {
	accepted := time.Now()
	build := func() *model.License {
		return &model.License{
			ID: "lic", Email: "Owner@Example.com",
			Seats: []*model.Seat{
				{Email: "admin@example.com", Role: "admin", AcceptedAt: &accepted},
				{Email: "pending-admin@example.com", Role: "admin"},
				{Email: "member@example.com", Role: "member", AcceptedAt: &accepted},
			},
			Activations: []*model.Activation{
				{ID: "owner-pc", SeatEmail: "owner@example.com"},
				{ID: "member-pc", SeatEmail: "member@example.com"},
				{ID: "key-pc"},
			},
		}
	}
	cases := []struct {
		email   string
		wantKey bool
		wantIDs []string
	}{
		{"owner@example.com", true, []string{"owner-pc", "member-pc", "key-pc"}},
		{"ADMIN@example.com", true, []string{"owner-pc", "member-pc", "key-pc"}},
		{"pending-admin@example.com", false, []string{}},
		{"member@example.com", false, []string{"member-pc"}},
	}
	for _, tc := range cases {
		got := portalLicenseFor(build(), tc.email, "KG-SECRET")
		if (got.LicenseKey != "") != tc.wantKey {
			t.Errorf("%s: key given = %v, want %v", tc.email, got.LicenseKey != "", tc.wantKey)
		}
		ids := make([]string, 0, len(got.Activations))
		for _, a := range got.Activations {
			ids = append(ids, a.ID)
		}
		if len(ids) != len(tc.wantIDs) {
			t.Errorf("%s: devices = %v, want %v", tc.email, ids, tc.wantIDs)
			continue
		}
		for i := range ids {
			if ids[i] != tc.wantIDs[i] {
				t.Errorf("%s: devices = %v, want %v", tc.email, ids, tc.wantIDs)
				break
			}
		}
		if got.ActivationCount != 0 && got.ActivationCount != len(ids) {
			t.Errorf("%s: activation_count = %d, want %d", tc.email, got.ActivationCount, len(ids))
		}
	}
}
