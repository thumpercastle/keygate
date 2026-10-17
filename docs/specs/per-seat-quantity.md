# Spec: per-seat quantity for subscription plans

Upstream discussion: https://github.com/tabloy/keygate/issues/41
Base: upstream `main` @ `4c27a4d` (v0.1.13)

---

## 1. Goal

Let a merchant sell one licence covering **N seats**, priced per seat in Stripe (unit price × N), with the buyer choosing N at checkout and changing it later in Stripe's customer portal.

Today a plan's `max_seats` and `max_activations` are fixed, and every checkout line item is created with `Quantity: 1`. That happens in `internal/payment/stripe.go`, in `CreateCheckoutSession`, `CheckoutByPlan` and the update-renewal checkout.

The feature is **opt-in per plan**. A plan without `per_seat` must behave exactly as it does today: same limits, same UI, same API responses apart from new additive fields.

## 2. Semantics

Let `q` be the licence's quantity (≥ 1) and `plan` its plan. For a plan with `per_seat = true`:

| Limit | Effective value |
|---|---|
| Activations (also floating concurrent sessions) | `plan.max_activations × q`. If `plan.max_activations == 0` (unlimited), it stays 0/unlimited. |
| Seats (people, **including the owner**) | `plan.max_seats × q`. If `plan.max_seats == 0` (unlimited), it stays unlimited. |
| Invitations allowed | `effective_seats − 1`, because **the owner occupies one seat** |

For a plan with `per_seat = false`, the effective values equal the plan values, and invitations are capped at `plan.max_seats`. That's today's behaviour, where the owner holds no seat; leave it unchanged.

**Example:** `max_seats = 1`, `max_activations = 1`, `q = 5` gives 5 people (the owner plus 4 invitees) and 5 devices.

**Trap: a cap of 0 means unlimited.** `store.AddSeatWithinLimit(ctx, seat, maxSeats)` treats `maxSeats <= 0` as **no limit**. A `per_seat` licence with 1 effective seat has an invitation cap of **0**, and that must mean "no invitations", not "unlimited". Refuse in the service before calling the store, returning the same `ErrSeatLimitReached` → 409 "maximum seats reached" response, or give the store an explicit way to say "zero allowed". Add a test for this exact case.

**Reducing q below current usage:** never remove seats, activations or sessions automatically. When usage is at or over the effective cap, new invitations and activations are refused, which is how the existing `count >= max` checks already behave. Existing members and devices keep working. The portal shows the over-limit state (§7).

## 3. Data model and migration

New migration pair `db/migrations/2026101x_per_seat_quantity.{up,down}.sql`. Use the date you write it, and follow the existing file style: a header comment explaining why, `IF NOT EXISTS` / `IF EXISTS`.

```sql
-- up
ALTER TABLE plans
  ADD COLUMN IF NOT EXISTS per_seat     BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS min_quantity INTEGER NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS max_quantity INTEGER NOT NULL DEFAULT 1;
ALTER TABLE plans ADD CONSTRAINT plans_quantity_bounds
  CHECK (min_quantity >= 1 AND max_quantity >= min_quantity AND max_quantity <= 10000);
ALTER TABLE plans ADD CONSTRAINT plans_per_seat_subscription
  CHECK (NOT per_seat OR license_type = 'subscription');
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS quantity INTEGER NOT NULL DEFAULT 1;
ALTER TABLE licenses ADD CONSTRAINT licenses_quantity_positive CHECK (quantity >= 1);
```

The down migration drops the constraints and columns.

Check that the constraints don't collide with the existing plan triggers (`plans_prices_disjoint`, `plans_record_update_terms`, `plans_require_gated_feed`). Run the full migration chain from empty against `kg-dev-pg`.

**`internal/model/model.go`:**
- `Plan`: add `PerSeat bool`, `MinQuantity int`, `MaxQuantity int`, with JSON `per_seat`, `min_quantity`, `max_quantity`. Heed the comment above `MaxActivations`: **no bun `default:N` annotations**. The handler is the source of truth.
- `License`: add `Quantity int` with `bun:",notnull"`, JSON `quantity`.
- Add **one** helper and use it everywhere, so nothing multiplies by hand:
  ```go
  // EffectiveLimits: activations, seats (incl. owner when per-seat), invite cap.
  func (l *License) EffectiveLimits() Limits
  ```
  It returns the activation cap, the seat cap and the invitation cap, with an explicit "unlimited" flag rather than relying on 0. It must handle `l.Plan == nil` the same way each call site does today, for example `maxActivations` defaulting to 3.

## 4. Replace the direct limit reads

Each of these reads `lic.Plan.MaxActivations` / `lic.Plan.MaxSeats` today. Route each through the helper:

