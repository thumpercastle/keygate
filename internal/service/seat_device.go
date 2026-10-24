package service

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/tabloy/keygate/internal/middleware"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/apperr"
)

// SeatDeviceService signs desktop devices in to seats with an emailed
// link instead of a licence key.
//
//  1. Start: the app sends an address and its device identity, and gets
//     a request id and poll secret. A link is mailed only when the
//     address holds a seat on a live licence of the product; the answer
//     is the same either way.
//  2. Approve: the person opens the link and approves (a POST — mail
//     scanners fetch links but do not submit forms). The device becomes
//     the seat's device; see store.ApproveDesktopSignin for the rules.
//  3. Poll: the app collects its device secret and first token, once.
//  4. Verify: the app trades its device secret for a new one and a
//     fresh token. Any failure is the same 404 as a dead licence, except
//     a sign-in past its reauth interval, which asks for a new sign-in.
type SeatDeviceService struct {
	store    *store.Store
	licenses *LicenseService
	email    *EmailService
	webhook  *WebhookService
	failures FailureTracker
	logger   *slog.Logger
	baseURL  string
	now      func() time.Time
	// linkSent, when set, receives every link that would be mailed.
	// Tests use it to follow the link; production leaves it nil.
	linkSent func(to, link string)
}

func NewSeatDeviceService(s *store.Store, licenses *LicenseService, em *EmailService, wh *WebhookService, failures FailureTracker, logger *slog.Logger, baseURL string) *SeatDeviceService {
	return &SeatDeviceService{
		store: s, licenses: licenses, email: em, webhook: wh, failures: failures,
		logger: logger, baseURL: strings.TrimRight(baseURL, "/"), now: time.Now,
	}
}

// PollInterval is the delay the app is told to leave between polls.
const PollInterval = 3

const maxSigninRequestsPer10Min = 3

// ─── Start ───

type SigninStartInput struct {
	Email       string
	ProductID   string
	Identifier  string
	Label       string
	Fingerprint string
	IPAddress   string
}

type SigninStartResult struct {
	RequestID  string `json:"request_id"`
	PollSecret string `json:"poll_secret"`
	Interval   int    `json:"interval"`
	ExpiresIn  int    `json:"expires_in"`
}

func (s *SeatDeviceService) Start(ctx context.Context, in SigninStartInput) (*SigninStartResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if appErr := apperr.ValidateEmail(email); appErr != nil {
		return nil, appErr
	}
	if err := validateDeviceFields(in.ProductID, in.Identifier, in.Label, in.Fingerprint); err != nil {
		return nil, err
	}

	count, err := s.store.CountRecentDesktopSignins(ctx, email)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if count >= maxSigninRequestsPer10Min {
		return nil, apperr.New(429, "RATE_LIMITED", "too many sign-in requests, try again later")
	}

	holding, err := s.resolveHolding(ctx, in.ProductID, email)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	linkToken, linkHash, err := store.NewOpaqueSecret()
	if err != nil {
		return nil, apperr.Internal(err)
	}
	pollSecret, pollHash, err := store.NewOpaqueSecret()
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if holding == nil {
		// Store a link nobody was sent. The row has to exist either
		// way: the rate limit above counts rows, so skipping it would
		// let the limiter tell a seat holder from a stranger.
		_, linkHash, err = store.NewOpaqueSecret()
		if err != nil {
			return nil, apperr.Internal(err)
		}
	}
	req := &model.DesktopSigninRequest{
		ProductID: in.ProductID, Email: email,
		Identifier: in.Identifier, Label: strings.TrimSpace(in.Label), Fingerprint: in.Fingerprint,
		LinkTokenHash: linkHash, PollSecretHash: pollHash,
		Status: model.SigninPending, CreatedIP: in.IPAddress,
		ExpiresAt: s.now().Add(store.DesktopSigninTTL),
	}
	if err := s.store.CreateDesktopSignin(ctx, req); err != nil {
		return nil, apperr.Internal(err)
	}

	if holding == nil {
		s.logger.Warn("desktop sign-in requested for an address with no seat",
			"email", email, "product_id", in.ProductID, "ip", in.IPAddress)
	} else {
		productName := ""
		if holding.License.Product != nil {
			productName = holding.License.Product.Name
		}
		s.sendSigninLink(email, productName, req.Label, s.signinURL(linkToken))
	}

	return &SigninStartResult{
		RequestID: req.ID, PollSecret: pollSecret,
		Interval: PollInterval, ExpiresIn: int(store.DesktopSigninTTL / time.Second),
	}, nil
}

