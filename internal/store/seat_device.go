package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/tabloy/keygate/internal/model"
)

// Seat-bound desktop sign-in.
//
// A desktop app asks for a sign-in link for an address (a request row);
// the person clicks it and approves (the activation is created or
// renewed); the app's next poll collects a device secret (the request is
// consumed). From then on the device verifies with that secret, which
// is replaced on every verify.

// DesktopSigninTTL is how long an emailed sign-in link stays usable.
const DesktopSigninTTL = 15 * time.Minute

// DeviceSecretGrace is how long the previous device secret is still
// accepted after a rotation: long enough for a client whose verify
// response was lost to retry with the secret it still holds. After it,
// presenting the previous secret means two copies of the device exist.
const DeviceSecretGrace = 10 * time.Minute

// MoveWindow is the rolling window the per-seat move limit counts in.
const MoveWindow = 30 * 24 * time.Hour

var (
	// ErrSigninNotPending: the request is not waiting for approval
	// (unknown, expired, already approved, refused or consumed).
	ErrSigninNotPending = errors.New("sign-in request is not pending")
	// ErrSigninNotApproved: a poll tried to collect a request that is
	// not (or no longer) approved.
	ErrSigninNotApproved = errors.New("sign-in request is not approved")
	// ErrSeatGone: the seat (or the owner's claim on the licence)
	// stopped existing between resolution and approval.
	ErrSeatGone = errors.New("seat no longer exists")
	// ErrDeviceSecretRace: a concurrent verify rotated the secret first.
	ErrDeviceSecretRace = errors.New("device secret already rotated")
)

// NewOpaqueSecret returns 32 random bytes as hex and its SHA-256 hash.
func NewOpaqueSecret() (plain, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	plain = hex.EncodeToString(b)
	return plain, HashOpaqueSecret(plain), nil
}

// HashOpaqueSecret is the lookup key for a link token, poll secret or
// device secret. Only hashes are stored.
func HashOpaqueSecret(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

// ─── Requests ───

func (s *Store) CreateDesktopSignin(ctx context.Context, r *model.DesktopSigninRequest) error {
	if r.ID == "" {
		r.ID = newID()
	}
	r.Email = normalizeEmail(r.Email)
	_, err := s.DB.NewInsert().Model(r).Exec(ctx)
	return err
}

// CountRecentDesktopSignins counts sign-in requests for an address in
// the last ten minutes. Every request leaves a row, eligible or not, so
// the count cannot tell a seat holder from a stranger.
func (s *Store) CountRecentDesktopSignins(ctx context.Context, email string) (int, error) {
	return s.DB.NewSelect().Model((*model.DesktopSigninRequest)(nil)).
		Where("email = ? AND created_at > now() - interval '10 minutes'", normalizeEmail(email)).
		Count(ctx)
}

func (s *Store) FindDesktopSigninByID(ctx context.Context, id string) (*model.DesktopSigninRequest, error) {
	r := new(model.DesktopSigninRequest)
	return r, s.DB.NewSelect().Model(r).Where("id = ?", id).Scan(ctx)
}

func (s *Store) FindDesktopSigninByLinkToken(ctx context.Context, plainToken string) (*model.DesktopSigninRequest, error) {
	r := new(model.DesktopSigninRequest)
	return r, s.DB.NewSelect().Model(r).
		Where("link_token_hash = ?", HashOpaqueSecret(plainToken)).Scan(ctx)
}

// RefuseDesktopSignin closes a pending request with a reason the app's
// poll and the approval page both report.
func (s *Store) RefuseDesktopSignin(ctx context.Context, id, code string, nextMoveAt *time.Time) error {
	_, err := s.DB.NewUpdate().Model((*model.DesktopSigninRequest)(nil)).
		Set("status = ?", model.SigninRefused).
		Set("refuse_code = ?", code).
		Set("next_move_at = ?", nextMoveAt).
		Where("id = ? AND status = ?", id, model.SigninPending).
		Exec(ctx)
	return err
}

// CleanExpiredDesktopSignins drops requests a day past their expiry.
// Their rows are only kept that long so the per-address rate limit and
// a late poll still see them.
func (s *Store) CleanExpiredDesktopSignins(ctx context.Context) {
	_, _ = s.DB.NewDelete().Model((*model.DesktopSigninRequest)(nil)).
		Where("expires_at < now() - interval '1 day'").
		Exec(ctx)
}

// ─── Seat holders ───

// SeatHolding is one licence an address may sign a device in on: as
// its owner (Seat nil) or through a live seat.
type SeatHolding struct {
	License *model.License
	Seat    *model.Seat
}

// FindSeatHoldings lists the licences of a product an address can sign
// in on, owner holdings first, then seats, each oldest first. Licences
// come with plan and product loaded; lifecycle is the caller's check.
func (s *Store) FindSeatHoldings(ctx context.Context, productID, email string) ([]SeatHolding, error) {
	e := normalizeEmail(email)
	var owned []*model.License
	if err := s.DB.NewSelect().Model(&owned).
		Relation("Plan").Relation("Plan.Entitlements").Relation("Product").
		Where("license.product_id = ?", productID).
		Where("lower(license.email) = ?", e).
		Where("license.owner_seat_released = false").
		OrderExpr("license.created_at ASC, license.id ASC").
		Scan(ctx); err != nil {
		return nil, err
	}
	var seats []*model.Seat
	if err := s.DB.NewSelect().Model(&seats).
		Relation("License").Relation("License.Plan").Relation("License.Plan.Entitlements").
		Relation("License.Product").
		Where("lower(seat.email) = ?", e).
		Where("seat.removed_at IS NULL").
		Where("license.product_id = ?", productID).
		OrderExpr("seat.created_at ASC, seat.id ASC").
		Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]SeatHolding, 0, len(owned)+len(seats))
	for _, l := range owned {
		out = append(out, SeatHolding{License: l})
	}
	for _, st := range seats {
		lic := st.License
		st.License = nil
		out = append(out, SeatHolding{License: lic, Seat: st})
	}
	return out, nil
}

