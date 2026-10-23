package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

func TestDeleteLicenseRemovesRowAndDependents(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()
	gin.SetMode(gin.TestMode)
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	prod := &model.Product{Name: "Delete", Slug: "delete-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Seats", Slug: "delete-plan-" + suffix,
		LicenseType: "subscription", LicenseModel: "standard", PerSeat: true,
		MinQuantity: 1, MaxQuantity: 5, MaxSeats: 1, MaxActivations: 1,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	newLicense := func(tag string) *model.License {
		lic := &model.License{
			ProductID: prod.ID, PlanID: plan.ID, Email: tag + "-" + suffix + "@example.com",
			LicenseKey: "kg_del_" + tag + suffix, Status: model.StatusActive, Quantity: 1,
		}
		if err := s.CreateLicense(ctx, lic); err != nil {
			t.Fatal(err)
		}
		return lic
	}
	h := &AdminHandler{Store: s}
	call := func(id, confirm string) (int, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/admin/licenses/"+id, strings.NewReader(`{"confirm":"`+confirm+`"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.DeleteLicense(c)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		// A bodyless 204 never flushes to the recorder, so read gin's status.
		return c.Writer.Status(), body.Error.Code
	}

	t.Run("confirmation must match the licence email", func(t *testing.T) {
		lic := newLicense("mismatch")
		if code, ec := call(lic.ID, "someone-else@example.com"); code != http.StatusBadRequest || ec != "CONFIRMATION_MISMATCH" {
			t.Fatalf("mismatched confirm = %d %q, want 400 CONFIRMATION_MISMATCH", code, ec)
		}
		if _, err := s.FindLicenseByID(ctx, lic.ID); err != nil {
			t.Fatalf("licence was removed despite a mismatched confirmation: %v", err)
		}
	})

	t.Run("a live Stripe subscription blocks deletion", func(t *testing.T) {
		lic := newLicense("stripe")
		lic.StripeSubscriptionID, lic.PaymentProvider = "sub_del_"+suffix, "stripe"
		if err := s.UpdateLicense(ctx, lic, "stripe_subscription_id", "payment_provider"); err != nil {
			t.Fatal(err)
		}
		if code, ec := call(lic.ID, lic.Email); code != http.StatusConflict || ec != "STRIPE_ACTIVE" {
			t.Fatalf("Stripe-backed delete = %d %q, want 409 STRIPE_ACTIVE", code, ec)
		}
		lic.Status = model.StatusCanceled
		if err := s.UpdateLicense(ctx, lic, "status"); err != nil {
			t.Fatal(err)
		}
		if code, _ := call(lic.ID, lic.Email); code != http.StatusNoContent {
			t.Fatalf("canceled Stripe-backed delete = %d, want 204", code)
		}
	})

	t.Run("deletes the licence, its activations and seats, and audits it", func(t *testing.T) {
		lic := newLicense("gone")
		act := &model.Activation{LicenseID: lic.ID, Identifier: "machine-" + suffix, IdentifierType: "device"}
		if err := s.CreateActivation(ctx, act); err != nil {
			t.Fatal(err)
		}
		seat := &model.Seat{LicenseID: lic.ID, Email: "member-" + suffix + "@example.com", Role: "member"}
		if err := s.CreateSeat(ctx, seat); err != nil {
			t.Fatal(err)
		}
		// Case and padding in the typed confirmation are forgiven.
		if code, ec := call(lic.ID, "  "+strings.ToUpper(lic.Email)+" "); code != http.StatusNoContent {
			t.Fatalf("delete = %d %q, want 204", code, ec)
		}
		for table, id := range map[string]string{"licenses": lic.ID, "activations": act.ID, "seats": seat.ID} {
			n, err := s.DB.NewSelect().Table(table).Where("id = ?", id).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%s row %s survived the delete", table, id)
			}
		}
		// Store.Audit writes in the background.
		var n int
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			n, err = s.DB.NewSelect().Table("audit_logs").
				Where("entity = 'license' AND entity_id = ? AND action = 'deleted'", lic.ID).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				break
			}
		}
		if n != 1 {
			t.Fatalf("audit rows for the delete = %d, want 1", n)
		}
	})

	t.Run("unknown licence is 404", func(t *testing.T) {
		if code, _ := call("00000000-0000-0000-0000-000000000000", "x@example.com"); code != http.StatusNotFound {
			t.Fatalf("missing licence delete = %d, want 404", code)
		}
	})
}