func validateDeviceFields(productID, identifier, label, fingerprint string) error {
	switch {
	case strings.TrimSpace(productID) == "":
		return apperr.BadRequest("product_id is required")
	case strings.TrimSpace(identifier) == "":
		return apperr.BadRequest("identifier is required")
	case len(identifier) > 256:
		return apperr.BadRequest("identifier must be at most 256 characters")
	case len(label) > 128:
		return apperr.BadRequest("label must be at most 128 characters")
	case len(fingerprint) > 128:
		return apperr.BadRequest("fingerprint must be at most 128 characters")
	}
	return nil
}

// resolveHolding picks the licence an address signs in on: the first
// usable holding, owner holdings first. Nil when there is none.
func (s *SeatDeviceService) resolveHolding(ctx context.Context, productID, email string) (*store.SeatHolding, error) {
	holdings, err := s.store.FindSeatHoldings(ctx, productID, email)
	if err != nil {
		return nil, err
	}
	for i := range holdings {
		h := holdings[i]
		if !seatSigninSupported(h.License) {
			continue
		}
		if s.licenses.assertUsable(h.License) != nil {
			continue
		}
		return &h, nil
	}
	return nil, nil
}

// seatSigninSupported: seat sign-in needs both halves — seats to sign
// in with and device activations to sign in to — so a hybrid product.
func seatSigninSupported(lic *model.License) bool {
	return lic.Product != nil &&
		model.ProductSupports(lic.Product.Type, model.CapSeats) &&
		model.ProductSupports(lic.Product.Type, model.CapActivations)
}

func (s *SeatDeviceService) signinURL(token string) string {
	return s.baseURL + "/desktop-signin?t=" + token
}

func (s *SeatDeviceService) sendSigninLink(to, productName, label, link string) {
	if s.linkSent != nil {
		s.linkSent(to, link)
		return
	}
	logFallback := func() {
		s.logger.Warn("email not configured — desktop sign-in link printed to log (configure email for delivery)",
			"email", to, "link", link)
	}
	if s.email == nil {
		logFallback()
		return
	}
	configured, err := s.email.Configured()
	switch {
	case err != nil:
		// Fail closed, as the login-code path does: a live sign-in
		// link must not reach the log because of a config read blip.
		s.logger.Error("email config unavailable; sign-in link not sent and not logged",
			"email", to, "error", err)
	case configured:
		s.email.SendDesktopSignin(to, productName, label, link)
	default:
		logFallback()
	}
}

// ─── Describe / Approve (the emailed page) ───

