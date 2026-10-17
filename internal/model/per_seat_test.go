package model

import (
	"encoding/json"
	"testing"
)

func TestLicenseEffectiveLimits(t *testing.T) {
	tests := []struct {
		name        string
		license     *License
		activations Limit
		seats       Limit
		invitations Limit
	}{
		{
			name:        "non per-seat ignores quantity",
			license:     &License{Quantity: 5, Plan: &Plan{MaxActivations: 2, MaxSeats: 3}},
			activations: Limit{Value: 2}, seats: Limit{Value: 3}, invitations: Limit{Value: 3},
		},
		{
			name:        "per-seat quantity one reserves owner seat",
			license:     &License{Quantity: 1, Plan: &Plan{PerSeat: true, MaxActivations: 2, MaxSeats: 1}},
			activations: Limit{Value: 2}, seats: Limit{Value: 1}, invitations: Limit{Value: 0},
		},
		{
			name:        "per-seat multiplies activation and seat caps",
			license:     &License{Quantity: 5, Plan: &Plan{PerSeat: true, MaxActivations: 2, MaxSeats: 3}},
			activations: Limit{Value: 10}, seats: Limit{Value: 15}, invitations: Limit{Value: 14},
		},
		{
			name:        "large effective caps saturate instead of wrapping",
			license:     &License{Quantity: 2, Plan: &Plan{PerSeat: true, MaxActivations: int(^uint(0) >> 1)}},
			activations: Limit{Value: int(^uint(0) >> 1)}, seats: Limit{Unlimited: true}, invitations: Limit{Unlimited: true},
		},
		{
			name:        "unlimited plan caps stay unlimited",
			license:     &License{Quantity: 5, Plan: &Plan{PerSeat: true}},
			activations: Limit{Unlimited: true}, seats: Limit{Unlimited: true}, invitations: Limit{Unlimited: true},
		},
		{
			name:        "nil plan preserves caller defaults",
			license:     &License{},
			activations: Limit{Value: 3}, seats: Limit{Unlimited: true}, invitations: Limit{Unlimited: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.license.EffectiveLimits()
			if got.Activations != tt.activations {
				t.Errorf("activations = %+v, want %+v", got.Activations, tt.activations)
			}
			if got.Seats != tt.seats {
				t.Errorf("seats = %+v, want %+v", got.Seats, tt.seats)
			}
			if got.Invitations != tt.invitations {
				t.Errorf("invitations = %+v, want %+v", got.Invitations, tt.invitations)
			}
		})
	}
}

func TestLicenseJSONIncludesEffectiveLimits(t *testing.T) {
	lic := &License{
		Quantity:   4,
		LicenseKey: "secret-license-key",
		Plan:       &Plan{PerSeat: true, MaxActivations: 2, MaxSeats: 1},
	}
	var payload map[string]any
	raw, err := json.Marshal(lic)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{
		"quantity":                  4,
		"effective_max_activations": 8,
		"effective_max_seats":       4,
	} {
		if got := payload[key]; got != want {
			t.Errorf("%s = %v, want %v; json=%s", key, got, want, raw)
		}
	}
	if _, exists := payload["license_key"]; exists {
		t.Errorf("license key leaked in JSON: %s", raw)
	}
}

func TestLicenseJSONOmitsEffectiveLimitsWithoutPlan(t *testing.T) {
	lic := &License{Quantity: 1}
	raw, err := json.Marshal(lic)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"effective_max_activations", "effective_max_seats"} {
		if _, exists := payload[key]; exists {
			t.Errorf("%s should be omitted without a loaded plan: %s", key, raw)
		}
	}
}

func TestPlanClampQuantity(t *testing.T) {
	plan := &Plan{PerSeat: true, MinQuantity: 2, MaxQuantity: 8}
	for _, tt := range []struct{ input, want int }{
		{0, 2}, {1, 2}, {2, 2}, {5, 5}, {8, 8}, {9, 8}, {50, 8},
	} {
		if got := plan.ClampQuantity(tt.input); got != tt.want {
			t.Errorf("ClampQuantity(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
	if got := (&Plan{}).ClampQuantity(9); got != 1 {
		t.Errorf("non-per-seat ClampQuantity(9) = %d, want 1", got)
	}
}
