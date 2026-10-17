package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/apperr"
)

func TestSeatInvitesRespectPerSeatInvitationCap(t *testing.T) {
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
	product := &model.Product{Name: "Seat limits", Slug: "seat-limits-" + suffix, Type: model.ProductTypeSaaS}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatal(err)
	}
	wh := NewWebhookService(s, slog.Default(), time.Second, 1, false)
	seats := NewSeatService(s, wh, nil, slog.Default(), "")
	makeLicense := func(name string, perSeat bool, quantity int, maxSeats int) *model.License {
		t.Helper()
		plan := &model.Plan{
			ProductID: product.ID, Name: name, Slug: name + "-" + suffix,
			LicenseType: "subscription", LicenseModel: "standard", PerSeat: perSeat,
			MinQuantity: 1, MaxQuantity: 10, MaxSeats: maxSeats,
		}
		if err := s.CreatePlan(ctx, plan); err != nil {
			t.Fatalf("create plan %s: %v", name, err)
		}
		lic := &model.License{
			ProductID: product.ID, PlanID: plan.ID, Email: name + "-owner@example.com",
			LicenseKey: "KG-SEAT-" + name + "-" + suffix, Status: model.StatusActive, Quantity: quantity,
		}
		if err := s.CreateLicense(ctx, lic); err != nil {
			t.Fatalf("create license %s: %v", name, err)
		}
		return lic
	}
	add := func(lic *model.License, email string) error {
		t.Helper()
		_, err := seats.AddSeat(ctx, AddSeatInput{LicenseKey: lic.LicenseKey, Email: email, ProductID: product.ID})
		return err
	}
	assertLimit := func(err error) {
		t.Helper()
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "SEAT_LIMIT" || ae.Status != 409 {
			t.Fatalf("limit error = %v, want 409 SEAT_LIMIT", err)
		}
	}

	t.Run("one seat leaves no invitation slots", func(t *testing.T) {
		lic := makeLicense("one", true, 1, 1)
		ownerAccess, err := seats.CheckSeatAccess(ctx, lic.LicenseKey, lic.Email, product.ID)
		if err != nil || !ownerAccess {
			t.Fatalf("per-seat owner access = %v, %v; want true, nil", ownerAccess, err)
		}
		assertLimit(add(lic, "one-invite@example.com"))
	})
	t.Run("three seats allow exactly two invitations", func(t *testing.T) {
		lic := makeLicense("three", true, 3, 1)
		for i := 1; i <= 2; i++ {
			if err := add(lic, fmt.Sprintf("three-invite-%d@example.com", i)); err != nil {
				t.Fatalf("invite %d: %v", i, err)
			}
		}
		assertLimit(add(lic, "three-invite-3@example.com"))
	})
	t.Run("non per-seat preserves invite cap", func(t *testing.T) {
		lic := makeLicense("legacy", false, 1, 2)
		for i := 1; i <= 2; i++ {
			if err := add(lic, fmt.Sprintf("legacy-invite-%d@example.com", i)); err != nil {
				t.Fatalf("invite %d: %v", i, err)
			}
		}
		assertLimit(add(lic, "legacy-invite-3@example.com"))
	})
}

func TestSeatReinviteAtZeroInvitationCapacity(t *testing.T) {
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
	product := &model.Product{Name: "Seat reinvite", Slug: "seat-reinvite-" + suffix, Type: model.ProductTypeSaaS}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{ProductID: product.ID, Name: "Seats", Slug: "seats-" + suffix, LicenseType: "subscription", LicenseModel: "standard", PerSeat: true, MinQuantity: 1, MaxQuantity: 10, MaxSeats: 1}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	lic := &model.License{ProductID: product.ID, PlanID: plan.ID, Email: "owner-" + suffix + "@example.com", LicenseKey: "KG-REINVITE-" + suffix, Status: model.StatusActive, Quantity: 3}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatal(err)
	}
	seats := NewSeatService(s, NewWebhookService(s, slog.Default(), time.Second, 1, false), nil, slog.Default(), "")

	t.Run("pending seat rotates invitation", func(t *testing.T) {
		email := "pending-" + suffix + "@example.com"
		first, err := seats.AddSeat(ctx, AddSeatInput{LicenseKey: lic.LicenseKey, Email: email, ProductID: product.ID})
		if err != nil {
			t.Fatalf("first invite: %v", err)
		}
		lic.Quantity = 1
		if err := s.UpdateLicense(ctx, lic, "quantity"); err != nil {
			t.Fatal(err)
		}
		second, err := seats.AddSeat(ctx, AddSeatInput{LicenseKey: lic.LicenseKey, Email: email, ProductID: product.ID})
		if err != nil {
			t.Fatalf("re-invite pending seat at zero capacity: %v", err)
		}
		if second.ID != first.ID {
			t.Errorf("re-invite seat id = %q, want %q", second.ID, first.ID)
		}
		if second.InviteTokenHash == first.InviteTokenHash {
			t.Error("pending re-invite did not rotate token hash")
		}
	})

	t.Run("accepted seat remains idempotent", func(t *testing.T) {
		email := "accepted-" + suffix + "@example.com"
		now := time.Now()
		accepted := &model.Seat{LicenseID: lic.ID, Email: email, Role: "member", AcceptedAt: &now}
		if err := s.CreateSeat(ctx, accepted); err != nil {
			t.Fatal(err)
		}
		_, err := seats.AddSeat(ctx, AddSeatInput{LicenseKey: lic.LicenseKey, Email: email, ProductID: product.ID})
		if err != nil {
			t.Fatalf("re-invite accepted seat at zero capacity: %v", err)
		}
	})
}