// SeatHolderLive reports whether email still holds a seat on the
// licence: as its owner (not released) or through a live seat. The
// seat id is empty for the owner.
func (s *Store) SeatHolderLive(ctx context.Context, lic *model.License, email string) (seatID string, ok bool, err error) {
	e := normalizeEmail(email)
	if normalizeEmail(lic.Email) == e && !lic.OwnerSeatReleased {
		return "", true, nil
	}
	seat := new(model.Seat)
	err = s.DB.NewSelect().Model(seat).
		Where("license_id = ? AND lower(email) = ? AND removed_at IS NULL", lic.ID, e).
		Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return seat.ID, true, nil
}

// ─── Approval ───

// ApproveParams is everything approval decides with. Policy comes from
// the licence's plan, read by the caller.
type ApproveParams struct {
	LinkToken string
	LicenseID string
	SeatID    string // empty: the owner's own seat
	Policy    model.SeatDevicePolicy
	IPAddress string
	Now       time.Time
}

// ApproveOutcome reports what approval did.
type ApproveOutcome struct {
	Request *model.DesktopSigninRequest
	// Refused with RefuseCode set when the move limit stopped it;
	// NextMoveAt is when a move frees up (nil: never, the plan allows
	// none).
	Refused    bool
	RefuseCode string
	NextMoveAt *time.Time
	// Activation is the seat-bound activation the device now holds.
	Activation *model.Activation
	// Renewed: the device already held the seat and only its sign-in
	// was refreshed. Moved: this counted against the move limit.
	Renewed bool
	Moved   bool
	// Removed are activations this approval took away: the seat's
	// oldest devices beyond its cap, and whatever else held this
	// device's identifier on the licence.
	Removed []*model.Activation
	// SeatAccepted: approving accepted a pending seat invitation.
	SeatAccepted bool
}

// RefuseMoveLimit is the refusal code when a seat has used its moves.
const RefuseMoveLimit = "DEVICE_TRANSFER_LIMIT"

