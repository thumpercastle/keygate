package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
)

func perSeatPaymentFixture(t *testing.T, priceID string) (*store.Store, *model.Plan) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	prod := &model.Product{Name: "Per-seat payment", Slug: "psp-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(context.Background(), prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Team", Slug: "team-" + suffix,
		LicenseType: "subscription", LicenseModel: "standard",
		StripePriceID: priceID + "_" + suffix, PerSeat: true, MinQuantity: 2, MaxQuantity: 8,
	}
	if err := s.CreatePlan(context.Background(), plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return s, plan
}

func TestCreateCheckoutSession_PerSeatQuantityParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, plan := perSeatPaymentFixture(t, "price_ps_checkout")
	_ = s

	tests := []struct {
		name     string
		quantity *int
		status   int
		want     string
	}{
		{name: "defaults to minimum", status: http.StatusOK, want: "2"},
		{name: "requested quantity", quantity: intPointer(5), status: http.StatusOK, want: "5"},
		{name: "rejects quantity below minimum", quantity: intPointer(1), status: http.StatusBadRequest},
		{name: "rejects quantity above maximum", quantity: intPointer(9), status: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
					t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
					http.Error(w, `{}`, http.StatusNotFound)
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse form: %v", err)
				}
				if r.Form.Get("line_items[0][price]") != plan.StripePriceID {
					t.Errorf("price = %q", r.Form.Get("line_items[0][price]"))
				}
				got, _ = strconv.Atoi(r.Form.Get("line_items[0][quantity]"))
				if r.Form.Get("line_items[0][adjustable_quantity][enabled]") != "true" ||
					r.Form.Get("line_items[0][adjustable_quantity][minimum]") != "2" ||
					r.Form.Get("line_items[0][adjustable_quantity][maximum]") != "8" {
					t.Errorf("adjustable quantity params missing: %v", r.Form)
				}
				if r.Form.Get("metadata[quantity]") != r.Form.Get("line_items[0][quantity]") {
					t.Errorf("quantity metadata = %q, line quantity = %q", r.Form.Get("metadata[quantity]"), r.Form.Get("line_items[0][quantity]"))
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cs_test_quantity","object":"checkout.session","url":"https://checkout.stripe.test/session"}`)
			})
			bodyJSON := fmt.Sprintf(`{"price_id":%q`, plan.StripePriceID)
			if tc.quantity != nil {
				bodyJSON += fmt.Sprintf(`,"quantity":%d`, *tc.quantity)
			}
			body := strings.NewReader(bodyJSON + `}`)
			req := httptest.NewRequest(http.MethodPost, "/checkout/session", body)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = req
			(&StripeHandler{Store: s, BaseURL: "https://keygate.test"}).CreateCheckoutSession(c)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == http.StatusOK && got != mustAtoi(t, tc.want) {
				t.Fatalf("Stripe quantity = %d, want %s", got, tc.want)
			}
		})
	}
}