| Site | Use |
|---|---|
| `internal/service/license.go` `maxActivations` (→ `ActivateWithinLimit`) | activation cap |
| `internal/service/floating.go` (two sites, `maxSessions`) | activation cap |
| `internal/service/seats.go` `AddSeat` (`maxSeats`) | **invitation cap**, with the zero trap from §2 |
| `internal/service/seats.go` `CheckSeatAccess` | for `per_seat`, the owner's email must return true. Leave non-per-seat behaviour alone. |
| `internal/handler/portal_activations.go` `maxActivationsForLicense` | activation cap |

Then grep again. `grep -rn "Plan.MaxSeats\|Plan.MaxActivations" internal` should leave only the helper and admin plan CRUD/validation. Any other hit is a missed call site.

## 5. Stripe

### 5.1 Checkout (`CreateCheckoutSession`, `CheckoutByPlan`)
- For `per_seat` plans, set the line item `Quantity` to the requested value, defaulting to `min_quantity`. Also set `AdjustableQuantity{Enabled: true, Minimum: min_quantity, Maximum: max_quantity}`, so the buyer can change it on Stripe's page.
- Accept the requested quantity as the `quantity` JSON field in `CreateCheckoutSession`, and as `?quantity=N` on `GET /pay/:checkout_id`. Reject anything outside `[min, max]`: 400 on the API, plain-text 400 on `/pay`. For non-`per_seat` plans, ignore `quantity` and keep `1`.
- Leave the update-renewal checkout (perpetual) at quantity 1. It's out of scope.
- Also record `metaQuantity` in the session metadata for traceability. **The subscription item is authoritative**, not the metadata (§5.2).

### 5.2 Fulfilment (`fulfillCheckout` → `CreateLicenseWithSubscription`)
- When the plan is `per_seat` and there's a subscription, set `lic.Quantity` from the subscription item's `quantity`. `resolvePlan(ctx, subscriptionID)` already fetches the subscription, so reuse that read rather than adding another Stripe call.
- Otherwise `lic.Quantity = 1`.
- Item selection: pick the item whose price equals `plan.StripePriceID`, falling back to `Items.Data[0]`. That matches `ChangePlan`'s single-item assumption.
- Include the quantity in the new-licence audit `Changes`.

### 5.3 Sync (`onSubscriptionUpdated` → `syncFromCurrent` → `applySubscriptionState`)
- Extend `subscriptionEvent.Items.Data[]` with `Quantity int64` and the price id, so the selection rule in §5.2 can be applied.
- In `applySubscriptionState`, for a `per_seat` licence whose subscription reports a quantity ≥ 1 that differs from `lic.Quantity`, set it and append `"quantity"` to `cols`. It must go through the **same** `applyLicenseFromSubscription` write, so it inherits the existing `asOf` / `StripeReadStamp` ordering guard and the restricted-licence guard. Do not add a separate write path; Stripe events arrive out of order.
- When the write lands and the quantity actually changed:
  - write an audit entry `quantity_changed` with `{old, new}`;
  - dispatch a `license.quantity_changed` webhook with `{license_id, old_quantity, new_quantity}`. Add `EventLicenseQuantityChanged = "license.quantity_changed"` beside `EventPlanChanged` in `internal/model/model.go` (around line 795). Also register it wherever the webhook UI builds its event picker, so it can be selected there.
- This one path covers every source of change: the Stripe customer portal, the Stripe dashboard, and the API. **No new Keygate "change seats" endpoint** in this version.

### 5.4 `ChangePlan`
When swapping the item's price, pass the item's current `Quantity` explicitly, so a plan change never resets seats to 1. When moving between a `per_seat` plan and a non-`per_seat` plan:
- `per_seat` → non-`per_seat`: set quantity to 1, and set `lic.Quantity = 1`.
- non-`per_seat` → `per_seat`: keep 1, clamped to the new plan's `[min, max]`.

Cover both cases with tests.

The admin path `AdminHandler.ChangeLicensePlan` (`internal/handler/admin.go`, around line 3364) already refuses Stripe-billed licences with 409 `STRIPE_MANAGED`. For manually issued licences, apply the same rules: per_seat → non-per_seat resets the quantity to 1, and the other direction clamps it to the new plan's `[min, max]`.

### 5.5 Stripe customer portal
Nothing to code. The merchant enables "customers can update quantity" in Stripe → Settings → Billing → Customer portal. Mention this in the plan form hint (§7) and the PR description.

## 6. API surface (additive only)
- Plan JSON: `per_seat`, `min_quantity`, `max_quantity`.
- Licence JSON (admin and portal): `quantity`, plus `effective_max_activations` and `effective_max_seats`, with 0 still meaning unlimited. The frontend should display these and never compute them.
- Admin create/update plan (`internal/handler/admin.go`, around lines 899–1290, and the shared validator around line 1779):
  - accept the three fields;
  - validate `1 ≤ min ≤ max ≤ 10000`, and `per_seat` only with `license_type = subscription`, giving a clear 400 rather than leaving it to the DB constraint;
  - `per_seat` with `max_seats > 0` requires a product supporting `CapSeats`, matching the existing `ProductSupports` checks.
