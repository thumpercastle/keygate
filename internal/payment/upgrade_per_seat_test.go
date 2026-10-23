package payment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82"

	"github.com/tabloy/keygate/internal/model"
)

// perSeatPaid turns the fixture's paid plan into a per-seat plan.
func (f *upgradeFixture) perSeatPaid(t *testing.T, min, max int) {
	t.Helper()
	f.paid.PerSeat, f.paid.MinQuantity, f.paid.MaxQuantity, f.paid.MaxSeats = true, min, max, 1
	if err := f.s.UpdatePlan(f.ctx, f.paid); err != nil {
		t.Fatalf("update plan: %v", err)
	}
}

// A multi-seat licence only moves to a plan that can hold its seats; a
// single-seat plan would silently drop everyone but the owner.
func TestUpgradeTarget_RespectsSeats(t *testing.T) {
	single := &model.Plan{ID: "single", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_1"}
	seats := &model.Plan{ID: "seats", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_2",
		PerSeat: true, MinQuantity: 1, MaxQuantity: 10}
	small := &model.Plan{ID: "small", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_3",
		PerSeat: true, MinQuantity: 1, MaxQuantity: 2}
	for _, tt := range []struct {
		quantity int
		plan     *model.Plan
		want     bool
	}{
		{1, single, true},
		{3, single, false},
		{3, seats, true},
		{3, small, false},
	} {
		lic := &model.License{ProductID: "p", PlanID: "free", Quantity: tt.quantity}
		if got := upgradeTarget(lic, tt.plan); got != tt.want {
			t.Errorf("quantity %d → %s: upgradeTarget = %v, want %v", tt.quantity, tt.plan.ID, got, tt.want)
		}
	}
}

// A licence on a free per-seat plan is a team licence the operator
// granted, not a free tier: the portal offers it no upgrade.
func TestUpgradableLicenses_FreePerSeatLicenceIsNotOffered(t *testing.T) {
	f := newUpgradeFixture(t, "upteam")
	f.perSeatPaid(t, 1, 10)
	f.free.PerSeat, f.free.MinQuantity, f.free.MaxQuantity, f.free.MaxSeats = true, 1, 10, 1
	if err := f.s.UpdatePlan(f.ctx, f.free); err != nil {
		t.Fatal(err)
	}
	f.lic.Quantity = 7
	if err := f.s.UpdateLicense(f.ctx, f.lic, "quantity"); err != nil {
		t.Fatal(err)
	}
	prev := stripe.Key
	stripe.Key = "sk_test_offer"
	t.Cleanup(func() { stripe.Key = prev })
	lic := f.reload(t)
	got, err := f.h.UpgradableLicenses(f.ctx, lic.Email, []*model.License{lic})
	if err != nil {
		t.Fatal(err)
	}
	if got[lic.ID] {
		t.Fatal("a free per-seat team licence was offered an upgrade")
	}
}

// Upgrading to a per-seat plan sells seats: the checkout carries an
// adjustable quantity, and the licence takes the quantity Stripe bills.
// When the subscription ends the licence goes back to the free plan
// with the quantity it had.
func TestUpgrade_PerSeatCarriesQuantityAndRevertRestoresIt(t *testing.T) {
	f := newUpgradeFixture(t, "upseats")
	f.perSeatPaid(t, 2, 10)
	subID, sessionID := "sub_seats_"+f.tag, "cs_seats_"+f.tag
	var checkoutForm url.Values
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/prices/"+f.paid.StripePriceID:
			fmt.Fprintf(w, `{"id":%q,"object":"price","active":true,"type":"recurring"}`, f.paid.StripePriceID)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			body, _ := io.ReadAll(r.Body)
			checkoutForm, _ = url.ParseQuery(string(body))
			fmt.Fprintf(w, `{"id":%q,"object":"checkout.session","url":"https://checkout.example/%s"}`, sessionID, sessionID)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions/"+subID:
			fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_1","quantity":4,"price":{"id":%q}}]}}`,
				subID, f.paid.StripePriceID)
		default:
			t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{}`, http.StatusNotFound)
		}
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]string{"license_id": f.lic.ID, "plan_id": f.paid.ID})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/portal/licenses/upgrade", &buf)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("email", f.lic.Email)
	f.h.StartUpgrade(c)
	if w.Code != http.StatusOK {
		t.Fatalf("StartUpgrade = %d: %s", w.Code, w.Body.String())
	}
	if got := checkoutForm.Get("line_items[0][quantity]"); got != "2" {
		t.Errorf("checkout quantity = %q, want the plan minimum 2", got)
	}
	if checkoutForm.Get("line_items[0][adjustable_quantity][enabled]") != "true" ||
		checkoutForm.Get("line_items[0][adjustable_quantity][minimum]") != "2" ||
		checkoutForm.Get("line_items[0][adjustable_quantity][maximum]") != "10" {
		t.Errorf("checkout adjustable quantity = %v", checkoutForm)
	}

	ok, err := f.h.fulfillCheckout(f.ctx, f.lic.Email, "cus_"+f.tag, subID, "", f.meta(sessionID), "webhook")
	if err != nil || !ok {
		t.Fatalf("fulfil: ok=%v err=%v", ok, err)
	}
	if l := f.reload(t); l.PlanID != f.paid.ID || l.Quantity != 4 {
		t.Fatalf("after upgrade: plan=%s quantity=%d, want paid plan with 4 seats", l.PlanID, l.Quantity)
	}

	lic := f.reload(t)
	if err := f.h.applySubscriptionState(f.ctx, lic, &subscriptionEvent{Status: "canceled"}, nil, "test"); err != nil {
		t.Fatalf("end subscription: %v", err)
	}
	if l := f.reload(t); l.PlanID != f.free.ID || l.Quantity != 1 {
		t.Fatalf("after the subscription ended: plan=%s quantity=%d, want the free plan with 1 seat", l.PlanID, l.Quantity)
	}
}

// An upgrade never leaves a licence with fewer devices than it has: a
// free licence that allows seven computers is not offered a plan that
// allows one.
func TestUpgradeTarget_RespectsDevices(t *testing.T) {
	free7 := &model.Plan{ID: "free7", ProductID: "p", Active: true, LicenseType: "perpetual", MaxActivations: 7}
	free1 := &model.Plan{ID: "free1", ProductID: "p", Active: true, LicenseType: "trial", MaxActivations: 1}
	one := &model.Plan{ID: "one", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_1", MaxActivations: 1}
	seven := &model.Plan{ID: "seven", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_7", MaxActivations: 7}
	unlimited := &model.Plan{ID: "unl", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_u"}
	perSeat := &model.Plan{ID: "seats", ProductID: "p", Active: true, LicenseType: "subscription", StripePriceID: "price_s",
		PerSeat: true, MinQuantity: 1, MaxQuantity: 10, MaxActivations: 1, MaxSeats: 1}
	for _, tt := range []struct {
		from, to *model.Plan
		want     bool
	}{
		{free7, one, false},
		{free7, seven, true},
		{free7, unlimited, true},
		{free7, perSeat, false}, // starts at one seat, so one device
		{free1, one, true},
		{free1, perSeat, true},
	} {
		lic := &model.License{ProductID: "p", PlanID: tt.from.ID, Plan: tt.from, Quantity: 1}
		if got := upgradeTarget(lic, tt.to); got != tt.want {
			t.Errorf("%s → %s: upgradeTarget = %v, want %v", tt.from.ID, tt.to.ID, got, tt.want)
		}
	}
}