func TestCreateCheckoutSession_NonPerSeatIgnoresRequestedQuantity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, perSeat := perSeatPaymentFixture(t, "price_ps_nonseat")
	plan, err := s.FindPlanByID(context.Background(), perSeat.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan.PerSeat = false
	if err := s.UpdatePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	var seen bool
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("line_items[0][quantity]") != "1" {
			t.Errorf("quantity = %q, want 1", r.Form.Get("line_items[0][quantity]"))
		}
		if _, ok := r.Form["line_items[0][adjustable_quantity][enabled]"]; ok {
			t.Errorf("unexpected adjustable quantity: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cs_test_nonseat","object":"checkout.session","url":"https://checkout.stripe.test/session"}`)
	})
	req := httptest.NewRequest(http.MethodPost, "/checkout/session", strings.NewReader(fmt.Sprintf(`{"price_id":%q,"quantity":7}`, plan.StripePriceID)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = req
	(&StripeHandler{Store: s, BaseURL: "https://keygate.test"}).CreateCheckoutSession(c)
	if rr.Code != http.StatusOK || !seen {
		t.Fatalf("status=%d seen=%v body=%s", rr.Code, seen, rr.Body.String())
	}
}

func TestCheckoutByPlan_PassesRequestedQuantity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, plan := perSeatPaymentFixture(t, "price_ps_link")
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/prices/" + plan.StripePriceID:
			fmt.Fprintf(w, `{"id":%q,"object":"price","type":"recurring"}`, plan.StripePriceID)
		case "/v1/checkout/sessions":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.Form.Get("line_items[0][quantity]") != "6" || r.Form.Get("metadata[quantity]") != "6" {
				t.Errorf("quantity params = %v", r.Form)
			}
			if r.Form.Get("line_items[0][adjustable_quantity][minimum]") != "2" || r.Form.Get("line_items[0][adjustable_quantity][maximum]") != "8" {
				t.Errorf("adjustable params = %v", r.Form)
			}
			fmt.Fprint(w, `{"id":"cs_test_pay","object":"checkout.session","url":"https://checkout.stripe.test/session"}`)
		default:
			t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
			http.Error(w, `{}`, http.StatusNotFound)
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/pay/"+plan.CheckoutID+"?quantity=6", nil)
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = req
	c.Params = gin.Params{{Key: "checkout_id", Value: plan.CheckoutID}}
	(&StripeHandler{Store: s, BaseURL: "https://keygate.test"}).CheckoutByPlan(c)
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCheckoutQuantityQueryRejectsOutOfRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, plan := perSeatPaymentFixture(t, "price_ps_link_bad")
	called := false
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Errorf("Stripe called for invalid quantity: %s %s", r.Method, r.URL.Path)
	})
	req := httptest.NewRequest(http.MethodGet, "/pay/"+url.PathEscape(plan.CheckoutID)+"?quantity=9", nil)
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = req
	c.Params = gin.Params{{Key: "checkout_id", Value: plan.CheckoutID}}
	(&StripeHandler{Store: s}).CheckoutByPlan(c)
	if rr.Code != http.StatusBadRequest || called {
		t.Fatalf("status=%d StripeCalled=%v body=%q", rr.Code, called, rr.Body.String())
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func intPointer(n int) *int { return &n }

func TestSubscriptionEvent_QuantityAndPriceAreDecoded(t *testing.T) {
	var ev subscriptionEvent
	if err := json.Unmarshal([]byte(`{"id":"sub_quantity","items":{"data":[{"quantity":7,"price":{"id":"price_expected"}}]}}`), &ev); err != nil {
		t.Fatal(err)
	}
	if len(ev.Items.Data) != 1 || ev.Items.Data[0].Quantity != 7 || ev.Items.Data[0].Price.ID != "price_expected" {
		t.Fatalf("decoded item = %+v", ev.Items.Data)
	}
}

func TestFulfillCheckout_UsesMatchingSubscriptionItemQuantity(t *testing.T) {
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_match")
	subID := "sub_ps_quantity_" + plan.ID
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/subscriptions/"+subID {
			t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_other","quantity":3,"price":{"id":"price_other"}},{"id":"si_match","quantity":7,"price":{"id":%q}}]}}`, subID, plan.StripePriceID)
	})

	h := &StripeHandler{Store: s}
	email := "ps-fulfill-" + plan.ID + "@example.com"
	ok, err := h.fulfillCheckout(ctx, email, "", subID, "", map[string]string{"plan_id": plan.ID}, "test")
	if err != nil || !ok {
		t.Fatalf("fulfillCheckout = (%v, %v)", ok, err)
	}
	licenses, err := s.ListLicensesByEmail(ctx, email)
	if err != nil {
		t.Fatalf("list licenses: %v", err)
	}
	if len(licenses) != 1 || licenses[0].Quantity != 7 {
		t.Fatalf("licenses = %+v, want one license with quantity 7", licenses)
	}
	var created *model.AuditLog
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		logs, _, err := s.ListAuditLogs(ctx, "license", licenses[0].ID, "", 0, 20)
		if err != nil {
			t.Fatalf("list audit logs: %v", err)
		}
		for _, log := range logs {
			if log.Action == "created" {
				created = log
				break
			}
		}
		if created != nil {
			break
		}
	}
	if created == nil || fmt.Sprint(created.Changes["quantity"]) != "7" {
		t.Fatalf("creation audit changes = %+v, want quantity 7", created)
	}
}

