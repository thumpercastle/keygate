package store

import (
	"testing"

	"github.com/tabloy/keygate/internal/model"
)

func TestFillPlanIDsDefaultsQuantityBounds(t *testing.T) {
	s := &Store{}
	plan := &model.Plan{}
	s.FillPlanIDs(plan)
	if plan.MinQuantity != 1 || plan.MaxQuantity != 1 {
		t.Fatalf("quantity bounds = [%d, %d], want [1, 1]", plan.MinQuantity, plan.MaxQuantity)
	}
}

func TestFillPlanIDsPreservesConfiguredQuantityBounds(t *testing.T) {
	s := &Store{}
	plan := &model.Plan{MinQuantity: 2, MaxQuantity: 25}
	s.FillPlanIDs(plan)
	if plan.MinQuantity != 2 || plan.MaxQuantity != 25 {
		t.Fatalf("quantity bounds = [%d, %d], want [2, 25]", plan.MinQuantity, plan.MaxQuantity)
	}
}

func TestPrepareLicenseForInsertDefaultsQuantityButPreservesInvalidValue(t *testing.T) {
	s := &Store{}
	legacy := &model.License{}
	if err := s.prepareLicenseForInsert(legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Quantity != 1 {
		t.Errorf("zero quantity normalized to %d, want 1", legacy.Quantity)
	}

	invalid := &model.License{Quantity: -2}
	if err := s.prepareLicenseForInsert(invalid); err != nil {
		t.Fatal(err)
	}
	if invalid.Quantity != -2 {
		t.Errorf("negative quantity normalized to %d, want preserved for validation", invalid.Quantity)
	}
}
