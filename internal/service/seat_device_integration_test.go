package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tabloy/keygate/internal/license"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/apperr"
)

// seatFixture is a hybrid product with a per-seat plan (1 device per
// seat, 2 moves per 30 days, re-sign-in every 180 days) and one licence
// for three seats, plus a seat sign-in service on a fake clock.
type seatFixture struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	pub     ed25519.PublicKey
	lic     *LicenseService
	svc     *SeatDeviceService
	seats   *SeatService
	product *model.Product
	plan    *model.Plan
	license *model.License
	clock   time.Time

	mu    sync.Mutex
	links map[string]string // email → last link token mailed
}

func newSeatFixture(t *testing.T) *seatFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	st, err := store.New(dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	product := &model.Product{Name: "Seat Signin", Slug: "seat-signin-" + suffix, Type: model.ProductTypeHybrid}
	if err := st.CreateProduct(ctx, product); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{
		ProductID: product.ID, Name: "Per seat", Slug: "per-seat-" + suffix,
		LicenseType: "subscription", LicenseModel: "standard", PerSeat: true,
		MinQuantity: 1, MaxQuantity: 10, MaxActivations: 1, MaxSeats: 1, GraceDays: 7,
		MaxDevicesPerSeat: 1, MaxDeviceMovesPer30d: 2, ReauthDays: 180,
	}
	if err := st.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	lic := &model.License{
		ProductID: product.ID, PlanID: plan.ID, Email: "Owner-" + suffix + "@Example.com",
		LicenseKey: "KG-SEATSIGNIN-" + suffix, Status: model.StatusActive, Quantity: 3,
	}
	if err := st.CreateLicense(ctx, lic); err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	licSvc := NewLicenseService(st, priv, slog.Default(), nil, nil)
	f := &seatFixture{
		t: t, ctx: ctx, st: st, pub: pub, lic: licSvc,
		product: product, plan: plan, license: lic,
		clock: time.Now().Truncate(time.Second), links: map[string]string{},
	}
	f.svc = NewSeatDeviceService(st, licSvc, nil, nil, nil, slog.Default(), "https://kg.test")
	f.svc.now = func() time.Time { return f.clock }
	licSvc.clock = f.svc.now
	f.svc.linkSent = func(to, link string) {
		u, err := url.Parse(link)
		if err != nil {
			t.Errorf("mailed link does not parse: %v", err)
			return
		}
		f.mu.Lock()
		f.links[to] = u.Query().Get("t")
		f.mu.Unlock()
	}
	f.seats = NewSeatService(st, NewWebhookService(st, slog.Default(), time.Second, 1, false), nil, slog.Default(), "")
	return f
}

func (f *seatFixture) owner() string { return strings.ToLower(f.license.Email) }

func (f *seatFixture) advance(d time.Duration) { f.clock = f.clock.Add(d) }

// lastLink returns and forgets the link last mailed to email.
func (f *seatFixture) lastLink(email string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := f.links[email]
	delete(f.links, email)
	return tok
}

