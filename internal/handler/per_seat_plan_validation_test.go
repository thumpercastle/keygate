package handler

import (
	"context"
	"encoding/json"
	"fmt"
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

// These requests fail before the handler needs a store, making the API's
// quantity validation independently testable and ensuring bad values get a
// useful 400 instead of a database constraint error.
func TestCreatePlanRejectsInvalidPerSeatConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
	}{
		{"minimum below one", `{"product_id":"p","name":"Plan","slug":"plan","license_type":"subscription","per_seat":true,"min_quantity":0,"max_quantity":5}`},
		{"minimum above maximum", `{"product_id":"p","name":"Plan","slug":"plan","license_type":"subscription","per_seat":true,"min_quantity":6,"max_quantity":5}`},
		{"maximum above bound", `{"product_id":"p","name":"Plan","slug":"plan","license_type":"subscription","per_seat":true,"min_quantity":1,"max_quantity":10001}`},
		{"per seat requires subscription", `{"product_id":"p","name":"Plan","slug":"plan","license_type":"perpetual","per_seat":true,"min_quantity":1,"max_quantity":5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/admin/plans", strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			(&AdminHandler{}).CreatePlan(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("CreatePlan status = %d, want 400; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestLicenseResponseWrappersKeepAdditionalFields(t *testing.T) {
	license := &model.License{ID: "license-id", Quantity: 3, Plan: &model.Plan{MaxActivations: 2, MaxSeats: 1, PerSeat: true}}
	tests := []struct {
		name  string
		value any
		want  []string
	}{
		{name: "key", value: licenseWithKey{License: license, LicenseKey: "secret"}, want: []string{"license_key", "effective_max_activations", "effective_max_seats"}},
		{name: "hint", value: licenseWithHint{License: license, LicenseKeyHint: "abcd", KeyUsable: true}, want: []string{"license_key_hint", "key_usable", "effective_max_activations", "effective_max_seats"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			for _, field := range tt.want {
				if _, ok := fields[field]; !ok {
					t.Errorf("%s response omitted %q: %s", tt.name, field, body)
				}
			}
		})
	}
}

func TestLicenseWrappersOmitEffectiveLimitsWithoutPlan(t *testing.T) {
	for name, value := range map[string]any{
		"key":  licenseWithKey{License: &model.License{ID: "license-id", Quantity: 1}, LicenseKey: "secret"},
		"hint": licenseWithHint{License: &model.License{ID: "license-id", Quantity: 1}, LicenseKeyHint: "abcd", KeyUsable: true},
	} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"effective_max_activations", "effective_max_seats"} {
			if _, ok := fields[key]; ok {
				t.Errorf("%s response emitted %s without a loaded plan: %s", name, key, body)
			}
		}
	}
}

func TestNormalizePerSeatQuantityBounds(t *testing.T) {
	tests := []struct {
		name             string
		min, max         *int
		wantMin, wantMax int
		wantErr          bool
	}{
		{name: "omitted bounds default to one", wantMin: 1, wantMax: 1},
		{name: "explicit valid bounds", min: intPtr(4), max: intPtr(9), wantMin: 4, wantMax: 9},
		{name: "explicit zero is rejected", min: intPtr(0), max: intPtr(5), wantErr: true},
		{name: "minimum exceeds maximum", min: intPtr(6), max: intPtr(5), wantErr: true},
		{name: "maximum exceeds bound", min: intPtr(1), max: intPtr(10001), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			min, max, err := normalizeQuantityBounds(tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeQuantityBounds error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (min != tt.wantMin || max != tt.wantMax) {
				t.Fatalf("normalizeQuantityBounds = (%d, %d), want (%d, %d)", min, max, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

func TestSetLicenseQuantity_ManualOnly(t *testing.T) {
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
	suffix := time.Now().Format("150405.000")
	prod := &model.Product{Name: "Qty API", Slug: "qty-api-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(context.Background(), prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Seats", Slug: "qty-api-plan-" + suffix,
		LicenseType: "subscription", LicenseModel: "standard", PerSeat: true,
		MinQuantity: 1, MaxQuantity: 5, MaxSeats: 1, MaxActivations: 1,
	}
	if err := s.CreatePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	lic := &model.License{ProductID: prod.ID, PlanID: plan.ID, Email: "qty-" + suffix + "@example.com", LicenseKey: "kg_test_" + strings.ReplaceAll(suffix, ".", ""), Status: model.StatusActive, Quantity: 1}
	if err := s.CreateLicense(context.Background(), lic); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{Store: s}
	call := func(quantity int) (int, string, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/licenses/"+lic.ID+"/quantity", strings.NewReader(fmt.Sprintf(`{"quantity":%d}`, quantity)))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: lic.ID}}
		h.SetLicenseQuantity(c)
		var body struct {
			Data struct {
				Quantity int `json:"quantity"`
			} `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Error.Code, fmt.Sprint(body.Data.Quantity)
	}
	if code, _, quantity := call(3); code != http.StatusOK || quantity != "3" {
		t.Fatalf("manual quantity update = status %d quantity %s, want 200 and 3", code, quantity)
	}
	if code, _, _ := call(6); code != http.StatusBadRequest {
		t.Fatalf("out-of-range quantity status = %d, want 400", code)
	}
	lic.StripeSubscriptionID, lic.PaymentProvider = "sub_manual_test_"+strings.ReplaceAll(suffix, ".", ""), "stripe"
	if err := s.UpdateLicense(context.Background(), lic, "stripe_subscription_id", "payment_provider"); err != nil {
		t.Fatal(err)
	}
	if code, ec, _ := call(4); code != http.StatusConflict || ec != "STRIPE_MANAGED" {
		t.Fatalf("Stripe-backed quantity update = %d %q, want 409 STRIPE_MANAGED", code, ec)
	}
}