type SigninDescription struct {
	Status      string     `json:"status"`
	Email       string     `json:"email"`
	Label       string     `json:"label"`
	ProductName string     `json:"product_name,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
	RefuseCode  string     `json:"refuse_code,omitempty"`
	NextMoveAt  *time.Time `json:"next_move_at,omitempty"`
}

func signinInvalid() error {
	return apperr.New(404, "SIGNIN_INVALID", "this sign-in link is invalid or has expired")
}

// Describe tells the approval page what it is approving. It changes
// nothing, so a mail scanner fetching the page does no harm.
func (s *SeatDeviceService) Describe(ctx context.Context, linkToken string) (*SigninDescription, error) {
	if strings.TrimSpace(linkToken) == "" {
		return nil, signinInvalid()
	}
	req, err := s.store.FindDesktopSigninByLinkToken(ctx, linkToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, signinInvalid()
		}
		return nil, apperr.Internal(err)
	}
	return s.describe(ctx, req)
}

func (s *SeatDeviceService) describe(ctx context.Context, req *model.DesktopSigninRequest) (*SigninDescription, error) {
	status := req.Status
	if status == model.SigninPending && !s.now().Before(req.ExpiresAt) {
		return nil, signinInvalid()
	}
	if status == model.SigninConsumed {
		status = model.SigninApproved
	}
	d := &SigninDescription{
		Status: status, Email: req.Email, Label: req.Label, ExpiresAt: req.ExpiresAt,
		RefuseCode: req.RefuseCode, NextMoveAt: req.NextMoveAt,
	}
	if p, err := s.store.FindProductByID(ctx, req.ProductID); err == nil {
		d.ProductName = p.Name
	}
	return d, nil
}

// Approve is the click on the emailed page. Repeating it on a request
// already decided answers with that decision.
func (s *SeatDeviceService) Approve(ctx context.Context, linkToken, ip string) (*SigninDescription, error) {
	if strings.TrimSpace(linkToken) == "" {
		return nil, signinInvalid()
	}
	req, err := s.store.FindDesktopSigninByLinkToken(ctx, linkToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, signinInvalid()
		}
		return nil, apperr.Internal(err)
	}
	if req.Status != model.SigninPending {
		return s.describe(ctx, req)
	}
	if !s.now().Before(req.ExpiresAt) {
		return nil, signinInvalid()
	}

	holding, err := s.resolveHolding(ctx, req.ProductID, req.Email)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if holding == nil {
		// The seat went away after the link was sent.
		if err := s.store.RefuseDesktopSignin(ctx, req.ID, "NO_SEAT", nil); err != nil {
			return nil, apperr.Internal(err)
		}
		req.Status, req.RefuseCode = model.SigninRefused, "NO_SEAT"
		return s.describe(ctx, req)
	}
	seatID := ""
	if holding.Seat != nil {
		seatID = holding.Seat.ID
	}
	out, err := s.store.ApproveDesktopSignin(ctx, store.ApproveParams{
		LinkToken: linkToken, LicenseID: holding.License.ID, SeatID: seatID,
		Policy: holding.License.SeatDevicePolicy(), IPAddress: ip, Now: s.now(),
	})
	switch {
	case errors.Is(err, store.ErrSigninNotPending):
		// Raced with another click or expired under the lock.
		fresh, ferr := s.store.FindDesktopSigninByLinkToken(ctx, linkToken)
		if ferr != nil {
			return nil, signinInvalid()
		}
		return s.describe(ctx, fresh)
	case errors.Is(err, store.ErrSeatGone):
		if err := s.store.RefuseDesktopSignin(ctx, req.ID, "NO_SEAT", nil); err != nil {
			return nil, apperr.Internal(err)
		}
		req.Status, req.RefuseCode = model.SigninRefused, "NO_SEAT"
		return s.describe(ctx, req)
	case err != nil:
		return nil, apperr.Internal(err)
	}

	lic := holding.License
	if out.Refused {
		s.store.Audit(ctx, &model.AuditLog{
			Entity: "license", EntityID: lic.ID, Action: "seat_signin_refused",
			ActorType: "seat", ActorID: req.Email, IPAddress: ip,
			Changes: map[string]any{"identifier": req.Identifier, "label": req.Label, "reason": out.RefuseCode},
		})
		return s.describe(ctx, out.Request)
	}

	for _, a := range out.Removed {
		s.dispatch(ctx, lic.ProductID, "license.deactivated", map[string]any{
			"license_id": lic.ID, "identifier": a.Identifier, "reason": "replaced_by_seat_signin",
		})
	}
	if !out.Renewed {
		s.dispatch(ctx, lic.ProductID, "license.activated", map[string]any{
			"license_id": lic.ID, "identifier": req.Identifier, "type": "device", "seat_email": req.Email,
		})
	}
	s.store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "seat_signin_approved",
		ActorType: "seat", ActorID: req.Email, IPAddress: ip,
		Changes: map[string]any{
			"identifier": req.Identifier, "label": req.Label, "renewed": out.Renewed,
			"moved": out.Moved, "replaced": len(out.Removed), "seat_accepted": out.SeatAccepted,
		},
	})
	s.logger.Info("desktop sign-in approved", "license_id", lic.ID, "email", req.Email,
		"renewed", out.Renewed, "moved", out.Moved)
	return s.describe(ctx, out.Request)
}

// ─── Poll ───

type SigninPollResult struct {
	Status     string     `json:"status"` // pending | expired | refused | approved
	RefuseCode string     `json:"refuse_code,omitempty"`
	NextMoveAt *time.Time `json:"next_move_at,omitempty"`
	*SeatSession
}

// SeatSession is what a signed-in device holds: its next device secret
// and a token, plus what the app shows.
type SeatSession struct {
	Token        string         `json:"token"`
	DeviceSecret string         `json:"device_secret"`
	Email        string         `json:"email"`
	LicenseID    string         `json:"license_id"`
	PlanID       string         `json:"plan_id"`
	PlanName     string         `json:"plan_name"`
	ValidUntil   *time.Time     `json:"valid_until,omitempty"`
	Features     map[string]any `json:"features"`
	GraceDays    int            `json:"grace_days"`
	ReauthDue    *time.Time     `json:"reauth_due,omitempty"`
	Meta         map[string]any `json:"meta"`
}

func (s *SeatDeviceService) Poll(ctx context.Context, requestID, pollSecret string) (*SigninPollResult, error) {
	req, err := s.store.FindDesktopSigninByID(ctx, strings.TrimSpace(requestID))
	expected := store.HashOpaqueSecret("")
	if err == nil {
		expected = req.PollSecretHash
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Internal(err)
	}
	match := hmac.Equal([]byte(store.HashOpaqueSecret(pollSecret)), []byte(expected))
	if err != nil || !match || pollSecret == "" {
		return nil, signinInvalid()
	}

	switch req.Status {
	case model.SigninPending:
		if !s.now().Before(req.ExpiresAt) {
			return &SigninPollResult{Status: "expired"}, nil
		}
		return &SigninPollResult{Status: "pending"}, nil
	case model.SigninRefused:
		return &SigninPollResult{Status: "refused", RefuseCode: req.RefuseCode, NextMoveAt: req.NextMoveAt}, nil
	case model.SigninConsumed:
		return &SigninPollResult{Status: "expired"}, nil
	}

	secret, secretHash, err := store.NewOpaqueSecret()
	if err != nil {
		return nil, apperr.Internal(err)
	}
	act, err := s.store.ConsumeDesktopSignin(ctx, req.ID, secretHash, s.now())
	switch {
	case errors.Is(err, store.ErrSigninNotApproved):
		return &SigninPollResult{Status: "expired"}, nil
	case errors.Is(err, store.ErrSeatGone):
		return &SigninPollResult{Status: "refused", RefuseCode: "NO_SEAT"}, nil
	case err != nil:
		return nil, apperr.Internal(err)
	}
	lic, err := s.store.FindLicenseByID(ctx, act.LicenseID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	seatID, _, err := s.store.SeatHolderLive(ctx, lic, act.SeatEmail)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	session, err := s.session(ctx, lic, act, seatID, secret)
	if err != nil {
		return nil, err
	}
	return &SigninPollResult{Status: "approved", SeatSession: session}, nil
}

// reauthDue is when the activation's sign-in lapses; nil when the plan
// never asks for a new one.
func reauthDue(lic *model.License, act *model.Activation) *time.Time {
	days := lic.SeatDevicePolicy().ReauthDays
	if days <= 0 || act.SignedInAt == nil {
		return nil
	}
	t := act.SignedInAt.Add(time.Duration(days) * 24 * time.Hour)
	return &t
}

func (s *SeatDeviceService) session(ctx context.Context, lic *model.License, act *model.Activation, seatID, secret string) (*SeatSession, error) {
	due := reauthDue(lic, act)
	token, features, err := s.licenses.signTokenWith(ctx, lic, act.Identifier, &seatClaims{
		Email: act.SeatEmail, SeatID: seatID, ReauthAt: due,
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	planName := ""
	if lic.Plan != nil {
		planName = lic.Plan.Name
	}
	return &SeatSession{
		Token: token, DeviceSecret: secret, Email: act.SeatEmail, LicenseID: lic.ID,
		PlanID: lic.PlanID, PlanName: planName, ValidUntil: lic.ValidUntil,
		Features: features, GraceDays: s.licenses.effectiveGraceDays(lic),
		ReauthDue: due, Meta: responseMeta(),
	}, nil
}

// ─── Verify / Deactivate ───

type SeatVerifyInput struct {
	DeviceSecret string
	Identifier   string
	Fingerprint  string
	ProductID    string
	IPAddress    string
}

// Verify renews a signed-in device: a new device secret and token.
func (s *SeatDeviceService) Verify(ctx context.Context, in SeatVerifyInput) (*SeatSession, error) {
	if s.failures != nil {
		if blocked, retryAfter := s.failures.IsBlocked(middleware.FailureKeyIP(in.IPAddress)); blocked {
			return nil, lockedOut(retryAfter)
		}
	}
	act, lic, err := s.findDevice(ctx, in.DeviceSecret, in.Identifier, in.Fingerprint, in.ProductID, in.IPAddress, true)
	if err != nil {
		return nil, err
	}
	seatID, live, err := s.store.SeatHolderLive(ctx, lic, act.SeatEmail)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if !live {
		return nil, s.fail(in.IPAddress)
	}
	if due := reauthDue(lic, act); due != nil && !s.now().Before(*due) {
		return nil, apperr.WithDetails(
			apperr.New(401, "REAUTH_REQUIRED", "sign in again to keep using this device"),
			map[string]any{"email": act.SeatEmail, "reauth_due": due},
		)
	}

	secret, secretHash, err := store.NewOpaqueSecret()
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.store.RotateDeviceSecret(ctx, act.ID, store.HashOpaqueSecret(in.DeviceSecret), secretHash, s.now()); err != nil {
		if errors.Is(err, store.ErrDeviceSecretRace) {
			return nil, apperr.Conflict("DEVICE_SECRET_RACE", "another check-in from this device is in progress; retry")
		}
		return nil, apperr.Internal(err)
	}
	if s.failures != nil {
		s.failures.RecordSuccess(middleware.FailureKeyIP(in.IPAddress))
	}
	middleware.LicenseVerifications.WithLabelValues(lic.ProductID, "valid").Inc()
	return s.session(ctx, lic, act, seatID, secret)
}

// Deactivate signs the device out ("Sign out of this computer").
func (s *SeatDeviceService) Deactivate(ctx context.Context, in SeatVerifyInput) error {
	if s.failures != nil {
		if blocked, retryAfter := s.failures.IsBlocked(middleware.FailureKeyIP(in.IPAddress)); blocked {
			return lockedOut(retryAfter)
		}
	}
	act, lic, err := s.findDevice(ctx, in.DeviceSecret, in.Identifier, "", in.ProductID, in.IPAddress, false)
	if err != nil {
		return err
	}
	if err := s.store.DeleteActivation(ctx, act.ID); err != nil {
		return apperr.Internal(err)
	}
	s.store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "deactivated",
		ActorType: "seat", ActorID: act.SeatEmail, IPAddress: in.IPAddress,
		Changes: map[string]any{"identifier": act.Identifier, "via": "seat_signout"},
	})
	s.dispatch(ctx, lic.ProductID, "license.deactivated", map[string]any{
		"license_id": lic.ID, "identifier": act.Identifier, "reason": "seat_signout",
	})
	return nil
}

// findDevice resolves a device secret to its activation and licence,
// enforcing identifier, fingerprint (when asked), product and lifecycle. Every
// failure is the uniform 404. A previous secret presented after the
// grace window means the device was copied: the activation is revoked
// so neither copy keeps working.
func (s *SeatDeviceService) findDevice(ctx context.Context, secret, identifier, fingerprint, productID, ip string, checkFingerprint bool) (*model.Activation, *model.License, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, nil, s.fail(ip)
	}
	act, viaPrev, err := s.store.FindActivationByDeviceSecret(ctx, secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, s.fail(ip)
		}
		return nil, nil, apperr.Internal(err)
	}
	if act.Identifier != identifier {
		return nil, nil, s.fail(ip)
	}
	// The activation is bound to the machine it signed in on. A
	// secret presented from another machine is a copy of the device's
	// files; revoke so the copy cannot keep the original signed out
	// and the original learns at its next check-in.
	if checkFingerprint && act.Fingerprint != "" && act.Fingerprint != fingerprint {
		s.revoke(ctx, act, "fingerprint_mismatch", ip)
		return nil, nil, s.fail(ip)
	}
	if viaPrev && (act.SecretRotatedAt == nil || s.now().Sub(*act.SecretRotatedAt) > store.DeviceSecretGrace) {
		s.revoke(ctx, act, "device_secret_reuse", ip)
		return nil, nil, s.fail(ip)
	}
	lic, err := s.store.FindLicenseByID(ctx, act.LicenseID)
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	if productID != "" && lic.ProductID != productID {
		return nil, nil, s.fail(ip)
	}
	if !seatSigninSupported(lic) || s.licenses.assertUsable(lic) != nil {
		return nil, nil, s.fail(ip)
	}
	return act, lic, nil
}

// revoke deletes an activation that has proved to exist in two places.
func (s *SeatDeviceService) revoke(ctx context.Context, act *model.Activation, reason, ip string) {
	if err := s.store.DeleteActivation(ctx, act.ID); err != nil {
		s.logger.Error("revoking copied device failed", "activation_id", act.ID, "error", err)
		return
	}
	s.store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: act.LicenseID, Action: "deactivated",
		ActorType: "system", IPAddress: ip,
		Changes: map[string]any{"identifier": act.Identifier, "seat_email": act.SeatEmail, "reason": reason},
	})
	s.logger.Warn("seat device revoked", "license_id", act.LicenseID, "email", act.SeatEmail, "reason", reason)
	if lic, err := s.store.FindLicenseByID(ctx, act.LicenseID); err == nil {
		s.dispatch(ctx, lic.ProductID, "license.deactivated", map[string]any{
			"license_id": lic.ID, "identifier": act.Identifier, "reason": reason,
		})
	}
}

func (s *SeatDeviceService) fail(ip string) error {
	if s.failures != nil {
		key := middleware.FailureKeyIP(ip)
		s.failures.RecordFailure(key)
		if blocked, retryAfter := s.failures.IsBlocked(key); blocked {
			return lockedOut(retryAfter)
		}
	}
	return licenseNotFound()
}

func (s *SeatDeviceService) dispatch(ctx context.Context, productID, event string, data map[string]any) {
	if s.webhook != nil {
		s.webhook.Dispatch(ctx, productID, event, data)
	}
}

// ─── Email ───

// SendDesktopSignin mails the sign-in link. The page it opens asks for
// a click before anything happens.
func (e *EmailService) SendDesktopSignin(to, productName, deviceLabel, link string) {
	if productName == "" {
		productName = "your app"
	}
	device := "a computer"
	if deviceLabel != "" {
		device = deviceLabel
	}
	subject, body := e.renderParts("desktop_signin", "Sign in to "+productName, map[string]any{
		"Product": productName, "Device": device, "Link": link,
	})
	go func() {
		if err := e.Send(to, subject, body); err != nil {
			e.logger.Error("desktop sign-in email delivery failed", "to", to, "error", err)
		}
	}()
}