- Admin create licence (`CreateLicense`): accept an optional `quantity` for `per_seat` plans, default `min_quantity` and validated against the bounds. This is for manually issued team licences.
- **Should-have (do it if time allows, but don't block on it):** let an admin edit `quantity` on a licence **without** a Stripe subscription. A Stripe-billed licence must answer 409 "quantity is managed by Stripe".
- Update `docs/api-contract.md` / `docs/openapi.yaml` for every changed request and response.

## 7. UI (`web/`)
- **Admin, plan form (`pages/admin/plans.tsx`):**
  - a "Sold per seat" toggle, enabled only for Subscription;
  - Min and Max quantity fields, shown when it's on;
  - hint text: per-seat multiplies max activations and max seats by the purchased quantity, the owner counts as a seat, and enabling quantity changes in Stripe's customer portal lets customers change it later.
- **Admin, plans table:** show "per seat" next to the limits.
- **Admin, licence detail (`pages/admin/licenses.tsx`):** show the quantity and the effective limits.
- **Portal (`pages/portal/licenses.tsx`):**
  - Seats tile: show `effective_max_seats`.
  - `showSeats`: for `per_seat`, show the Team Members tab when `effective_max_seats > 1`. Leave the non-per-seat rule unchanged.
  - The Team Members header counts the **owner** for `per_seat`, e.g. "Team members (3/5)" = owner + 2. Disable *Add member* at the invitation cap.
  - When usage exceeds the cap after a reduction, show an over-limit note.
  - Add a "Change seats" link that opens the existing Stripe billing-portal session (`CreatePortalSession`).
- **i18n:** add every new string to **both** `web/src/i18n/locales/en.ts` and `zh.ts`. If you can't translate, mark the `zh` entry with an English placeholder and say so in the PR.
- `bun run typecheck`, `bun run lint` and `bun run build` must pass.

## 8. Tests (Go)

Follow the existing patterns, e.g. `*_test.go` beside the code and the store integration tests using `TEST_DATABASE_URL`.

1. The `EffectiveLimits` table: per_seat on/off, q = 1/5, max = 0 (unlimited), and `Plan == nil`.
2. Seat invites for a per_seat plan with `max_seats=1`:
   - q=1: 0 invitations allowed. This is **the zero trap**.
   - q=3: exactly 2 invitations, and the 3rd returns 409.
   - Non-per_seat `max_seats=2` still allows 2 invitations. This is the regression guard for today's behaviour.
3. Activations: per_seat `max_activations=1`, q=3 allows 3 devices and refuses the 4th, including under concurrency, mirroring the existing atomic tests.
4. Checkout params:
   - per_seat sets `Quantity` and `AdjustableQuantity` with min/max;
   - an out-of-range quantity is rejected;
   - non-per_seat stays at 1 with no adjustable quantity.

   Stub Stripe the way the existing payment tests do: an `httptest` server standing in for the Stripe backend. See `internal/payment/fulfill_test.go` and `renewal_test.go`. Never call the real Stripe API.
5. Sync:
   - a subscription read with quantity 7 updates the licence, writes the audit entry and dispatches the webhook;
   - a stale read, i.e. an older `asOf`, does not overwrite a newer quantity;
   - a suspended or revoked licence is not modified;
   - a non-per_seat plan ignores the quantity.
6. Fulfilment stores the subscription item's quantity.
7. `ChangePlan` keeps the quantity, and handles the per_seat ↔ non-per_seat transitions.
8. Migration: the up and down migrations apply cleanly from empty and on top of the current chain.

## 9. Definition of done
- Everything on the PR checklist (`.github/pull_request_template.md`) passes:
  - `go vet ./...`
  - `go test ./... -count=1`
  - the integration tests with `TEST_DATABASE_URL`
  - `go build ./cmd/server`
  - `cd web && bun install --frozen-lockfile && bun run typecheck && bun run lint && bun run build`
- A plan with `per_seat=false` behaves byte-for-byte as before. Say how you checked.

## 10. Out of scope
- Perpetual / one-time plans with quantity.
- A Keygate-native "change seats" endpoint. The Stripe customer portal covers it.
- Changing the owner/seat semantics for **non**-per_seat plans. This is the inconsistency reported in issue #41: the portal hides the team tab at `max_seats=1`, while the server allows owner + `max_seats` invitees. Leave it for the maintainers.
- Tiered or graduated Stripe pricing. That's Stripe-side configuration and works unchanged, because Keygate only reads the quantity.
