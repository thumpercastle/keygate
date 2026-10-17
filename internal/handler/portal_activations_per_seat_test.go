package handler

import (
	"testing"

	"github.com/tabloy/keygate/internal/model"
)

func TestMaxActivationsForLicenseUsesEffectiveLimit(t *testing.T) {
	tests := []struct {
		name string
		lic  *model.License
		want int
	}{
		{"per-seat quantity", &model.License{Quantity: 3, Plan: &model.Plan{PerSeat: true, MaxActivations: 2}}, 6},
		{"non per-seat unchanged", &model.License{Quantity: 3, Plan: &model.Plan{MaxActivations: 2}}, 2},
		{"unlimited", &model.License{Quantity: 3, Plan: &model.Plan{PerSeat: true}}, 0},
		{"nil plan retains portal zero", &model.License{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxActivationsForLicense(tt.lic); got != tt.want {
				t.Errorf("maxActivationsForLicense() = %d, want %d", got, tt.want)
			}
		})
	}
}