func TestApplySubscriptionState_QuantityChangeAuditsAndDispatches(t *testing.T) {
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_sync")
	lic := makePerSeatSyncLicense(t, s, plan, "active", 2, "sync-change")
	type webhookMessage struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	gotWebhook := make(chan webhookMessage, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg webhookMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("decode webhook: %v", err)
		} else {
			gotWebhook <- msg
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	hook := &model.Webhook{
		ProductID: plan.ProductID, URL: receiver.URL, Secret: "whsec_quantity", Active: true,
		Events: []string{model.EventLicenseQuantityChanged},
	}
	if err := s.CreateWebhook(ctx, hook); err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	h := &StripeHandler{Store: s, WebhookSvc: service.NewWebhookService(s, slog.Default(), 2*time.Second, 1, true)}
	loaded, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	data := subscriptionEvent{Status: "active"}
	data.Items.Data = append(data.Items.Data, subscriptionItemEvent{Quantity: 7})
	data.Items.Data[0].Price.ID = plan.StripePriceID
	duplicateSnapshot, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.applySubscriptionState(ctx, loaded, &data, nil, "quantity_test")
	// Both handlers observed quantity 2 before the first persisted. The
	// second same-quantity write must not emit a second change event.
	h.applySubscriptionState(ctx, duplicateSnapshot, &data, nil, "quantity_test_duplicate")

	stored, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Quantity != 7 {
		t.Fatalf("stored quantity = %d, want 7", stored.Quantity)
	}
	var created *model.AuditLog
	auditCount := 0
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		logs, _, err := s.ListAuditLogs(ctx, "license", lic.ID, "", 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, log := range logs {
			if log.Action == "quantity_changed" {
				created = log
				auditCount++
			}
		}
		if created != nil {
			break
		}
	}
	if created == nil || auditCount != 1 || fmt.Sprint(created.Changes["old"]) != "2" || fmt.Sprint(created.Changes["new"]) != "7" {
		t.Fatalf("quantity audits = %d latest=%+v, want one old=2 new=7", auditCount, created)
	}
	var deliveryCount int
	if err := s.DB.NewRaw("SELECT count(*) FROM webhook_deliveries WHERE webhook_id = ? AND event = ?", hook.ID, model.EventLicenseQuantityChanged).Scan(ctx, &deliveryCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 {
		t.Fatalf("quantity webhook deliveries = %d, want 1", deliveryCount)
	}
	select {
	case msg := <-gotWebhook:
		if msg.Event != model.EventLicenseQuantityChanged || fmt.Sprint(msg.Data["license_id"]) != lic.ID ||
			fmt.Sprint(msg.Data["old_quantity"]) != "2" || fmt.Sprint(msg.Data["new_quantity"]) != "7" {
			t.Fatalf("quantity webhook = %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("quantity changed webhook was not delivered")
	}
}

func TestApplySubscriptionState_QuantityGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		stale  bool
	}{
		{name: "stale read", status: model.StatusActive, stale: true},
		{name: "suspended license", status: model.StatusSuspended},
		{name: "revoked license", status: model.StatusRevoked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, plan := perSeatPaymentFixture(t, "price_ps_guard_"+strings.ReplaceAll(tc.name, " ", "_"))
			lic := makePerSeatSyncLicense(t, s, plan, tc.status, 2, "guard-"+strings.ReplaceAll(tc.name, " ", "-"))
			loaded, err := s.FindLicenseByID(ctx, lic.ID)
			if err != nil {
				t.Fatal(err)
			}
			var readAt *time.Time
			if tc.stale {
				newer := time.Now()
				if err := s.UpdateLicenseFromSubscriptionRead(ctx, loaded, newer, "quantity"); err != nil {
					t.Fatalf("seed subscription read stamp: %v", err)
				}
				loaded, err = s.FindLicenseByID(ctx, lic.ID)
				if err != nil {
					t.Fatal(err)
				}
				older := newer.Add(-time.Second)
				readAt = &older
			}
			data := subscriptionEvent{Status: "active"}
			data.Items.Data = append(data.Items.Data, subscriptionItemEvent{Quantity: 7})
			data.Items.Data[0].Price.ID = plan.StripePriceID
			(&StripeHandler{Store: s}).applySubscriptionState(ctx, loaded, &data, readAt, "quantity_guard_test")
			stored, err := s.FindLicenseByID(ctx, lic.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Quantity != 2 || stored.Status != tc.status {
				t.Fatalf("license changed under guard: quantity=%d status=%s", stored.Quantity, stored.Status)
			}
			logs, _, err := s.ListAuditLogs(ctx, "license", lic.ID, "", 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, log := range logs {
				if log.Action == "quantity_changed" {
					t.Fatalf("quantity audit emitted for rejected write: %+v", log)
				}
			}
		})
	}
}

func TestApplySubscriptionState_UsesPersistedQuantityWhenSnapshotMatchesIncoming(t *testing.T) {
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_snapshot")
	lic := makePerSeatSyncLicense(t, s, plan, model.StatusActive, 5, "sync-snapshot")
	previousRead := time.Now()
	loaded, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLicenseFromSubscriptionRead(ctx, loaded, previousRead, "quantity"); err != nil {
		t.Fatalf("seed older quantity read: %v", err)
	}
	// Another handler had an ahead-of-database snapshot. Its incoming
	// Stripe answer equals that snapshot, but it must still be written
	// over the database's quantity 5 because its read is newer.
	loaded.Quantity = 7
	newerRead := previousRead.Add(time.Second)
	data := subscriptionEvent{Status: "active"}
	data.Items.Data = append(data.Items.Data, subscriptionItemEvent{Quantity: 7})
	data.Items.Data[0].Price.ID = plan.StripePriceID
	(&StripeHandler{Store: s}).applySubscriptionState(ctx, loaded, &data, &newerRead, "quantity_snapshot_test")
	stored, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Quantity != 7 {
		t.Fatalf("stored quantity = %d, want newer Stripe quantity 7", stored.Quantity)
	}
	var audit *model.AuditLog
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		logs, _, err := s.ListAuditLogs(ctx, "license", lic.ID, "", 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, log := range logs {
			if log.Action == "quantity_changed" {
				audit = log
				break
			}
		}
		if audit != nil {
			break
		}
	}
	if audit == nil || fmt.Sprint(audit.Changes["old"]) != "5" || fmt.Sprint(audit.Changes["new"]) != "7" {
		t.Fatalf("quantity audit = %+v, want old=5 new=7", audit)
	}
}

func TestOnSubscriptionUpdated_SyncsQuantityFromCurrentSubscription(t *testing.T) {
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_on_update")
	lic := makePerSeatSyncLicense(t, s, plan, model.StatusActive, 2, "sync-current")
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/subscriptions/"+lic.StripeSubscriptionID {
			t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_current","quantity":7,"price":{"id":%q}}]}}`, lic.StripeSubscriptionID, plan.StripePriceID)
	})
	h := &StripeHandler{Store: s}
	raw := []byte(fmt.Sprintf(`{"id":%q,"status":"active"}`, lic.StripeSubscriptionID))
	if err := h.onSubscriptionUpdated(ctx, raw); err != nil {
		t.Fatalf("onSubscriptionUpdated: %v", err)
	}
	stored, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Quantity != 7 {
		t.Fatalf("synced quantity = %d, want 7", stored.Quantity)
	}
}

func TestApplySubscriptionState_NonPerSeatIgnoresQuantity(t *testing.T) {
	ctx := context.Background()
	s, plan := perSeatPaymentFixture(t, "price_ps_nonseat_sync")
	plan.PerSeat = false
	if err := s.UpdatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	lic := makePerSeatSyncLicense(t, s, plan, model.StatusActive, 1, "sync-nonseat")
	loaded, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	data := subscriptionEvent{Status: "active"}
	data.Items.Data = append(data.Items.Data, subscriptionItemEvent{Quantity: 7})
	data.Items.Data[0].Price.ID = plan.StripePriceID
	(&StripeHandler{Store: s}).applySubscriptionState(ctx, loaded, &data, nil, "quantity_nonseat_test")
	stored, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Quantity != 1 {
		t.Fatalf("non-per-seat quantity changed to %d", stored.Quantity)
	}
}

func TestChangePlan_PreservesAndClampsSubscriptionQuantity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name               string
		oldPerSeat         bool
		newPerSeat         bool
		currentQuantity    int64
		targetMaxQuantity  int
		wantStripeQuantity string
		wantQuantityEvent  bool
	}{
		{name: "per seat to non per seat resets to one", oldPerSeat: true, currentQuantity: 5, wantStripeQuantity: "1", wantQuantityEvent: true},
		{name: "non per seat to per seat clamps one to minimum", newPerSeat: true, currentQuantity: 1, wantStripeQuantity: "2", wantQuantityEvent: true},
		{name: "per seat to per seat preserves in-range quantity", oldPerSeat: true, newPerSeat: true, currentQuantity: 6, wantStripeQuantity: "6"},
		{name: "per seat to per seat clamps to new maximum", oldPerSeat: true, newPerSeat: true, currentQuantity: 50, targetMaxQuantity: 10, wantStripeQuantity: "10", wantQuantityEvent: true},
		{name: "legacy to legacy omits quantity", currentQuantity: 6, wantStripeQuantity: "", wantQuantityEvent: false},
		{name: "legacy metered item without quantity stays omitted", currentQuantity: 0, wantStripeQuantity: "", wantQuantityEvent: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, source := perSeatPaymentFixture(t, "price_ps_change_source")
			if !tc.oldPerSeat {
				source.PerSeat = false
				if err := s.UpdatePlan(ctx, source); err != nil {
					t.Fatal(err)
				}
			}
			targetMaxQuantity := tc.targetMaxQuantity
			if targetMaxQuantity == 0 {
				targetMaxQuantity = 8
			}
			target := &model.Plan{
				ProductID: source.ProductID, Name: "Target", Slug: "target-" + source.ID,
				LicenseType: "subscription", LicenseModel: "standard",
				StripePriceID: "price_ps_change_target_" + source.ID,
				PerSeat:       tc.newPerSeat, MinQuantity: 2, MaxQuantity: targetMaxQuantity,
			}
			if err := s.CreatePlan(ctx, target); err != nil {
				t.Fatal(err)
			}
			initialQuantity := int(tc.currentQuantity)
			if !tc.oldPerSeat && !tc.newPerSeat {
				initialQuantity = 1
			}
			lic := &model.License{
				ProductID: source.ProductID, PlanID: source.ID, Email: "change-" + source.ID + "@example.com",
				LicenseKey: "KEY-change-" + source.ID, Status: model.StatusActive,
				Quantity: initialQuantity, StripeSubscriptionID: "sub_change_" + source.ID,
			}
			if err := s.CreateLicense(ctx, lic); err != nil {
				t.Fatal(err)
			}
			if err := store.SyncLicenseSubscriptionIn(ctx, s.DB, lic.ID, source, model.StatusActive, nil); err != nil {
				t.Fatalf("create subscription row: %v", err)
			}
			type webhookMessage struct {
				Event string         `json:"event"`
				Data  map[string]any `json:"data"`
			}
			messages := make(chan webhookMessage, 4)
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var msg webhookMessage
				if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
					t.Errorf("decode webhook: %v", err)
				} else {
					messages <- msg
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer receiver.Close()
			hook := &model.Webhook{
				ProductID: source.ProductID, URL: receiver.URL, Secret: "whsec_change_" + source.ID,
				Active: true, Events: []string{"plan.changed", model.EventLicenseQuantityChanged},
			}
			if err := s.CreateWebhook(ctx, hook); err != nil {
				t.Fatal(err)
			}
			oldRead, err := s.StripeReadStamp(ctx)
			if err != nil {
				t.Fatal(err)
			}
			stripeQuantity := ""
			stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/subscriptions/"+lic.StripeSubscriptionID {
					t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
					http.Error(w, `{}`, http.StatusNotFound)
					return
				}
				if r.Method == http.MethodGet {
					if !tc.oldPerSeat && !tc.newPerSeat && tc.currentQuantity == 0 {
						fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_change","price":{"id":%q}}]}}`, lic.StripeSubscriptionID, source.StripePriceID)
					} else {
						fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_change","quantity":%d,"price":{"id":%q}}]}}`,
							lic.StripeSubscriptionID, tc.currentQuantity, source.StripePriceID)
					}
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse update form: %v", err)
				}
				stripeQuantity = r.Form.Get("items[0][quantity]")
				if !tc.oldPerSeat && !tc.newPerSeat {
					if _, ok := r.Form["items[0][quantity]"]; ok {
						t.Errorf("legacy plan change sent quantity: %v", r.Form)
					}
				} else if _, ok := r.Form["items[0][quantity]"]; !ok {
					t.Errorf("per-seat plan change omitted quantity: %v", r.Form)
				}
				if r.Form.Get("items[0][price]") != target.StripePriceID {
					t.Errorf("updated price = %q, want %q", r.Form.Get("items[0][price]"), target.StripePriceID)
				}
				if stripeQuantity == "" {
					fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_change","price":{"id":%q}}]}}`, lic.StripeSubscriptionID, target.StripePriceID)
				} else {
					fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_change","quantity":%s,"price":{"id":%q}}]}}`, lic.StripeSubscriptionID, stripeQuantity, target.StripePriceID)
				}
			})
			req := httptest.NewRequest(http.MethodPost, "/billing/change-plan", strings.NewReader(fmt.Sprintf(`{"license_id":%q,"new_price_id":%q}`, lic.ID, target.StripePriceID)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = req
			c.Set("email", lic.Email)
			h := &StripeHandler{Store: s, WebhookSvc: service.NewWebhookService(s, slog.Default(), 2*time.Second, 1, true)}
			h.ChangePlan(c)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if stripeQuantity != tc.wantStripeQuantity {
				t.Fatalf("Stripe quantity = %q, want %q", stripeQuantity, tc.wantStripeQuantity)
			}
			stored, err := s.FindLicenseByID(ctx, lic.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantStoredQuantity := int(tc.currentQuantity)
			if !tc.oldPerSeat && !tc.newPerSeat {
				wantStoredQuantity = 1
			}
			if tc.wantStripeQuantity != "" {
				wantStoredQuantity = mustAtoi(t, tc.wantStripeQuantity)
			}
			if stored.Quantity != wantStoredQuantity || stored.PlanID != target.ID {
				t.Fatalf("stored license quantity=%d plan=%s; want quantity=%d plan=%s", stored.Quantity, stored.PlanID, wantStoredQuantity, target.ID)
			}
			subRecord, err := s.FindSubscriptionByLicense(ctx, lic.ID)
			if err != nil || subRecord.PlanID != target.ID {
				t.Fatalf("subscription plan=%q (err %v), want %q", subRecord.PlanID, err, target.ID)
			}
			if !tc.oldPerSeat && tc.newPerSeat {
				staleSnapshot, err := s.FindLicenseByID(ctx, lic.ID)
				if err != nil {
					t.Fatal(err)
				}
				staleEvent := subscriptionEvent{Status: "active"}
				staleEvent.Items.Data = append(staleEvent.Items.Data, subscriptionItemEvent{Quantity: tc.currentQuantity})
				staleEvent.Items.Data[0].Price.ID = target.StripePriceID
				(&StripeHandler{Store: s}).applySubscriptionState(ctx, staleSnapshot, &staleEvent, &oldRead, "change_plan_stale_read_test")
				reloaded, err := s.FindLicenseByID(ctx, lic.ID)
				if err != nil {
					t.Fatal(err)
				}
				if reloaded.Quantity != mustAtoi(t, tc.wantStripeQuantity) {
					t.Fatalf("older read reset quantity to %d; want %s", reloaded.Quantity, tc.wantStripeQuantity)
				}
			}
			wantMessages := 1
			if tc.wantQuantityEvent {
				wantMessages++
			}
			seen := map[string]map[string]any{}
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			for len(seen) < wantMessages {
				select {
				case msg := <-messages:
					seen[msg.Event] = msg.Data
				case <-timer.C:
					t.Fatalf("webhooks received = %v, want plan.changed and quantity event=%v", seen, tc.wantQuantityEvent)
				}
			}
			if _, ok := seen["plan.changed"]; !ok {
				t.Fatalf("plan.changed missing: %v", seen)
			}
			var quantityAudits []*model.AuditLog
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				logs, _, err := s.ListAuditLogs(ctx, "license", lic.ID, "", 0, 20)
				if err != nil {
					t.Fatal(err)
				}
				quantityAudits = quantityAudits[:0]
				for _, log := range logs {
					if log.Action == "quantity_changed" {
						quantityAudits = append(quantityAudits, log)
					}
				}
				if len(quantityAudits) > 0 || !tc.wantQuantityEvent {
					break
				}
			}
			if tc.wantQuantityEvent {
				msg, ok := seen[model.EventLicenseQuantityChanged]
				if !ok || fmt.Sprint(msg["old_quantity"]) != fmt.Sprint(tc.currentQuantity) || fmt.Sprint(msg["new_quantity"]) != tc.wantStripeQuantity {
					t.Fatalf("quantity webhook = %+v", msg)
				}
				if len(quantityAudits) != 1 || fmt.Sprint(quantityAudits[0].Changes["old"]) != fmt.Sprint(tc.currentQuantity) || fmt.Sprint(quantityAudits[0].Changes["new"]) != tc.wantStripeQuantity {
					t.Fatalf("quantity audits = %+v", quantityAudits)
				}
			} else if _, ok := seen[model.EventLicenseQuantityChanged]; ok || len(quantityAudits) != 0 {
				t.Fatalf("unchanged quantity emitted change side effects: seen=%v audits=%+v", seen, quantityAudits)
			}
		})
	}
}

