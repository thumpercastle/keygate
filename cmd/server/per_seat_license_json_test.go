package main

import (
	"encoding/json"
	"testing"

	"github.com/tabloy/keygate/internal/model"
)

func TestPortalLicenseJSONKeepsCredentialAndEffectiveLimits(t *testing.T) {
	body, err := json.Marshal(portalLicense{
		License:    &model.License{ID: "lic_test", Quantity: 2, Plan: &model.Plan{PerSeat: true, MaxActivations: 2, MaxSeats: 1}},
		LicenseKey: "kg_test_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"license_key", "quantity", "effective_max_activations", "effective_max_seats"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("portal license omitted %q: %s", name, body)
		}
	}
	var activations, seats int
	_ = json.Unmarshal(fields["effective_max_activations"], &activations)
	_ = json.Unmarshal(fields["effective_max_seats"], &seats)
	if activations != 4 || seats != 2 {
		t.Fatalf("portal license effective limits = %d/%d, want 4/2: %s", activations, seats, body)
	}
}

func TestPortalPlanViewIncludesPerSeatFields(t *testing.T) {
	view := portalPlanView(&model.Plan{PerSeat: true, MinQuantity: 2, MaxQuantity: 9})
	if view["per_seat"] != true || view["min_quantity"] != 2 || view["max_quantity"] != 9 {
		t.Fatalf("portal plan view omitted per-seat settings: %#v", view)
	}
}
