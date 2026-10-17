package handler

import "testing"

func TestSeatDevicePolicyBounds(t *testing.T) {
	ptr := func(v int) *int { return &v }
	p := seatDevicePolicy{Devices: 1, Moves: 2, Reauth: 180}
	if err := p.validate(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	for _, bad := range []struct {
		name                   string
		devices, moves, reauth *int
	}{
		{"zero devices", ptr(0), nil, nil},
		{"too many devices", ptr(101), nil, nil},
		{"negative moves", nil, ptr(-1), nil},
		{"negative reauth", nil, nil, ptr(-1)},
		{"reauth over ten years", nil, nil, ptr(3651)},
	} {
		q := p
		q.apply(bad.devices, bad.moves, bad.reauth)
		if err := q.validate(); err == nil {
			t.Errorf("%s: accepted %+v", bad.name, q)
		}
	}
	q := p
	q.apply(nil, ptr(0), ptr(0))
	if err := q.validate(); err != nil || q.Moves != 0 || q.Reauth != 0 || q.Devices != 1 {
		t.Fatalf("no moves / never re-sign-in rejected or misapplied: %+v %v", q, err)
	}
}