func TestChangePlan_RejectsRestrictedLicensesBeforeStripe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		status string
		by     string
	}{
		{name: "admin suspended", status: model.StatusSuspended, by: model.SuspendedByAdmin},
		{name: "Stripe paused", status: model.StatusSuspended, by: model.SuspendedByStripe},
		{name: "revoked", status: model.StatusRevoked},
		{name: "canceled", status: model.StatusCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, source := perSeatPaymentFixture(t, "price_ps_change_restricted")
			target := &model.Plan{ProductID: source.ProductID, Name: "Target", Slug: "restricted-target-" + source.ID, LicenseType: "subscription", LicenseModel: "standard", StripePriceID: "price_ps_restricted_" + source.ID, Active: true, PerSeat: true, MinQuantity: 2, MaxQuantity: 8}
			if err := s.CreatePlan(ctx, target); err != nil {
				t.Fatal(err)
			}
			lic := &model.License{ProductID: source.ProductID, PlanID: source.ID, Email: "restricted-" + source.ID + "@example.com", LicenseKey: "KEY-restricted-" + source.ID, Status: tc.status, Quantity: 2, StripeSubscriptionID: "sub_restricted_" + source.ID}
			if tc.status == model.StatusSuspended {
				now := time.Now()
				lic.SuspendedAt, lic.SuspendedBy = &now, tc.by
			}
			if err := s.CreateLicense(ctx, lic); err != nil {
				t.Fatal(err)
			}
			if err := store.SyncLicenseSubscriptionIn(ctx, s.DB, lic.ID, source, model.StatusActive, nil); err != nil {
				t.Fatal(err)
			}
			stripeCalled := false
			stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
				stripeCalled = true
				http.Error(w, `{}`, http.StatusInternalServerError)
			})
			req := httptest.NewRequest(http.MethodPost, "/billing/change-plan", strings.NewReader(fmt.Sprintf(`{"license_id":%q,"new_price_id":%q}`, lic.ID, target.StripePriceID)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = req
			c.Set("email", lic.Email)
			(&StripeHandler{Store: s}).ChangePlan(c)
			if rr.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s, want 409", rr.Code, rr.Body.String())
			}
			if stripeCalled {
				t.Fatal("Stripe was called for restricted license")
			}
		})
	}
}