// ApproveDesktopSignin turns a pending request into a seat-bound
// activation, all under the licence row lock:
//
//   - the request must still be pending and unexpired;
//   - the seat must still exist (or the owner still hold theirs); a
//     pending seat invitation is accepted, because clicking a link
//     mailed to the address proves the address;
//   - a device that already holds this seat has its sign-in renewed —
//     never a move;
//   - a device new to the seat is a move when the seat's last new
//     device was a different one; moves are capped per rolling 30 days
//     and a refusal is recorded on the request;
//   - the seat's oldest devices are signed out to stay within
//     DevicesPerSeat, and anything else holding this identifier on the
//     licence (a licence-key activation from before the switch, or a
//     colleague's seat on a shared machine) is replaced.
func (s *Store) ApproveDesktopSignin(ctx context.Context, p ApproveParams) (*ApproveOutcome, error) {
	out := &ApproveOutcome{}
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		req := new(model.DesktopSigninRequest)
		if err := tx.NewSelect().Model(req).
			Where("link_token_hash = ?", HashOpaqueSecret(p.LinkToken)).
			For("UPDATE").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSigninNotPending
			}
			return err
		}
		out.Request = req
		if req.Status != model.SigninPending || !p.Now.Before(req.ExpiresAt) {
			return ErrSigninNotPending
		}

		lic := new(model.License)
		if err := tx.NewSelect().Model(lic).Where("id = ?", p.LicenseID).
			For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		email := req.Email

		// Re-check the holding under the lock.
		if p.SeatID == "" {
			if normalizeEmail(lic.Email) != email || lic.OwnerSeatReleased {
				return ErrSeatGone
			}
		} else {
			seat := new(model.Seat)
			if err := tx.NewSelect().Model(seat).Where("id = ?", p.SeatID).
				For("UPDATE").Scan(ctx); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrSeatGone
				}
				return err
			}
			if seat.RemovedAt != nil || seat.LicenseID != lic.ID || normalizeEmail(seat.Email) != email {
				return ErrSeatGone
			}
			if seat.AcceptedAt == nil {
				if err := acceptSeatIn(ctx, tx, seat, p.Now); err != nil {
					return err
				}
				out.SeatAccepted = true
			}
		}

		var held []*model.Activation
		if err := tx.NewSelect().Model(&held).
			Where("license_id = ? AND seat_email = ?", lic.ID, email).
			OrderExpr("COALESCE(signed_in_at, created_at) ASC, id ASC").
			Scan(ctx); err != nil {
			return err
		}

		for _, a := range held {
			if a.Identifier != req.Identifier {
				continue
			}
			// The device already holds this seat: renew its sign-in.
			// Its secrets are cleared; the poll issues a fresh one.
			if _, err := tx.NewUpdate().Model((*model.Activation)(nil)).
				Set("signed_in_at = ?", p.Now).
				Set("fingerprint = ?", req.Fingerprint).
				Set("label = ?", req.Label).
				Set("ip_address = ?", p.IPAddress).
				Set("last_verified = ?", p.Now).
				Set("device_secret_hash = NULL").
				Set("prev_device_secret_hash = NULL").
				Set("secret_rotated_at = NULL").
				Where("id = ?", a.ID).Exec(ctx); err != nil {
				return err
			}
			a.SignedInAt = &p.Now
			out.Activation = a
			out.Renewed = true
			return markApproved(ctx, tx, req, lic.ID, a.ID, p.Now)
		}

		// A device new to this seat. Is it a move?
		var lastIdentifier string
		err := tx.NewRaw(`SELECT identifier FROM seat_device_events
			WHERE license_id = ? AND email = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
			lic.ID, email).Scan(ctx, &lastIdentifier)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		isMove := lastIdentifier != "" && lastIdentifier != req.Identifier
		if isMove {
			windowStart := p.Now.Add(-MoveWindow)
			var moves []time.Time
			if err := tx.NewRaw(`SELECT created_at FROM seat_device_events
				WHERE license_id = ? AND email = ? AND is_move AND created_at > ?
				ORDER BY created_at ASC`, lic.ID, email, windowStart).Scan(ctx, &moves); err != nil {
				return err
			}
			if len(moves) >= p.Policy.MovesPer30d {
				out.Refused = true
				out.RefuseCode = RefuseMoveLimit
				if p.Policy.MovesPer30d > 0 {
					// The move that has to age out for one to free up.
					t := moves[len(moves)-p.Policy.MovesPer30d].Add(MoveWindow)
					out.NextMoveAt = &t
				}
				if _, err := tx.NewUpdate().Model((*model.DesktopSigninRequest)(nil)).
					Set("status = ?", model.SigninRefused).
					Set("refuse_code = ?", RefuseMoveLimit).
					Set("next_move_at = ?", out.NextMoveAt).
					Set("license_id = ?", lic.ID).
					Where("id = ?", req.ID).Exec(ctx); err != nil {
					return err
				}
				req.Status = model.SigninRefused
				req.RefuseCode = RefuseMoveLimit
				req.NextMoveAt = out.NextMoveAt
				req.LicenseID = lic.ID
				return nil
			}
		}

		// Sign the seat's oldest devices out to make room.
		excess := len(held) - p.Policy.DevicesPerSeat + 1
		for i := 0; i < excess && i < len(held); i++ {
			out.Removed = append(out.Removed, held[i])
		}
		// Anything else holding this identifier on the licence.
		other := new(model.Activation)
		err = tx.NewSelect().Model(other).
			Where("license_id = ? AND identifier = ?", lic.ID, req.Identifier).Scan(ctx)
		switch {
		case err == nil:
			out.Removed = append(out.Removed, other)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		for _, a := range out.Removed {
			if _, err := tx.NewDelete().Model((*model.Activation)(nil)).
				Where("id = ?", a.ID).Exec(ctx); err != nil {
				return err
			}
		}

		if _, err := tx.NewInsert().Model(&model.SeatDeviceEvent{
			ID: newID(), LicenseID: lic.ID, Email: email,
			Identifier: req.Identifier, IsMove: isMove, CreatedAt: p.Now,
		}).Exec(ctx); err != nil {
			return err
		}
		act := &model.Activation{
			ID: newID(), LicenseID: lic.ID, Identifier: req.Identifier,
			IdentifierType: "device", Label: req.Label, IPAddress: p.IPAddress,
			SeatEmail: email, Fingerprint: req.Fingerprint,
			SignedInAt: &p.Now, LastVerified: p.Now, CreatedAt: p.Now,
		}
		if _, err := tx.NewInsert().Model(act).Exec(ctx); err != nil {
			return err
		}
		out.Activation = act
		out.Moved = isMove
		return markApproved(ctx, tx, req, lic.ID, act.ID, p.Now)
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

func markApproved(ctx context.Context, tx bun.Tx, req *model.DesktopSigninRequest, licenseID, activationID string, now time.Time) error {
	_, err := tx.NewUpdate().Model((*model.DesktopSigninRequest)(nil)).
		Set("status = ?", model.SigninApproved).
		Set("license_id = ?", licenseID).
		Set("activation_id = ?", activationID).
		Set("approved_at = ?", now).
		Where("id = ?", req.ID).Exec(ctx)
	req.Status = model.SigninApproved
	req.LicenseID = licenseID
	req.ActivationID = activationID
	req.ApprovedAt = &now
	return err
}

// acceptSeatIn accepts a pending seat inside the caller's transaction:
// the same binding AcceptSeatInvite makes, keyed off the seat's email.
func acceptSeatIn(ctx context.Context, tx bun.Tx, seat *model.Seat, now time.Time) error {
	user := new(model.User)
	err := tx.NewSelect().Model(user).Where("email = ?", normalizeEmail(seat.Email)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		user.ID = newID()
		user.Email = normalizeEmail(seat.Email)
		user.Role = model.RoleUser
		if _, err := tx.NewInsert().Model(user).Exec(ctx); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := tx.NewUpdate().Model((*model.Seat)(nil)).
		Set("user_id = ?", user.ID).
		Set("accepted_at = ?", now).
		Set("invite_token_hash = NULL").
		Set("invite_expires_at = NULL").
		Where("id = ?", seat.ID).Exec(ctx); err != nil {
		return err
	}
	seat.UserID = user.ID
	seat.AcceptedAt = &now
	return nil
}

// ConsumeDesktopSignin collects an approved request exactly once: the
// request becomes consumed and its activation gets the device secret
// whose hash is passed in.
func (s *Store) ConsumeDesktopSignin(ctx context.Context, requestID, deviceSecretHash string, now time.Time) (*model.Activation, error) {
	var act *model.Activation
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		req := new(model.DesktopSigninRequest)
		if err := tx.NewSelect().Model(req).Where("id = ?", requestID).
			For("UPDATE").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSigninNotApproved
			}
			return err
		}
		if req.Status != model.SigninApproved || req.ActivationID == "" {
			return ErrSigninNotApproved
		}
		if _, err := tx.NewUpdate().Model((*model.DesktopSigninRequest)(nil)).
			Set("status = ?", model.SigninConsumed).
			Where("id = ?", req.ID).Exec(ctx); err != nil {
			return err
		}
		res, err := tx.NewUpdate().Model((*model.Activation)(nil)).
			Set("device_secret_hash = ?", deviceSecretHash).
			Set("prev_device_secret_hash = NULL").
			Set("secret_rotated_at = ?", now).
			Where("id = ?", req.ActivationID).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			// Signed out (seat removed, replaced) between approval
			// and collection: nothing left to hand over.
			return ErrSeatGone
		}
		act = new(model.Activation)
		return tx.NewSelect().Model(act).Where("id = ?", req.ActivationID).Scan(ctx)
	})
	return act, err
}

// ─── Device secrets ───

// FindActivationByDeviceSecret finds the seat-bound activation a
// device secret belongs to. viaPrevious reports a match on the secret
// that was current before the last rotation.
func (s *Store) FindActivationByDeviceSecret(ctx context.Context, plainSecret string) (act *model.Activation, viaPrevious bool, err error) {
	h := HashOpaqueSecret(plainSecret)
	act = new(model.Activation)
	err = s.DB.NewSelect().Model(act).
		Where("device_secret_hash = ? OR prev_device_secret_hash = ?", h, h).
		Where("seat_email IS NOT NULL").
		Limit(1).Scan(ctx)
	if err != nil {
		return nil, false, err
	}
	return act, act.DeviceSecretHash != h, nil
}

// RotateDeviceSecret replaces the device secret. presentedHash must be
// the current secret (it becomes the previous one) or the previous one
// (a retry after a lost response: the previous one stays). Returns
// ErrDeviceSecretRace when a concurrent call already rotated past it.
func (s *Store) RotateDeviceSecret(ctx context.Context, activationID, presentedHash, newHash string, now time.Time) error {
	res, err := s.DB.NewUpdate().Model((*model.Activation)(nil)).
		Set(`prev_device_secret_hash = CASE WHEN device_secret_hash = ?
			THEN device_secret_hash ELSE prev_device_secret_hash END`, presentedHash).
		Set("device_secret_hash = ?", newHash).
		Set("secret_rotated_at = ?", now).
		Set("last_verified = ?", now).
		Where("id = ?", activationID).
		Where("device_secret_hash = ? OR prev_device_secret_hash = ?", presentedHash, presentedHash).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDeviceSecretRace
	}
	return nil
}

// ─── Seat removal ───

// RemoveSeatAndDevices removes a seat and signs out every device it
// holds, in one transaction. Returns the activations it deleted.
func (s *Store) RemoveSeatAndDevices(ctx context.Context, seatID string) ([]*model.Activation, error) {
	var removed []*model.Activation
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		seat := new(model.Seat)
		if err := tx.NewSelect().Model(seat).Where("id = ?", seatID).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if _, err := tx.NewUpdate().Model((*model.Seat)(nil)).
			Set("removed_at = ?", time.Now()).
			Where("id = ? AND removed_at IS NULL", seatID).Exec(ctx); err != nil {
			return err
		}
		return tx.NewDelete().Model(&removed).
			Where("license_id = ? AND seat_email = ?", seat.LicenseID, normalizeEmail(seat.Email)).
			Returning("*").
			Scan(ctx)
	})
	return removed, err
}

// ReleaseOwnerSeat sets whether the licence owner gives up their seat.
// Releasing it signs out the owner's devices; the owner's address no
// longer holds a seat from then on.
func (s *Store) SetOwnerSeatReleased(ctx context.Context, lic *model.License, released bool) ([]*model.Activation, error) {
	var removed []*model.Activation
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*model.License)(nil)).
			Set("owner_seat_released = ?", released).
			Where("id = ?", lic.ID).Exec(ctx); err != nil {
			return err
		}
		if !released {
			return nil
		}
		return tx.NewDelete().Model(&removed).
			Where("license_id = ? AND seat_email = ?", lic.ID, normalizeEmail(lic.Email)).
			Returning("*").
			Scan(ctx)
	})
	if err == nil {
		lic.OwnerSeatReleased = released
	}
	return removed, err
}
