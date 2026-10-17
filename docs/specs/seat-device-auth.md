# Seat-bound desktop sign-in

Builds on per-seat quantity (`docs/specs/per-seat-quantity.md`).

A licence owner buys N seats and assigns each one to an email address
through the portal. That person signs a desktop app in by clicking a link
emailed to them. The app never sees the licence key. Each seat holds a
limited number of computers, can move to a new computer only a limited
number of times, and has to sign in again at a fixed interval.

## Policy

The policy is set per plan; existing rows get the column defaults.

| Plan field | Default | Meaning |
|---|---|---|
| `max_devices_per_seat` | 1 | How many computers one seat may hold at once. A value below 1 is treated as 1. |
| `max_device_moves_per_30d` | 2 | How many times in any rolling 30 days a seat may sign in on a computer different from its previous one. The seat's first computer doesn't count. Signing out first doesn't avoid it: a→b→a is two moves. |
| `reauth_days` | 180 | Days after an email sign-in before that computer must sign in again. 0 means never. The token's `exp` never goes past this point. |
| `require_seat_signin` | false | When true, `/license/activate` refuses new key activations (403 `SEAT_SIGNIN_REQUIRED`) and `/license/verify` stops renewing the key's remaining devices. |

There is also one licence field:

- `owner_seat_released` (default false). On a per-seat plan the owner is seat #1. Setting this to true frees that seat for an invitee and signs the owner's own devices out. The owner sets it from the portal (`POST /portal/licenses/owner-seat`).

Seat sign-in needs a **hybrid** product, because it uses both seats and
device activations. On a desktop-only product the start request is
accepted, but no link is ever sent.

## Wire protocol

All endpoints are under `/api/v1` and use the usual
`{success, data | error}` envelope.

### `POST /license/seat/signin/start`

Request:
```json
{"email", "product_id", "identifier", "label", "fingerprint"}
```

Response:
```json
{"request_id", "poll_secret", "interval": 3, "expires_in": 900}
```

- The response is identical whether or not the address holds a seat, so it can't be used to find out which addresses do. A link is mailed only when it does.
- Each address gets at most 3 requests per 10 minutes, counted the same way for every address. Beyond that the server returns 429 `RATE_LIMITED`.
- Requests are also limited per IP (`RATE_LIMIT_OTP_SEND` per hour).
- `identifier` is the app's install ID.
- `fingerprint` is a stable hash of the machine. The app should salt it.

### `POST /license/seat/signin/poll`

Request:
```json
{"request_id", "poll_secret"}
```

The response `status` is one of:

- `pending`: keep polling.
- `expired`: the link wasn't used within 15 minutes, or this result has already been collected.
- `refused`: comes with a `refuse_code`:
  - `DEVICE_TRANSFER_LIMIT`, plus `next_move_at`, which is null when the plan allows no moves;
  - `NO_SEAT`.
- `approved`: comes with `token`, `device_secret`, `email`, `license_id`, `plan_id`, `plan_name`, `valid_until`, `features`, `grace_days`, `reauth_due`.
  - **This is returned once.** The next poll says `expired`.

A wrong secret or an unknown ID returns 404 `SIGNIN_INVALID`.

### `POST /license/seat/signin/describe` and `/signin/approve`

Request (both endpoints):
```json
{"token"}
```

These are used by the `/desktop-signin?t=…` page.

- `describe` only reads. Mail scanners that fetch the link therefore do nothing.
- `approve` is the button. It returns:
  ```json
  {"status": "approved | refused | pending", "email", "label", "product_name", "refuse_code", "next_move_at"}
  ```
  Clicking approve again on a request that has already been decided returns that same decision.

### `POST /license/seat/verify`

Request:
```json
{"device_secret", "identifier", "fingerprint"}
```

- The app calls this to renew. It gets back the same fields as an approved poll, including a **new `device_secret`**.
- **The client must save the new secret before it treats the check-in as successful.**

Failure cases:

- **404 `LICENSE_NOT_FOUND` for every failure**: unknown secret, wrong identifier, seat removed, licence dead, the device replaced by a sign-in elsewhere, a copied device.
- **401 `REAUTH_REQUIRED`**, with `{email, reauth_due}`, when the sign-in is older than `reauth_days`. The device keeps its activation; signing in again on the same computer renews it and doesn't count as a move.
- **409 `DEVICE_SECRET_RACE`**: two check-ins from the same device overlapped. Retry. The client should only check in from one window at a time.

Rules on the device secret:

- **Grace window:** the previous secret is still accepted for 10 minutes after a rotation. This covers a response that got lost in transit.
- **After the grace window:** presenting the previous secret means two copies of the device exist. The activation is revoked, so neither copy works.
- **Fingerprint mismatch:** presenting a secret with a fingerprint that doesn't match also revokes the activation.

### `POST /license/seat/deactivate`

Request:
```json
{"device_secret", "identifier"}
```

This is "Sign out of this computer".

### Token claims

A seat-bound token adds three claims to the existing ones:

- `eml`: the seat's email address.
- `sid`: the seat ID. It is empty for the owner, who has no seat row.
- `rau`: the reauth instant, as a Unix timestamp.

The signing key is unchanged, so clients don't need to pin a new public key.

## Approval rules

When a link is approved, the following runs under the licence row lock:

1. The request must still be pending and must not have expired.
2. The seat must still exist, or the owner must still hold their seat. A pending seat invitation is **accepted**, because clicking a link mailed to the address proves the address.
3. A computer that already holds the seat has its sign-in renewed. Its secrets are cleared and the poll issues new ones. This is never counted as a move.
4. If a computer new to the seat is a move and the limit has been reached, the request is refused, and the refusal is shown on the page and returned to the app.
5. The seat's oldest computers are signed out to stay within `max_devices_per_seat`. Anything else holding this `identifier` on the licence is replaced: either a licence-key activation from before the switch, or a colleague's seat on a shared machine.

## Other changes

- **Removing a seat** signs that seat's devices out in the same transaction.
- **The key path:**
  - a key can't renew, re-activate or sign out a seat-bound device;
  - `require_seat_signin` closes key activation entirely.
- **Portal:**
  - Only the owner and accepted seat admins receive the licence key.
  - Members see only their own devices and can sign out only those.
  - A pending seat shows nothing until it is accepted.
  - Each device row shows who the device is signed in as.
- **Admin plan form:** the four policy fields appear for hybrid products.

## Tests

- **`internal/service/seat_device_integration_test.go`** (needs `TEST_DATABASE_URL`) covers:
  - the full owner flow and token claims;
  - start responses that don't reveal seats, plus the rate limit;
  - link expiry;
  - secret rotation, the grace window and reuse revocation;
  - fingerprint-mismatch revocation;
  - the move limit and its rolling window;
  - signing out first not avoiding the move limit;
  - re-sign-in at 180 days without a move;
  - member seat acceptance, the portal listing, and removal signing the device out;
  - the owner releasing their seat;
  - the key path being unable to touch seat devices, and `require_seat_signin`;
  - desktop-only products.
- **`cmd/server/seat_portal_view_test.go`** covers who gets the key and which devices each person sees.
- **`internal/handler/seat_device_policy_test.go`** covers the bounds on the plan policy fields.

Known unrelated flake: `TestQueueMarksPermanentFailureTerminal` fails occasionally in a full `go test ./...` run. It shares email settings and the email queue with tests in other packages that run at the same time. It passes on its own and when its package runs alone.

## Adopting it

Migrations only add columns and tables, so clients that activate with a
licence key keep working after an upgrade. A typical switch-over:

1. Use a **hybrid** product, and set the plan's policy fields.
2. Ship a client that signs in by email. Clients that still hold a key keep
   working on it.
3. Once no key-based clients are left, set `require_seat_signin` on the plan.

When trying it out on a separate instance, don't give that instance a
Stripe key: Keygate registers a webhook endpoint in Stripe on startup
whenever a key is set.