func TestChangePlan_NewerSubscriptionStampDuringStripeUpdateStillPersistsAcceptedPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		status      string
		suspendedBy string
		subStatus   string
		newerStamp  bool
	}{
		{name: "newer read preserves quantity", status: model.StatusActive, subStatus: "active", newerStamp: true},
		{name: "admin suspension is preserved", status: model.StatusSuspended, suspendedBy: model.SuspendedByAdmin, subStatus: "paused"},
		{name: "revocation is preserved", status: model.StatusRevoked, subStatus: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, source := perSeatPaymentFixture(t, "price_ps_change_stamp")
			target := &model.Plan{ProductID: source.ProductID, Name: "Target", Slug: "stamp-target-" + source.ID, LicenseType: "subscription", LicenseModel: "standard", StripePriceID: "price_ps_stamp_" + source.ID, Active: true, PerSeat: true, MinQuantity: 2, MaxQuantity: 8}
			if err := s.CreatePlan(ctx, target); err != nil {
				t.Fatal(err)
			}
			lic := &model.License{ProductID: source.ProductID, PlanID: source.ID, Email: "stamp-" + source.ID + "@example.com", LicenseKey: "KEY-stamp-" + source.ID, Status: model.StatusActive, Quantity: 3, StripeSubscriptionID: "sub_stamp_" + source.ID}
			if err := s.CreateLicense(ctx, lic); err != nil {
				t.Fatal(err)
			}
			if err := store.SyncLicenseSubscriptionIn(ctx, s.DB, lic.ID, source, model.StatusActive, nil); err != nil {
				t.Fatal(err)
			}
			newerStamp := time.Now().Add(time.Minute)
			stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_stamp","quantity":3,"price":{"id":%q}}]}}`, lic.StripeSubscriptionID, source.StripePriceID)
					return
				}
				if tc.newerStamp {
					if _, err := s.DB.NewRaw("UPDATE licenses SET stripe_synced_at = ?, quantity = 7 WHERE id = ?", newerStamp, lic.ID).Exec(ctx); err != nil {
						t.Errorf("advance subscription stamp: %v", err)
					}
				}
				if tc.status != model.StatusActive {
					if _, err := s.DB.NewRaw("UPDATE licenses SET quantity = 7, status = ?, suspended_by = ?, suspended_at = now() WHERE id = ?", tc.status, tc.suspendedBy, lic.ID).Exec(ctx); err != nil {
						t.Errorf("change license state during update: %v", err)
					}
				}
				if _, err := s.DB.NewRaw("UPDATE subscriptions SET status = ? WHERE license_id = ?", tc.subStatus, lic.ID).Exec(ctx); err != nil {
					t.Errorf("change subscription state during update: %v", err)
				}
				fmt.Fprintf(w, `{"id":%q,"object":"subscription","status":"active","items":{"data":[{"id":"si_stamp","quantity":3,"price":{"id":%q}}]}}`, lic.StripeSubscriptionID, target.StripePriceID)
			})
			req := httptest.NewRequest(http.MethodPost, "/billing/change-plan", strings.NewReader(fmt.Sprintf(`{"license_id":%q,"new_price_id":%q}`, lic.ID, target.StripePriceID)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			c.Request = req
			c.Set("email", lic.Email)
			(&StripeHandler{Store: s}).ChangePlan(c)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want Stripe-accepted change reconciled", rr.Code, rr.Body.String())
			}
			stored, err := s.FindLicenseByID(ctx, lic.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.PlanID != target.ID || stored.Quantity != 7 || stored.Status != tc.status || stored.SuspendedBy != tc.suspendedBy {
				t.Fatalf("local state plan=%s quantity=%d status=%s suspended_by=%s, want accepted plan and preserved concurrent state", stored.PlanID, stored.Quantity, stored.Status, stored.SuspendedBy)
			}
			subRecord, err := s.FindSubscriptionByLicense(ctx, lic.ID)
			if err != nil || subRecord.PlanID != target.ID || subRecord.Status != tc.subStatus {
				t.Fatalf("subscription plan=%q status=%q (err %v), want accepted plan and preserved status", subRecord.PlanID, subRecord.Status, err)
			}
		})
	}
}

func makePerSeatSyncLicense(t *testing.T, s *store.Store, plan *model.Plan, status string, quantity int, suffix string) *model.License {
	t.Helper()
	ctx := context.Background()
	suffix += "-" + plan.ID
	lic := &model.License{
		ProductID: plan.ProductID, PlanID: plan.ID, Email: suffix + "@example.com",
		LicenseKey: "KEY-" + suffix, Status: status, Quantity: quantity,
		StripeSubscriptionID: "sub_" + suffix,
	}
	if status == model.StatusSuspended {
		now := time.Now()
		lic.SuspendedAt, lic.SuspendedBy = &now, model.SuspendedByAdmin
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}
	return lic
}