func TestCreateLicenseAcceptsAndDefaultsPerSeatQuantity(t *testing.T) {
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
	suffix := time.Now().Format("150405.000")
	prod := &model.Product{Name: "Issue Qty", Slug: "issue-qty-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(context.Background(), prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "Issue seats", Slug: "issue-seats-" + suffix, LicenseType: "subscription", LicenseModel: "standard", PerSeat: true, MinQuantity: 2, MaxQuantity: 4, MaxActivations: 2, MaxSeats: 1}
	if err := s.CreatePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{Store: s}
	issue := func(email, quantity string) (int, int, int, int) {
		body := `{"product_id":"` + prod.ID + `","plan_id":"` + plan.ID + `","email":"` + email + `"`
		if quantity != "" {
			body += `,"quantity":` + quantity
		}
		body += `}`
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/licenses", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.CreateLicense(c)
		var responseBody struct {
			Data struct {
				Quantity                int `json:"quantity"`
				EffectiveMaxActivations int `json:"effective_max_activations"`
				EffectiveMaxSeats       int `json:"effective_max_seats"`
			} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &responseBody)
		return w.Code, responseBody.Data.Quantity, responseBody.Data.EffectiveMaxActivations, responseBody.Data.EffectiveMaxSeats
	}
	if code, quantity, activations, seats := issue("default-"+suffix+"@example.com", ""); code != http.StatusCreated || quantity != 2 || activations != 4 || seats != 2 {
		t.Fatalf("omitted quantity response = status %d quantity %d limits %d/%d, want 201, 2, and 4/2", code, quantity, activations, seats)
	}
	if code, quantity, activations, seats := issue("explicit-"+suffix+"@example.com", "4"); code != http.StatusCreated || quantity != 4 || activations != 8 || seats != 4 {
		t.Fatalf("explicit quantity response = status %d quantity %d limits %d/%d, want 201, 4, and 8/4", code, quantity, activations, seats)
	}
	if code, _, _, _ := issue("outside-"+suffix+"@example.com", "5"); code != http.StatusBadRequest {
		t.Fatalf("out-of-range quantity status = %d, want 400", code)
	}
}

func TestAdminPlanAPIAcceptsAndUpdatesPerSeatFields(t *testing.T) {
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
	suffix := time.Now().Format("150405000")
	prod := &model.Product{Name: "Plan API", Slug: "plan-api-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(context.Background(), prod); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{Store: s}
	created := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(created)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/plans", strings.NewReader(fmt.Sprintf(
		`{"product_id":%q,"name":"Seats","slug":%q,"license_type":"subscription","max_seats":1,"per_seat":true,"min_quantity":2,"max_quantity":7}`,
		prod.ID, "seat-api-"+suffix)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CreatePlan(c)
	if created.Code != http.StatusCreated {
		t.Fatalf("CreatePlan status = %d, want 201: %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data model.Plan `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	plan := envelope.Data
	if !plan.PerSeat || plan.MinQuantity != 2 || plan.MaxQuantity != 7 {
		t.Fatalf("created per-seat plan = %+v", plan)
	}
	updated := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(updated)
	c.Request = httptest.NewRequest(http.MethodPut, "/admin/plans/"+plan.ID, strings.NewReader(`{"min_quantity":3}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: plan.ID}}
	h.UpdatePlan(c)
	if updated.Code != http.StatusOK {
		t.Fatalf("UpdatePlan status = %d, want 200: %s", updated.Code, updated.Body.String())
	}
	var updatedEnvelope struct {
		Data model.Plan `json:"data"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedEnvelope); err != nil {
		t.Fatal(err)
	}
	if updatedEnvelope.Data.MinQuantity != 3 || updatedEnvelope.Data.MaxQuantity != 7 || !updatedEnvelope.Data.PerSeat {
		t.Fatalf("partial quantity update lost existing plan fields: %+v", updatedEnvelope.Data)
	}
}

func TestChangeLicensePlanAdjustsManualSeatQuantity(t *testing.T) {
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
	suffix := time.Now().Format("150405.000")
	prod := &model.Product{Name: "Plan Qty", Slug: "plan-qty-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(context.Background(), prod); err != nil {
		t.Fatal(err)
	}
	plain := &model.Plan{ProductID: prod.ID, Name: "Plain", Slug: "plain-qty-" + suffix, LicenseType: "subscription", LicenseModel: "standard", MinQuantity: 1, MaxQuantity: 1}
	plainAlt := &model.Plan{ProductID: prod.ID, Name: "Plain Alt", Slug: "plain-alt-qty-" + suffix, LicenseType: "subscription", LicenseModel: "standard", MinQuantity: 1, MaxQuantity: 1}
	perSeat := &model.Plan{ProductID: prod.ID, Name: "Per Seat", Slug: "seat-qty-" + suffix, LicenseType: "subscription", LicenseModel: "standard", PerSeat: true, MinQuantity: 3, MaxQuantity: 8}
	perSeatNarrow := &model.Plan{ProductID: prod.ID, Name: "Per Seat Narrow", Slug: "seat-qty-narrow-" + suffix, LicenseType: "subscription", LicenseModel: "standard", PerSeat: true, MinQuantity: 4, MaxQuantity: 6}
	for _, p := range []*model.Plan{plain, plainAlt, perSeat, perSeatNarrow} {
		if err := s.CreatePlan(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	lic := &model.License{ProductID: prod.ID, PlanID: plain.ID, Email: "plan-qty-" + suffix + "@example.com", LicenseKey: "kg_test_plan_" + strings.ReplaceAll(suffix, ".", ""), Status: model.StatusActive, Quantity: 1}
	if err := s.CreateLicense(context.Background(), lic); err != nil {
		t.Fatal(err)
	}
	// Non-per-seat quantity is historically ignored; stale values must
	// start from one rather than carry into the new team plan.
	lic.Quantity = 6
	if err := s.UpdateLicense(context.Background(), lic, "quantity"); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{Store: s}
	move := func(planID string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/licenses/"+lic.ID+"/change-plan", strings.NewReader(`{"plan_id":"`+planID+`"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: lic.ID}}
		h.ChangeLicensePlan(c)
		return w.Code
	}
	if code := move(plainAlt.ID); code != http.StatusOK {
		t.Fatalf("legacy to legacy plan move status = %d, want 200", code)
	}
	got, err := s.FindLicenseByID(context.Background(), lic.ID)
	if err != nil || got.Quantity != 6 {
		t.Fatalf("legacy to legacy quantity = %v, err %v; legacy quantity should be untouched", got, err)
	}
	if code := move(perSeat.ID); code != http.StatusOK {
		t.Fatalf("move to per-seat plan status = %d, want 200", code)
	}
	got, err = s.FindLicenseByID(context.Background(), lic.ID)
	if err != nil || got.Quantity != 3 {
		t.Fatalf("non-per-seat to per-seat quantity = %v, err %v; stale quantity must be reset to one then clamped to 3", got, err)
	}
	if code := move(perSeatNarrow.ID); code != http.StatusOK {
		t.Fatalf("move between per-seat plans status = %d, want 200", code)
	}
	got, err = s.FindLicenseByID(context.Background(), lic.ID)
	if err != nil || got.Quantity != 4 {
		t.Fatalf("per-seat to per-seat quantity = %v, err %v; want clamped quantity 4", got, err)
	}
	if code := move(plain.ID); code != http.StatusOK {
		t.Fatalf("move to non-per-seat plan status = %d, want 200", code)
	}
	got, err = s.FindLicenseByID(context.Background(), lic.ID)
	if err != nil || got.Quantity != 1 {
		t.Fatalf("per-seat to non-per-seat quantity = %v, err %v; want 1", got, err)
	}
}

func TestUpdatePlanCannotTogglePerSeatWithExistingLicenses(t *testing.T) {
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
	suffix := time.Now().Format("150405.000")
	prod := &model.Product{Name: "Toggle Guard", Slug: "toggle-guard-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(context.Background(), prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "Seat plan", Slug: "toggle-plan-" + suffix, LicenseType: "subscription", LicenseModel: "standard", PerSeat: true, MinQuantity: 1, MaxQuantity: 5}
	if err := s.CreatePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	lic := &model.License{ProductID: prod.ID, PlanID: plan.ID, Email: "toggle-" + suffix + "@example.com", LicenseKey: "kg_toggle_" + strings.ReplaceAll(suffix, ".", ""), Status: model.StatusActive, Quantity: 1}
	if err := s.CreateLicense(context.Background(), lic); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{Store: s}
	update := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/admin/plans/"+plan.ID, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: plan.ID}}
		h.UpdatePlan(c)
		return w
	}
	for _, body := range []string{`{"per_seat":false}`, `{"name":"Renamed, same mode"}`} {
		w := update(body)
		if body == `{"per_seat":false}` {
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &envelope)
			if w.Code != http.StatusConflict || envelope.Error.Code != "HAS_LICENSES" || !strings.Contains(envelope.Error.Message, "per-seat") {
				t.Fatalf("per-seat toggle with license = %d %s; want 409 HAS_LICENSES with explanation", w.Code, w.Body.String())
			}
		} else if w.Code != http.StatusOK {
			t.Fatalf("unrelated plan edit with license = %d: %s", w.Code, w.Body.String())
		}
	}
	// Simulate fulfilment inserting a license after the cheap count but
	// before the plan write. The locked count must still refuse the toggle.
	racePlan := &model.Plan{ProductID: prod.ID, Name: "Race plan", Slug: "race-plan-" + suffix, LicenseType: "subscription", LicenseModel: "standard", MinQuantity: 1, MaxQuantity: 1}
	if err := s.CreatePlan(context.Background(), racePlan); err != nil {
		t.Fatal(err)
	}
	h.beforeCutoffWrite = func() {
		late := &model.License{ProductID: prod.ID, PlanID: racePlan.ID, Email: "late-" + suffix + "@example.com", LicenseKey: "kg_late_" + strings.ReplaceAll(suffix, ".", ""), Status: model.StatusActive, Quantity: 1}
		if err := s.CreateLicense(context.Background(), late); err != nil {
			t.Errorf("insert license during update: %v", err)
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/admin/plans/"+racePlan.ID, strings.NewReader(`{"per_seat":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: racePlan.ID}}
	h.UpdatePlan(c)
	var raceResponse struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &raceResponse)
	if w.Code != http.StatusConflict || raceResponse.Error.Code != "HAS_LICENSES" {
		t.Fatalf("concurrent license insert toggle = %d %s; want 409 HAS_LICENSES", w.Code, w.Body.String())
	}
}
