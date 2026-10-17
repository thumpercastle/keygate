package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/apperr"
)

func TestPerSeatActivationLimitIncludesQuantityAndIsAtomic(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_DATABASE_URL: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	product := &model.Product{Name: "Activation limits", Slug: "activation-limits-" + suffix, Type: model.ProductTypeDesktop}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{
		ProductID: product.ID, Name: "Per-seat", Slug: "activation-plan-" + suffix,
		LicenseType: "subscription", LicenseModel: "standard", PerSeat: true,
		MinQuantity: 1, MaxQuantity: 10, MaxActivations: 1,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	lic := &model.License{
		ProductID: product.ID, PlanID: plan.ID, Email: "activation-" + suffix + "@example.com",
		LicenseKey: "KG-ACTIVATION-" + suffix, Status: model.StatusActive, Quantity: 3,
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := NewLicenseService(s, privateKey, slog.Default(), nil, nil)
	activate := func(identifier string) error {
		_, err := service.Activate(ctx, ActivateInput{LicenseKey: lic.LicenseKey, Identifier: identifier, ProductID: product.ID})
		return err
	}

	t.Run("three devices allowed and fourth refused", func(t *testing.T) {
		for i := 1; i <= 3; i++ {
			if err := activate(fmt.Sprintf("device-%d", i)); err != nil {
				t.Fatalf("activation %d: %v", i, err)
			}
		}
		var ae *apperr.AppError
		if err := activate("device-4"); !errors.As(err, &ae) || ae.Code != "ACTIVATION_LIMIT" || ae.Status != 409 {
			t.Fatalf("fourth activation error = %v, want 409 ACTIVATION_LIMIT", err)
		}
	})

	// A second license isolates the simultaneous contest from the sequential
	// assertions above while exercising the service's atomic store path.
	parallel := &model.License{
		ProductID: product.ID, PlanID: plan.ID, Email: "parallel-" + suffix + "@example.com",
		LicenseKey: "KG-PARALLEL-" + suffix, Status: model.StatusActive, Quantity: 3,
	}
	if err := s.CreateLicense(ctx, parallel); err != nil {
		t.Fatalf("create parallel license: %v", err)
	}
	const contenders = 8
	start := make(chan struct{})
	errs := make(chan error, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := service.Activate(ctx, ActivateInput{LicenseKey: parallel.LicenseKey, Identifier: fmt.Sprintf("parallel-%d", n), ProductID: product.ID})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
			continue
		}
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "ACTIVATION_LIMIT" || ae.Status != 409 {
			t.Fatalf("concurrent activation returned unexpected error: %v", err)
		}
	}
	if accepted != 3 {
		t.Fatalf("simultaneous distinct activations accepted %d, want 3", accepted)
	}
	active, err := s.CountActivations(ctx, parallel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active != 3 {
		t.Fatalf("parallel active devices = %d, want 3", active)
	}
}