// start asks for a sign-in, ageing earlier requests out of the
// per-address rate window so a test can sign in as often as it needs.
func (f *seatFixture) start(email, identifier, fingerprint string) *SigninStartResult {
	f.t.Helper()
	if _, err := f.st.DB.NewRaw(
		"UPDATE desktop_signin_requests SET created_at = created_at - interval '1 hour' WHERE email = ?",
		strings.ToLower(email)).Exec(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	res, err := f.svc.Start(f.ctx, SigninStartInput{
		Email: email, ProductID: f.product.ID, Identifier: identifier,
		Label: "PC " + identifier, Fingerprint: fingerprint, IPAddress: "198.51.100.7",
	})
	if err != nil {
		f.t.Fatalf("start %s on %s: %v", email, identifier, err)
	}
	return res
}

// signIn runs start → approve → poll and returns the approved session.
func (f *seatFixture) signIn(email, identifier, fingerprint string) *SeatSession {
	f.t.Helper()
	res := f.start(email, identifier, fingerprint)
	link := f.lastLink(strings.ToLower(email))
	if link == "" {
		f.t.Fatalf("no link mailed to %s", email)
	}
	d, err := f.svc.Approve(f.ctx, link, "198.51.100.8")
	if err != nil {
		f.t.Fatalf("approve: %v", err)
	}
	if d.Status != model.SigninApproved {
		f.t.Fatalf("approve status = %s (%s), want approved", d.Status, d.RefuseCode)
	}
	poll, err := f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if err != nil {
		f.t.Fatalf("poll: %v", err)
	}
	if poll.Status != "approved" || poll.SeatSession == nil {
		f.t.Fatalf("poll status = %s, want approved", poll.Status)
	}
	return poll.SeatSession
}

func (f *seatFixture) verify(secret, identifier, fingerprint string) (*SeatSession, error) {
	return f.svc.Verify(f.ctx, SeatVerifyInput{
		DeviceSecret: secret, Identifier: identifier, Fingerprint: fingerprint,
		ProductID: f.product.ID, IPAddress: "198.51.100.9",
	})
}

func assertCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Status != status || ae.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

func TestSeatSignin_OwnerFlowAndToken(t *testing.T) {
	f := newSeatFixture(t)

	res := f.start(f.license.Email, "dev-a", "fp-a")
	link := f.lastLink(f.owner())
	if link == "" {
		t.Fatal("owner was not mailed a link")
	}

	// Polling before approval waits.
	poll, err := f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if err != nil || poll.Status != "pending" {
		t.Fatalf("poll before approve = %+v, %v; want pending", poll, err)
	}
	// A wrong poll secret is the uniform not-found.
	_, err = f.svc.Poll(f.ctx, res.RequestID, strings.Repeat("0", 64))
	assertCode(t, err, 404, "SIGNIN_INVALID")

	// Opening the page (describe) changes nothing: a mail scanner
	// fetching the link must not sign anyone in.
	d, err := f.svc.Describe(f.ctx, link)
	if err != nil || d.Status != model.SigninPending || d.Label != "PC dev-a" {
		t.Fatalf("describe = %+v, %v", d, err)
	}
	poll, _ = f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if poll.Status != "pending" {
		t.Fatalf("describe approved the request: %+v", poll)
	}

	d, err = f.svc.Approve(f.ctx, link, "203.0.113.1")
	if err != nil || d.Status != model.SigninApproved {
		t.Fatalf("approve = %+v, %v", d, err)
	}
	// A second click answers with the same decision.
	if d2, err := f.svc.Approve(f.ctx, link, "203.0.113.1"); err != nil || d2.Status != model.SigninApproved {
		t.Fatalf("second approve = %+v, %v", d2, err)
	}

	poll, err = f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if err != nil || poll.Status != "approved" {
		t.Fatalf("poll after approve = %+v, %v", poll, err)
	}
	if poll.DeviceSecret == "" || poll.Email != f.owner() || poll.LicenseID != f.license.ID {
		t.Fatalf("session = %+v", poll.SeatSession)
	}
	// The result is handed over once.
	again, err := f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if err != nil || again.Status != "expired" {
		t.Fatalf("second poll = %+v, %v; want expired", again, err)
	}

	tok, err := license.Verify(poll.Token, f.pub)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if tok.Email != f.owner() || tok.SeatID != "" || tok.Identifier != "dev-a" {
		t.Fatalf("claims = %+v", tok)
	}
	wantReauth := f.clock.Add(180 * 24 * time.Hour).Unix()
	if tok.ReauthAt != wantReauth || tok.ExpiresAt > wantReauth {
		t.Fatalf("rau = %d exp = %d, want rau %d and exp <= rau", tok.ReauthAt, tok.ExpiresAt, wantReauth)
	}
	if poll.ReauthDue == nil || poll.ReauthDue.Unix() != wantReauth {
		t.Fatalf("reauth_due = %v", poll.ReauthDue)
	}
}

func TestSeatSignin_StartDoesNotRevealSeats(t *testing.T) {
	f := newSeatFixture(t)
	stranger := "nobody-" + f.license.ID + "@example.com"

	known, err := f.svc.Start(f.ctx, SigninStartInput{Email: f.license.Email, ProductID: f.product.ID, Identifier: "x"})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := f.svc.Start(f.ctx, SigninStartInput{Email: stranger, ProductID: f.product.ID, Identifier: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if known.Interval != unknown.Interval || known.ExpiresIn != unknown.ExpiresIn ||
		len(known.PollSecret) != len(unknown.PollSecret) || len(known.RequestID) != len(unknown.RequestID) {
		t.Fatalf("responses differ: %+v vs %+v", known, unknown)
	}
	if f.lastLink(stranger) != "" {
		t.Fatal("a stranger was mailed a link")
	}
	// The rate limit counts both alike: the fourth request in ten
	// minutes is refused for either address.
	for _, email := range []string{f.license.Email, stranger} {
		for i := 0; i < 2; i++ {
			if _, err := f.svc.Start(f.ctx, SigninStartInput{Email: email, ProductID: f.product.ID, Identifier: "x"}); err != nil {
				t.Fatalf("%s request %d: %v", email, i+2, err)
			}
		}
		_, err := f.svc.Start(f.ctx, SigninStartInput{Email: email, ProductID: f.product.ID, Identifier: "x"})
		assertCode(t, err, 429, "RATE_LIMITED")
	}
}

func TestSeatSignin_LinkExpires(t *testing.T) {
	f := newSeatFixture(t)
	res := f.start(f.license.Email, "dev-a", "")
	link := f.lastLink(f.owner())
	f.advance(store.DesktopSigninTTL + time.Second)
	_, err := f.svc.Approve(f.ctx, link, "")
	assertCode(t, err, 404, "SIGNIN_INVALID")
	poll, err := f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if err != nil || poll.Status != "expired" {
		t.Fatalf("poll = %+v, %v; want expired", poll, err)
	}
}

func TestSeatSignin_SecretRotationAndReuse(t *testing.T) {
	f := newSeatFixture(t)
	s1 := f.signIn(f.license.Email, "dev-a", "fp-a")

	s2, err := f.verify(s1.DeviceSecret, "dev-a", "fp-a")
	if err != nil {
		t.Fatalf("verify with first secret: %v", err)
	}
	if s2.DeviceSecret == s1.DeviceSecret {
		t.Fatal("secret was not rotated")
	}
	// A retry with the previous secret inside the grace window (the
	// client lost the response) still works.
	f.advance(time.Minute)
	s3, err := f.verify(s1.DeviceSecret, "dev-a", "fp-a")
	if err != nil {
		t.Fatalf("retry with previous secret inside grace: %v", err)
	}
	// After the window, the previous secret means a copy exists:
	// the device is revoked and no secret works any more.
	f.advance(store.DeviceSecretGrace + time.Minute)
	_, err = f.verify(s1.DeviceSecret, "dev-a", "fp-a")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
	_, err = f.verify(s3.DeviceSecret, "dev-a", "fp-a")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
}

func TestSeatSignin_FingerprintMismatchRevokes(t *testing.T) {
	f := newSeatFixture(t)
	s := f.signIn(f.license.Email, "dev-a", "fp-a")

	_, err := f.verify(s.DeviceSecret, "dev-a", "fp-other-machine")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
	_, err = f.verify(s.DeviceSecret, "dev-a", "fp-a")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
}

func TestSeatSignin_MoveLimit(t *testing.T) {
	f := newSeatFixture(t)
	a := f.signIn(f.license.Email, "dev-a", "") // first device: free
	firstMove := f.clock.Add(time.Hour)
	f.clock = firstMove
	b := f.signIn(f.license.Email, "dev-b", "") // move 1

	// One device per seat: A was signed out by B's sign-in.
	_, err := f.verify(a.DeviceSecret, "dev-a", "")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
	if _, err := f.verify(b.DeviceSecret, "dev-b", ""); err != nil {
		t.Fatalf("B after its sign-in: %v", err)
	}

	f.advance(time.Hour)
	f.signIn(f.license.Email, "dev-a", "") // move 2

	// A third move inside 30 days is refused, on the page and to the app.
	f.advance(time.Hour)
	res := f.start(f.license.Email, "dev-c", "")
	d, err := f.svc.Approve(f.ctx, f.lastLink(f.owner()), "")
	if err != nil {
		t.Fatal(err)
	}
	wantNext := firstMove.Add(store.MoveWindow)
	if d.Status != model.SigninRefused || d.RefuseCode != store.RefuseMoveLimit ||
		d.NextMoveAt == nil || !d.NextMoveAt.Equal(wantNext) {
		t.Fatalf("third move = %+v, want refused until %v", d, wantNext)
	}
	poll, err := f.svc.Poll(f.ctx, res.RequestID, res.PollSecret)
	if err != nil || poll.Status != "refused" || poll.RefuseCode != store.RefuseMoveLimit {
		t.Fatalf("poll after refusal = %+v, %v", poll, err)
	}

	// Signing in again on the device that holds the seat is not a move.
	f.signIn(f.license.Email, "dev-a", "")

	// Once the first move ages out of the window, one move frees up.
	f.clock = wantNext.Add(time.Second)
	f.signIn(f.license.Email, "dev-c", "")
}

func TestSeatSignin_SignOutDoesNotDodgeMoveLimit(t *testing.T) {
	f := newSeatFixture(t)
	devices := []string{"dev-a", "dev-b", "dev-a"}
	for _, d := range devices {
		s := f.signIn(f.license.Email, d, "")
		if err := f.svc.Deactivate(f.ctx, SeatVerifyInput{DeviceSecret: s.DeviceSecret, Identifier: d}); err != nil {
			t.Fatalf("sign out of %s: %v", d, err)
		}
		f.advance(time.Hour)
	}
	// a → b → a used both moves even though each device signed out
	// first; b again is a third move.
	f.start(f.license.Email, "dev-b", "")
	d, err := f.svc.Approve(f.ctx, f.lastLink(f.owner()), "")
	if err != nil || d.Status != model.SigninRefused {
		t.Fatalf("third move after sign-outs = %+v, %v; want refused", d, err)
	}
}

func TestSeatSignin_ReauthAfter180Days(t *testing.T) {
	f := newSeatFixture(t)
	s := f.signIn(f.license.Email, "dev-a", "")
	signedIn := f.clock

	f.clock = signedIn.Add(179 * 24 * time.Hour)
	s, err := f.verify(s.DeviceSecret, "dev-a", "")
	if err != nil {
		t.Fatalf("verify on day 179: %v", err)
	}
	tok, err := license.Verify(s.Token, f.pub)
	if err != nil {
		// exp is capped at the reauth instant, a day from now: still valid.
		t.Fatalf("token on day 179: %v", err)
	}
	if tok.ExpiresAt != signedIn.Add(180*24*time.Hour).Unix() {
		t.Fatalf("exp = %d, want capped at the reauth instant", tok.ExpiresAt)
	}

	f.clock = signedIn.Add(180 * 24 * time.Hour)
	_, err = f.verify(s.DeviceSecret, "dev-a", "")
	assertCode(t, err, 401, "REAUTH_REQUIRED")

	// Signing in again on the same computer renews it without a move.
	s = f.signIn(f.license.Email, "dev-a", "")
	if s.ReauthDue == nil || !s.ReauthDue.Equal(f.clock.Add(180*24*time.Hour)) {
		t.Fatalf("reauth_due after re-sign-in = %v", s.ReauthDue)
	}
	var moves int
	if err := f.st.DB.NewRaw("SELECT count(*) FROM seat_device_events WHERE license_id = ? AND is_move",
		f.license.ID).Scan(f.ctx, &moves); err != nil {
		t.Fatal(err)
	}
	if moves != 0 {
		t.Fatalf("re-sign-in on the same computer recorded %d moves", moves)
	}
}

func TestSeatSignin_MemberSeatAcceptedAndRemovalSignsOut(t *testing.T) {
	f := newSeatFixture(t)
	member := "member-" + f.license.ID + "@example.com"
	seat, err := f.seats.AddSeat(f.ctx, AddSeatInput{LicenseKey: f.license.LicenseKey, Email: member, ProductID: f.product.ID})
	if err != nil {
		t.Fatalf("add seat: %v", err)
	}

	// A pending seat shows nothing in the member's portal...
	if lics, err := f.st.ListLicensesByEmail(f.ctx, member); err != nil || len(lics) != 0 {
		t.Fatalf("pending seat listed %d licences, %v; want none", len(lics), err)
	}

	s := f.signIn(member, "member-pc", "")
	// ...until signing in accepts it.
	if lics, err := f.st.ListLicensesByEmail(f.ctx, member); err != nil || len(lics) != 1 {
		t.Fatalf("accepted seat listed %d licences, %v; want 1", len(lics), err)
	}
	tok, err := license.Verify(s.Token, f.pub)
	if err != nil || tok.Email != member || tok.SeatID != seat.ID {
		t.Fatalf("member token = %+v, %v", tok, err)
	}
	got, err := f.st.FindSeatByID(f.ctx, seat.ID)
	if err != nil || got.AcceptedAt == nil || got.UserID == "" {
		t.Fatalf("seat not accepted by sign-in: %+v, %v", got, err)
	}

	if err := f.seats.RemoveSeat(f.ctx, f.license.LicenseKey, seat.ID, f.product.ID, ""); err != nil {
		t.Fatalf("remove seat: %v", err)
	}
	_, err = f.verify(s.DeviceSecret, "member-pc", "")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")

	// The removed address is no longer mailed a link.
	f.start(member, "member-pc", "")
	if f.lastLink(member) != "" {
		t.Fatal("removed member was mailed a link")
	}
}

func TestSeatSignin_OwnerReleasesSeat(t *testing.T) {
	f := newSeatFixture(t)
	s := f.signIn(f.license.Email, "dev-a", "")

	lic, err := f.st.FindLicenseByID(f.ctx, f.license.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lic.EffectiveLimits().Invitations.Value; got != 2 {
		t.Fatalf("invitations with owner seated = %d, want 2", got)
	}
	removed, err := f.st.SetOwnerSeatReleased(f.ctx, lic, true)
	if err != nil || len(removed) != 1 {
		t.Fatalf("release owner seat: removed %d, %v", len(removed), err)
	}
	if got := lic.EffectiveLimits().Invitations.Value; got != 3 {
		t.Fatalf("invitations with owner seat released = %d, want 3", got)
	}
	_, err = f.verify(s.DeviceSecret, "dev-a", "")
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
	f.start(f.license.Email, "dev-a", "")
	if f.lastLink(f.owner()) != "" {
		t.Fatal("owner who released their seat was mailed a link")
	}
}

func TestSeatSignin_KeyPathCannotTouchSeatDevices(t *testing.T) {
	f := newSeatFixture(t)
	// A device activated with the key before the switch...
	if _, err := f.lic.Activate(f.ctx, ActivateInput{LicenseKey: f.license.LicenseKey, Identifier: "dev-a"}); err != nil {
		t.Fatalf("key activate: %v", err)
	}
	// ...signs in with its seat: the key activation is replaced.
	s := f.signIn(f.license.Email, "dev-a", "")
	acts, err := f.st.ListActivations(f.ctx, f.license.ID)
	if err != nil || len(acts) != 1 || acts[0].SeatEmail != f.owner() {
		t.Fatalf("activations after sign-in = %+v, %v", acts, err)
	}

	// The key can neither renew nor sign out a seat-bound device.
	_, err = f.lic.Verify(f.ctx, VerifyInput{LicenseKey: f.license.LicenseKey, Identifier: "dev-a"})
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
	_, err = f.lic.Activate(f.ctx, ActivateInput{LicenseKey: f.license.LicenseKey, Identifier: "dev-a"})
	assertCode(t, err, 409, "SEAT_BOUND_DEVICE")
	err = f.lic.Deactivate(f.ctx, DeactivateInput{LicenseKey: f.license.LicenseKey, Identifier: "dev-a"})
	assertCode(t, err, 404, "LICENSE_NOT_FOUND")
	if _, err := f.verify(s.DeviceSecret, "dev-a", ""); err != nil {
		t.Fatalf("seat verify after key attempts: %v", err)
	}

	// A plan that requires seat sign-in takes no key activations.
	if _, err := f.st.DB.NewRaw("UPDATE plans SET require_seat_signin = true WHERE id = ?", f.plan.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.lic.Activate(f.ctx, ActivateInput{LicenseKey: f.license.LicenseKey, Identifier: "dev-z"})
	assertCode(t, err, 403, "SEAT_SIGNIN_REQUIRED")
}

func TestSeatSignin_DesktopProductHasNoSeatSignin(t *testing.T) {
	f := newSeatFixture(t)
	if _, err := f.st.DB.NewRaw("UPDATE products SET type = 'desktop' WHERE id = ?", f.product.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.start(f.license.Email, "dev-a", "")
	if f.lastLink(f.owner()) != "" {
		t.Fatal("a desktop-only product mailed a seat sign-in link")
	}
}
