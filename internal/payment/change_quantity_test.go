package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

func callChangeQuantity(h *StripeHandler, email string, body map[string]any) (int, string) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/portal/subscription/quantity", &buf)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("email", email)
	h.ChangeQuantity(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, errCode(out)
}

// The owner changes the seat count of a Stripe-billed per-seat licence
// from the portal: Stripe's subscription item takes the new quantity,
// prorated, and the licence follows it straight away.
func TestChangeQuantity_UpdatesStripeAndTheLicence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_qty")
	lic := makePerSeatSyncLicense(t, s, plan, model.StatusActive, 3, "qty")
	if err := store.SyncLicenseSubscriptionIn(ctx, s.DB, lic.ID, plan, model.StatusActive, nil); err != nil {
		t.Fatal(err)
	}
	var update url.Values
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		sub := fmt.Sprintf(`{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_other","quantity":9,"price":{"id":"price_addon"}},{"id":"si_seats","quantity":%%d,"price":{"id":%q}}]}}`,
			lic.StripeSubscriptionID, plan.StripePriceID)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions/"+lic.StripeSubscriptionID:
			fmt.Fprintf(w, sub, 3)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/subscriptions/"+lic.StripeSubscriptionID:
			body, _ := io.ReadAll(r.Body)
			update, _ = url.ParseQuery(string(body))
			fmt.Fprintf(w, sub, 5)
		default:
			t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{}`, http.StatusNotFound)
		}
	})
	h := &StripeHandler{Store: s}

	if code, ec := callChangeQuantity(h, lic.Email, map[string]any{"license_id": lic.ID, "quantity": 5}); code != http.StatusOK {
		t.Fatalf("change quantity = %d %s", code, ec)
	}
	if update.Get("items[0][id]") != "si_seats" || update.Get("items[0][quantity]") != "5" ||
		update.Get("proration_behavior") != "create_prorations" {
		t.Fatalf("Stripe update = %v, want the seat item set to 5, prorated", update)
	}
	got, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil || got.Quantity != 5 {
		t.Fatalf("licence quantity = %d (%v), want 5", got.Quantity, err)
	}
	var audited bool
	for deadline := time.Now().Add(2 * time.Second); !audited && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		logs, _, _ := s.ListAuditLogs(ctx, "license", lic.ID, "", 0, 20)
		for _, l := range logs {
			if l.Action == "quantity_changed" && fmt.Sprint(l.Changes["new"]) == "5" {
				audited = true
			}
		}
	}
	if !audited {
		t.Fatal("no quantity_changed audit entry")
	}
}

// Refusals happen before Stripe is asked anything.
func TestChangeQuantity_Refusals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_qty_refuse")
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Stripe called on a refused change: %s %s", r.Method, r.URL.Path)
		http.Error(w, `{}`, http.StatusNotFound)
	})
	h := &StripeHandler{Store: s}
	lic := makePerSeatSyncLicense(t, s, plan, model.StatusActive, 3, "qtyr")

	if code, _ := callChangeQuantity(h, "someone-else@example.com", map[string]any{"license_id": lic.ID, "quantity": 4}); code != http.StatusForbidden {
		t.Errorf("another customer's licence: %d, want 403", code)
	}
	for _, q := range []int{1, 9} { // plan bounds are 2..8
		if code, ec := callChangeQuantity(h, lic.Email, map[string]any{"license_id": lic.ID, "quantity": q}); code != http.StatusBadRequest || ec != "QUANTITY_OUT_OF_RANGE" {
			t.Errorf("quantity %d: %d %s, want 400 QUANTITY_OUT_OF_RANGE", q, code, ec)
		}
	}

	canceled := makePerSeatSyncLicense(t, s, plan, model.StatusCanceled, 3, "qtyc")
	if code, ec := callChangeQuantity(h, canceled.Email, map[string]any{"license_id": canceled.ID, "quantity": 4}); code != http.StatusConflict || ec != "LICENSE_QUANTITY_CHANGE_UNAVAILABLE" {
		t.Errorf("canceled licence: %d %s, want 409", code, ec)
	}

	single := &model.Plan{ProductID: plan.ProductID, Name: "Single", Slug: "single-" + plan.ID, LicenseType: "subscription",
		LicenseModel: "standard", StripePriceID: "price_single_" + plan.ID}
	if err := s.CreatePlan(ctx, single); err != nil {
		t.Fatal(err)
	}
	solo := makePerSeatSyncLicense(t, s, single, model.StatusActive, 1, "qtys")
	if code, ec := callChangeQuantity(h, solo.Email, map[string]any{"license_id": solo.ID, "quantity": 2}); code != http.StatusBadRequest || ec != "NOT_PER_SEAT" {
		t.Errorf("single-seat plan: %d %s, want 400 NOT_PER_SEAT", code, ec)
	}

	manual := makePerSeatSyncLicense(t, s, plan, model.StatusActive, 3, "qtym")
	manual.StripeSubscriptionID = ""
	if err := s.UpdateLicense(ctx, manual, "stripe_subscription_id"); err != nil {
		t.Fatal(err)
	}
	if code, ec := callChangeQuantity(h, manual.Email, map[string]any{"license_id": manual.ID, "quantity": 4}); code != http.StatusBadRequest || ec != "NO_SUBSCRIPTION" {
		t.Errorf("licence without a subscription: %d %s, want 400 NO_SUBSCRIPTION", code, ec)
	}
}
