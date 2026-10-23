import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, ChevronDown, Copy, Trash2 } from "lucide-react"
import { type ReactNode, useState } from "react"
import { showToast } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useAuth } from "@/hooks/use-auth"
import { type TranslationKeys, useI18n } from "@/i18n"
import {
  type Activation,
  type Entitlement,
  type Invoice,
  type Plan,
  type PortalLicense,
  portal,
  type Seat,
} from "@/lib/api"
import { cn, formatDate, formatDay, statusColor } from "@/lib/utils"

export default function PortalLicensesPage() {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "licenses"],
    queryFn: portal.licenses,
  })

  const licenses = data?.licenses || []
  // Renewals can be switched off server-side (during a rollout, or
  // when the merchant stops selling them); don't offer what the
  // endpoint would refuse. Only an explicit true counts: a replica on
  // a version without the field has no renewal endpoint either.
  const renewalsEnabled = data?.renewals_enabled === true

  if (isLoading) {
    return (
      <div className="space-y-4">
        {[1, 2].map((i) => (
          <div key={i} className="h-48 animate-pulse bg-muted rounded-lg" />
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">{t("portal.myLicenses")}</h1>

      {licenses.length === 0 ? (
        <div className="rounded-xl border bg-card py-12 px-6 text-center">
          <p className="font-medium">{t("portal.noLicenses")}</p>
          <p className="text-sm text-muted-foreground mt-1">{t("portal.noLicensesDesc")}</p>
        </div>
      ) : (
        <div className="space-y-6">
          {licenses.map((lic) => (
            <LicenseCard key={lic.id} license={lic} renewalsEnabled={renewalsEnabled} />
          ))}
        </div>
      )}
    </div>
  )
}

function LicenseCard({ license: lic, renewalsEnabled }: { license: PortalLicense; renewalsEnabled: boolean }) {
  const { t } = useI18n()
  const { user } = useAuth()
  // Seat members see the license too, but renewals, plan changes,
  // cancellation and payment methods belong to the license holder;
  // the server authorises those by the license's own address.
  const isOwner = !!user?.email && user.email.toLowerCase() === lic.email.toLowerCase()
  const [copied, setCopied] = useState(false)
  const [showKey, setShowKey] = useState(false)
  const [showInvoices, setShowInvoices] = useState(false)
  const [showChangePlan, setShowChangePlan] = useState(false)
  const [showChangeSeats, setShowChangeSeats] = useState(false)
  const [showUpgrade, setShowUpgrade] = useState(false)
  const [showCancel, setShowCancel] = useState(false)
  const productType = lic.product?.type || "perpetual"
  // Seats UI shows for any plan that *could* have more than one seat.
  // Backend treats max_seats=0 as "no limit", so the only case we hide
  // is max_seats=1 (true single-seat). Perpetual products don't have
  // a portal-managed team, so gate on product type too.
  const seatCap = lic.effective_max_seats ?? lic.plan?.max_seats ?? 0
  const perSeat = lic.plan?.per_seat === true
  const hasSeatCapability = productType === "saas" || productType === "hybrid"
  const embeddedActiveSeats = lic.seats?.filter((seat) => !seat.removed_at).length ?? 0
  // Seat management needs the key (it names the licence on the seat
  // endpoints), which only the owner and seat admins receive.
  const showSeats =
    !!lic.license_key &&
    hasSeatCapability &&
    (perSeat ? seatCap === 0 || seatCap > 1 || embeddedActiveSeats > 0 : seatCap !== 1)
  const entitlements = lic.plan?.entitlements || []
  const hasQuota =
    (productType === "saas" || productType === "hybrid") && entitlements.some((e) => e.value_type === "quota")
  // A plan that requires seat sign-in never activates by key: people
  // sign in with their email, so the key is not something to hand out.
  const keyUsable = !!lic.license_key && lic.plan?.require_seat_signin !== true

  const copyKey = () => {
    navigator.clipboard.writeText(lic.license_key)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  // Change plan, cancel and payment-method updates all act on a Stripe
  // subscription. A one-time purchase has none: the backend answers 400
  // to each, so don't offer them.
  const hasSubscription = lic.payment_provider === "stripe" && !!lic.stripe_subscription_id
  const live = lic.status === "active" || lic.status === "trialing"
  const canChangeSeats = isOwner && hasSubscription && perSeat && (live || lic.status === "past_due")
  const canChangePlan = isOwner && hasSubscription && live
  const hasBilling = isOwner && lic.payment_provider === "stripe"
  // A licence on a free plan upgrades through a checkout and keeps its
  // key. The server says whether this one can (Stripe configured, a free
  // plan, not suspended or revoked, and a paid plan to move to).
  const canUpgrade = isOwner && lic.upgradable === true

  // Maintenance period of a perpetual license. The license never
  // expires; updates_until only bounds which releases may be installed.
  const isPerpetualPlan = lic.plan?.license_type === "perpetual"
  const updatesUntil = lic.updates_until ? new Date(lic.updates_until) : null
  const updatesEnded = updatesUntil !== null && updatesUntil.getTime() < Date.now()
  const updatesEndingSoon =
    updatesUntil !== null && !updatesEnded && updatesUntil.getTime() - Date.now() < 30 * 24 * 60 * 60 * 1000
  const canRenewUpdates =
    renewalsEnabled &&
    isOwner &&
    isPerpetualPlan &&
    updatesUntil !== null &&
    lic.status === "active" &&
    lic.plan?.active !== false &&
    (lic.plan?.renewal_days ?? 0) > 0 &&
    !!lic.plan?.stripe_renewal_price_id
  const [renewing, setRenewing] = useState(false)
  const handleRenewUpdates = async () => {
    setRenewing(true)
    try {
      const res = await portal.renewUpdates({ license_id: lic.id })
      window.location.href = res.url
    } catch (err) {
      showToast(err instanceof Error ? err.message : t("portal.somethingWentWrong"))
      setRenewing(false)
    }
  }

  const handleBillingPortal = async () => {
    try {
      const res = await portal.getBillingPortal({ license_id: lic.id })
      window.location.href = res.url
    } catch (err) {
      showToast(err instanceof Error ? err.message : t("portal.somethingWentWrong"))
    }
  }

  // One line that answers "what do I have": seats, devices, and when it
  // ends. A subscription renews; anything else just has an end date.
  const deviceCap = !lic.license_key
    ? (lic.plan?.max_devices_per_seat ?? 1)
    : perSeat
      ? (lic.effective_max_activations ?? 0)
      : (lic.plan?.max_activations ?? 0)
  const deviceCount = lic.activations?.length || 0
  const summary: string[] = []
  if (hasSeatCapability && (perSeat || showSeats)) {
    summary.push(seatCap === 0 ? t("portal.seatsUnlimited") : t("portal.seatsCount", { count: seatCap }))
  }
  summary.push(
    deviceCap > 0
      ? t("portal.devicesOfMax", { count: deviceCount, max: deviceCap })
      : t("portal.devicesCount", { count: deviceCount }),
  )
  if (lic.valid_until) {
    summary.push(t(hasSubscription && live ? "portal.renewsOn" : "portal.endsOn", { date: formatDay(lic.valid_until) }))
  } else if (!isPerpetualPlan) {
    summary.push(t("portal.noEndDate"))
  }

  // Sections in the order owners reach for them; tabs only when there
  // is more than one.
  const sections: { id: string; label: string; node: ReactNode }[] = []
  if (showSeats) sections.push({ id: "team", label: t("portal.teamMembers"), node: <SeatsSection license={lic} /> })
  sections.push({ id: "devices", label: t("portal.devices"), node: <ActivationsSection license={lic} /> })
  if (entitlements.length > 0) {
    sections.push({
      id: "features",
      label: t("portal.featuresIncluded"),
      node: <EntitlementsSection entitlements={entitlements} />,
    })
  }
  if (hasQuota) sections.push({ id: "usage", label: t("analytics.usage"), node: <QuotaUsageSection license={lic} /> })

  return (
    <section className="rounded-xl border bg-card text-card-foreground">
      <div className="p-6 space-y-5">
        <div className="flex items-start justify-between gap-4 flex-wrap">
          <div className="min-w-0 space-y-1">
            <div className="flex items-center gap-2 flex-wrap">
              <h2 className="text-lg font-semibold leading-tight">{lic.plan?.name || lic.product?.name}</h2>
              {lic.status !== "active" && (
                <Badge className={statusColor(lic.status)}>{t(`status.${lic.status}` as TranslationKeys)}</Badge>
              )}
            </div>
            <p className="text-sm text-muted-foreground">{summary.join(" · ")}</p>
          </div>

          {(canUpgrade || canChangeSeats || canChangePlan || hasBilling || keyUsable) && (
            <div className="flex items-center gap-2 flex-wrap">
              {canUpgrade && (
                <Button size="sm" onClick={() => setShowUpgrade(true)}>
                  {t("portal.upgrade")}
                </Button>
              )}
              {canChangeSeats && (
                <Button size="sm" onClick={() => setShowChangeSeats(true)}>
                  {t("portal.changeSeats")}
                </Button>
              )}
              {canChangePlan && (
                <Button size="sm" variant="outline" onClick={() => setShowChangePlan(true)}>
                  {t("portal.changePlan")}
                </Button>
              )}
              {(hasBilling || keyUsable) && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button size="sm" variant="outline">
                      {hasBilling ? t("portal.billing") : t("portal.more")}
                      <ChevronDown className="h-3.5 w-3.5 ml-1 opacity-60" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {hasBilling && (
                      <DropdownMenuItem onClick={() => setShowInvoices(true)}>
                        {t("portal.viewInvoices")}
                      </DropdownMenuItem>
                    )}
                    {hasBilling && hasSubscription && (
                      <DropdownMenuItem onClick={handleBillingPortal}>{t("portal.updatePayment")}</DropdownMenuItem>
                    )}
                    {keyUsable && (
                      <DropdownMenuItem onClick={() => setShowKey((v) => !v)}>
                        {showKey ? t("portal.hideKey") : t("portal.showKey")}
                      </DropdownMenuItem>
                    )}
                    {hasBilling && hasSubscription && live && (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem className="text-destructive" onClick={() => setShowCancel(true)}>
                          {t("portal.cancelSubscription")}
                        </DropdownMenuItem>
                      </>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </div>
          )}
        </div>

        {keyUsable && showKey && (
          <div className="flex items-center gap-2 rounded-lg bg-muted px-3 py-2">
            <code className="text-sm flex-1 truncate">{lic.license_key}</code>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 shrink-0"
              onClick={copyKey}
              aria-label={t("portal.copyKey")}
            >
              {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
            </Button>
          </div>
        )}

        {/* Maintenance period */}
        {isPerpetualPlan && (
          <div className="flex items-center justify-between gap-3 flex-wrap text-sm">
            <p className={cn(updatesEnded && "text-muted-foreground", updatesEndingSoon && "text-destructive")}>
              {updatesUntil === null
                ? t("portal.updatesForLife")
                : updatesEnded
                  ? t("portal.updatesEnded", { date: formatDate(lic.updates_until) })
                  : `${t("portal.updatesUntil")} ${formatDate(lic.updates_until)}${updatesEndingSoon ? ` · ${t("portal.updatesEndingSoon")}` : ""}`}
            </p>
            {canRenewUpdates && (
              <Button
                size="sm"
                variant={updatesEnded || updatesEndingSoon ? "default" : "outline"}
                onClick={handleRenewUpdates}
                disabled={renewing}
              >
                {renewing ? t("common.loading") : t("portal.renewUpdates")}
              </Button>
            )}
          </div>
        )}
      </div>

      <div className="border-t px-6 py-5">
        {sections.length === 1 ? (
          sections[0].node
        ) : (
          <Tabs defaultValue={sections[0].id}>
            <TabsList>
              {sections.map((s) => (
                <TabsTrigger key={s.id} value={s.id}>
                  {s.label}
                </TabsTrigger>
              ))}
            </TabsList>
            {sections.map((s) => (
              <TabsContent key={s.id} value={s.id} className="mt-4">
                {s.node}
              </TabsContent>
            ))}
          </Tabs>
        )}
      </div>

      {showCancel && (
        <CancelDialog
          licenseId={lic.id}
          provider={lic.payment_provider || ""}
          productName={lic.product?.name || ""}
          onClose={() => setShowCancel(false)}
        />
      )}
      {showInvoices && <InvoicesDialog licenseId={lic.id} onClose={() => setShowInvoices(false)} />}
      {showChangePlan && <ChangePlanDialog license={lic} onClose={() => setShowChangePlan(false)} />}
      {showChangeSeats && <ChangeSeatsDialog license={lic} onClose={() => setShowChangeSeats(false)} />}
      {showUpgrade && <UpgradeDialog license={lic} onClose={() => setShowUpgrade(false)} />}
    </section>
  )
}

function ActivationsSection({ license }: { license: PortalLicense }) {
  const { t } = useI18n()
  const { user } = useAuth()
  const qc = useQueryClient()
  const [removing, setRemoving] = useState<Activation | null>(null)

  // The dedicated activations endpoint requires the caller to be the
  // license owner OR an *accepted* seat (portal_activations.go:84 —
  // "pending invites confer no authority", a deliberate security
  // gate). But /portal/licenses still lists the license for a
  // PENDING (unaccepted) seat too, so naively calling the endpoint
  // here would 404 and render a false "no devices" for a license we
  // just showed. Decide authorisation from the embedded data first;
  // only hit the gated endpoint when authorised. Unauthorised users
  // fall back to the read-only embedded list (the prior behaviour —
  // no regression, no false-empty, no delete controls).
  const myEmail = user?.email?.toLowerCase() ?? ""
  const isOwner = !!myEmail && license.email?.toLowerCase() === myEmail
  const mySeat = (license.seats || []).find((s) => s.email.toLowerCase() === myEmail && !s.removed_at)
  const isAcceptedSeat = !!mySeat?.accepted_at
  // Managing devices goes through the key-addressed endpoint, so only
  // callers who were given the key (owner, seat admins) take that path;
  // a member reads the embedded list, which the server has already
  // narrowed to their own devices.
  const authorized = (isOwner || isAcceptedSeat) && !!license.license_key

  const actQuery = useQuery({
    queryKey: ["portal", "activations", license.id],
    queryFn: () => portal.listActivations(license.license_key),
    enabled: authorized,
  })

  // Declared before any early return so the hook order is stable
  // every render (rules of hooks). Only exercised on the authorised
  // manage path below.
  const removeMut = useMutation({
    mutationFn: (activationId: string) => portal.removeActivation(license.license_key, activationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "activations", license.id] })
      // The card header's "N / max" count is rendered from
      // lic.activations, which comes from the separate
      // ["portal","licenses"] query — invalidate it too, else the
      // header keeps showing the pre-delete count while the list
      // below already dropped the row.
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
    },
    // Errors surface via the global MutationCache toast (main.tsx).
    // Just close the dialog either way so the row doesn't get stuck.
    onSettled: () => {
      setRemoving(null)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  // Read-only path for unauthorised (pending-seat) viewers, and a
  // defensive fallback if the gated query errors for an authorised
  // user (race / transient). Uses the activations embedded in the
  // ["portal","licenses"] payload, which is populated regardless of
  // seat-accept state — so we never claim "no devices" falsely.
  const useReadOnly = !authorized || actQuery.isError
  if (useReadOnly) {
    const embedded = license.activations || []
    // A member (no key) is limited per seat, not by the licence's pool.
    const effectiveMax = !license.license_key
      ? (license.plan?.max_devices_per_seat ?? 1)
      : (license.effective_max_activations ?? license.plan?.max_activations ?? 0)
    const overLimit = license.plan?.per_seat === true && effectiveMax > 0 && embedded.length > effectiveMax
    return (
      <div className="space-y-3">
        {overLimit && <p className="text-sm text-destructive">{t("portal.activationsOverLimit")}</p>}
        {embedded.length === 0 ? (
          <p className="text-sm text-muted-foreground py-4 text-center">{t("portal.noDevices")}</p>
        ) : (
          <div className="divide-y">
            {embedded.map((act) => (
              <div key={act.id} className="flex items-center justify-between gap-3 py-2.5 text-sm">
                <div className="min-w-0 flex-1">
                  <p className="font-medium truncate">{act.label || t("portal.unnamedDevice")}</p>
                  <p className="text-xs text-muted-foreground mt-0.5 truncate">
                    {act.seat_email && <>{act.seat_email} · </>}
                    {t("portal.lastSeen", { date: formatDay(act.last_verified) })}
                  </p>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    )
  }

  const activations = actQuery.data?.activations || []
  const max = license.effective_max_activations ?? actQuery.data?.max ?? 0
  const overLimit = license.plan?.per_seat === true && max > 0 && activations.length > max

  return (
    <div className="space-y-3">
      {overLimit && <p className="text-sm text-destructive">{t("portal.activationsOverLimit")}</p>}

      {actQuery.isLoading ? (
        <div className="h-16 animate-pulse bg-muted rounded-lg" />
      ) : activations.length === 0 ? (
        <p className="text-sm text-muted-foreground py-4 text-center">{t("portal.noDevices")}</p>
      ) : (
        <div className="divide-y">
          {activations.map((act) => (
            <div key={act.id} className="flex items-center justify-between gap-3 py-2.5 text-sm">
              <div className="min-w-0 flex-1">
                <p className="font-medium truncate">{act.label || t("portal.unnamedDevice")}</p>
                <p className="text-xs text-muted-foreground mt-0.5 truncate">
                  {act.seat_email && <>{act.seat_email} · </>}
                  {t("portal.lastSeen", { date: formatDay(act.last_verified) })}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 shrink-0 text-destructive"
                disabled={removeMut.isPending && removeMut.variables === act.id}
                onClick={() => setRemoving(act)}
                aria-label={t("portal.removeDevice")}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}

      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("portal.removeDeviceTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("portal.removeDeviceDesc").replace("{device}", removing?.label || removing?.identifier || "")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => removing && removeMut.mutate(removing.id)}
            >
              {t("portal.removeDevice")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function EntitlementsSection({ entitlements }: { entitlements: Entitlement[] }) {
  const { t } = useI18n()
  if (entitlements.length === 0) {
    return <p className="text-sm text-muted-foreground py-4 text-center">{t("portal.noEntitlements")}</p>
  }

  return (
    <div className="space-y-2">
      {entitlements.map((ent) => (
        <div key={ent.id} className="flex items-center justify-between gap-3 py-2.5 text-sm">
          <div className="flex items-center gap-2">
            <Check className="h-4 w-4 text-primary shrink-0" />
            <span className="font-medium">{ent.feature}</span>
          </div>
          <div className="text-muted-foreground text-xs">
            {ent.value_type === "boolean" ? (
              <Badge variant="secondary">{t("common.active")}</Badge>
            ) : ent.value_type === "quota" ? (
              <span>
                {formatNumber(Number(ent.value))} {ent.quota_unit || "units"} / {ent.quota_period || "period"}
              </span>
            ) : (
              <span>{ent.value}</span>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}

function SeatsSection({ license }: { license: PortalLicense }) {
  const { t } = useI18n()
  const { user } = useAuth()
  const qc = useQueryClient()
  const [showInvite, setShowInvite] = useState(false)
  const [removing, setRemoving] = useState<Seat | null>(null)

  const seatsQuery = useQuery({
    queryKey: ["portal", "seats", license.id],
    queryFn: () => portal.listSeats(license.license_key),
  })

  const removeMut = useMutation({
    mutationFn: (seatId: string) => portal.removeSeat({ license_key: license.license_key, seat_id: seatId }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "seats", license.id] })
      setRemoving(null)
    },
    // Errors surface via the global MutationCache toast handler in
    // main.tsx — no per-mutation onError needed, else we'd double-
    // toast. Just close the dialog on failure so the row stays put.
    onSettled: () => {
      setRemoving(null)
    },
  })

  const seats = seatsQuery.data?.seats || []
  const activeCount = seats.filter((s) => !s.removed_at).length
  // maxSeats=0 means "no limit" per backend (internal/store/seats.go:81);
  // never gate the Add button in that case.
  const perSeat = license.plan?.per_seat === true
  const maxSeats = license.effective_max_seats ?? license.plan?.max_seats ?? 0
  // On a per-seat plan the owner is seat #1 unless they released it.
  const ownerHoldsSeat = perSeat && !license.owner_seat_released
  const invitationCap = perSeat && maxSeats > 0 ? Math.max(0, maxSeats - (ownerHoldsSeat ? 1 : 0)) : maxSeats
  const memberCount = activeCount + (ownerHoldsSeat ? 1 : 0)
  const atLimit = invitationCap > 0 ? activeCount >= invitationCap : perSeat && maxSeats > 0
  const overLimit = perSeat && maxSeats > 0 && activeCount > invitationCap

  // canManage mirrors backend portalSeatMutationGuard: the license
  // owner (matched by email, case-insensitive) OR an accepted seat
  // with role=admin. Members and unaccepted invites only see the
  // roster; hiding the buttons prevents the "click → mystery 404 →
  // generic toast" UX failure mode.
  const myEmail = user?.email?.toLowerCase() ?? ""
  const isLicenseOwner = !!myEmail && license.email?.toLowerCase() === myEmail
  const mySeat = seats.find((s) => s.email.toLowerCase() === myEmail && !s.removed_at)
  const isAcceptedAdmin = !!mySeat?.accepted_at && mySeat.role === "admin"
  const canManage = isLicenseOwner || isAcceptedAdmin

  const ownerSeatMut = useMutation({
    mutationFn: (released: boolean) => portal.setOwnerSeat({ license_id: license.id, released }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
      qc.invalidateQueries({ queryKey: ["portal", "activations", license.id] })
    },
  })

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          {maxSeats === 0
            ? t("portal.seatsUsedUnlimited", { used: memberCount })
            : t("portal.seatsUsed", { used: memberCount, max: maxSeats })}
        </p>
        {canManage && (
          <Button size="sm" disabled={atLimit} onClick={() => setShowInvite(true)}>
            {t("portal.addMember")}
          </Button>
        )}
      </div>

      {overLimit ? (
        <p className="text-sm text-destructive">{t("portal.seatsOverLimit")}</p>
      ) : canManage && atLimit ? (
        <p className="text-xs text-muted-foreground">{t("portal.seatLimit")}</p>
      ) : null}

      {isLicenseOwner && perSeat && (
        <div className="flex items-center justify-between gap-3 py-2.5 text-sm border-b">
          <div className="min-w-0">
            <p className="font-medium truncate">
              {license.email} <span className="font-normal text-muted-foreground">· {t("portal.you")}</span>
            </p>
            <p className="text-xs text-muted-foreground mt-0.5">
              {ownerHoldsSeat ? t("portal.ownerSeatHeld") : t("portal.ownerSeatReleased")}
            </p>
          </div>
          <Button
            size="sm"
            variant="ghost"
            className="shrink-0"
            disabled={ownerSeatMut.isPending}
            onClick={() => ownerSeatMut.mutate(ownerHoldsSeat)}
          >
            {ownerHoldsSeat ? t("portal.ownerSeatRelease") : t("portal.ownerSeatTake")}
          </Button>
        </div>
      )}

      {seatsQuery.isLoading ? (
        <div className="h-16 animate-pulse bg-muted rounded-lg" />
      ) : seats.length === 0 ? (
        <p className="text-sm text-muted-foreground py-4">{t("portal.noMembers")}</p>
      ) : (
        <div className="divide-y">
          {seats.map((s) => (
            <SeatRow
              key={s.id}
              seat={s}
              canManage={canManage}
              onRemove={() => setRemoving(s)}
              isRemoving={removeMut.isPending && removeMut.variables === s.id}
            />
          ))}
        </div>
      )}

      {showInvite && <InviteSeatDialog license={license} onClose={() => setShowInvite(false)} />}

      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("portal.removeMemberTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("portal.removeMemberDesc").replace("{email}", removing?.email || "")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => removing && removeMut.mutate(removing.id)}
            >
              {t("portal.removeMember")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function SeatRow({
  seat,
  canManage,
  onRemove,
  isRemoving,
}: {
  seat: Seat
  canManage: boolean
  onRemove: () => void
  isRemoving: boolean
}) {
  const { t } = useI18n()
  const accepted = !!seat.accepted_at
  return (
    <div className="flex items-center justify-between gap-3 py-2.5 text-sm">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="font-medium truncate">{seat.email}</span>
          {seat.role === "admin" && (
            <Badge variant="secondary" className="text-xs">
              {t("portal.roleAdmin")}
            </Badge>
          )}
        </div>
        <p className="text-xs text-muted-foreground mt-0.5">
          {accepted
            ? t("portal.joinedOn", { date: formatDay(seat.accepted_at!) })
            : t("portal.invitedOn", { date: formatDay(seat.invited_at) })}
        </p>
      </div>
      {canManage && (
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 shrink-0 text-destructive"
          disabled={isRemoving}
          onClick={onRemove}
          aria-label={t("portal.removeMember")}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      )}
    </div>
  )
}

function InviteSeatDialog({ license, onClose }: { license: PortalLicense; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [email, setEmail] = useState("")
  const [role, setRole] = useState<"member" | "admin">("member")

  const inviteMut = useMutation({
    mutationFn: () => portal.addSeat({ license_key: license.license_key, email: email.trim(), role }),
    onSuccess: () => {
      showToast(t("licenses.seatInviteSent"), "success")
      qc.invalidateQueries({ queryKey: ["portal", "seats", license.id] })
      onClose()
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const trimmed = email.trim()
  const isEmail = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)
  const canSubmit = isEmail && !inviteMut.isPending

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("licenses.inviteTitle")}</DialogTitle>
          <DialogDescription>{t("licenses.inviteDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="invite-email">{t("common.email")}</Label>
              <Input
                id="invite-email"
                type="email"
                autoComplete="off"
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="teammate@example.com"
                onKeyDown={(e) => {
                  if (e.key === "Enter" && canSubmit) inviteMut.mutate()
                }}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="invite-role">{t("team.role")}</Label>
              <Select value={role} onValueChange={(v) => setRole(v as "member" | "admin")}>
                <SelectTrigger id="invite-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="member">{t("portal.roleMember")}</SelectItem>
                  <SelectItem value="admin">{t("portal.roleAdmin")}</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button disabled={!canSubmit} onClick={() => inviteMut.mutate()}>
                {t("licenses.sendInvite")}
              </Button>
            </div>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

function QuotaUsageSection({ license }: { license: PortalLicense }) {
  const { t } = useI18n()
  const quotaEntitlements = (license.plan?.entitlements || []).filter((e) => e.value_type === "quota")

  if (quotaEntitlements.length === 0) {
    return <p className="text-sm text-muted-foreground py-4">{t("portal.noQuota")}</p>
  }

  return (
    <div className="space-y-4">
      {quotaEntitlements.map((ent) => (
        <QuotaBar key={ent.id} entitlement={ent} licenseKey={license.license_key} />
      ))}
    </div>
  )
}

function QuotaBar({ entitlement, licenseKey }: { entitlement: Entitlement; licenseKey: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "quota", licenseKey, entitlement.feature],
    queryFn: () => portal.quotaStatus({ license_key: licenseKey, feature: entitlement.feature }),
  })

  const limit = Number(entitlement.value) || 0
  const used = data?.used ?? 0
  const percentage = limit > 0 ? Math.min((used / limit) * 100, 100) : 0
  const isWarning = percentage >= 80

  if (isLoading) {
    return <div className="h-12 animate-pulse bg-muted rounded-lg" />
  }

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-sm">
        <span className="font-medium">{entitlement.feature}</span>
        <span className={cn("text-xs", isWarning ? "text-amber-600 font-medium" : "text-muted-foreground")}>
          {formatNumber(used)} / {formatNumber(limit)} ({Math.round(percentage)}%)
        </span>
      </div>
      <div className="h-2.5 bg-muted rounded-full overflow-hidden">
        <div
          className={cn(
            "h-full rounded-full transition-all",
            isWarning ? (percentage >= 95 ? "bg-red-500" : "bg-amber-500") : "bg-emerald-500",
          )}
          style={{ width: `${percentage}%` }}
        />
      </div>
      {entitlement.quota_unit && (
        <p className="text-xs text-muted-foreground">
          {entitlement.quota_unit} / {entitlement.quota_period || "period"}
        </p>
      )}
    </div>
  )
}

function formatNumber(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)} GB`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)} MB`
  return n.toLocaleString()
}

function CancelDialog({
  licenseId,
  provider,
  productName,
  onClose,
}: {
  licenseId: string
  provider: string
  productName: string
  onClose: () => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [immediate, setImmediate] = useState(false)

  const cancelMut = useMutation({
    mutationFn: () => {
      return portal.cancelSubscription({ license_id: licenseId, immediate })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
      onClose()
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("portal.cancelSubscription")}</DialogTitle>
          <DialogDescription>{t("portal.cancelDesc", { product: productName })}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div className="space-y-4">
            {provider === "stripe" && (
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={immediate}
                  onChange={(e) => setImmediate(e.target.checked)}
                  className="rounded"
                />
                {t("portal.cancelImmediately")}
              </label>
            )}
            <p className="text-sm text-muted-foreground">
              {immediate ? t("portal.cancelImmediateWarning") : t("portal.cancelEndWarning")}
            </p>
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button variant="destructive" onClick={() => cancelMut.mutate()} disabled={cancelMut.isPending}>
                {cancelMut.isPending ? t("common.loading") : t("portal.confirmCancel")}
              </Button>
            </div>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

function InvoicesDialog({ licenseId, onClose }: { licenseId: string; onClose: () => void }) {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "invoices", licenseId],
    queryFn: () => portal.getInvoices(licenseId),
  })
  const invoices = data?.invoices || []

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-2xl h-[min(640px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("portal.invoices")}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : invoices.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-8">{t("common.noData")}</p>
          ) : (
            <div className="space-y-2">
              {invoices.map((inv: Invoice) => (
                <div
                  key={inv.id}
                  className="flex items-center justify-between bg-muted/50 rounded-lg px-4 py-3 text-sm"
                >
                  <div>
                    <p className="font-medium">{inv.number || inv.id}</p>
                    <p className="text-xs text-muted-foreground">{new Date(inv.created * 1000).toLocaleDateString()}</p>
                  </div>
                  <div className="flex items-center gap-3">
                    <Badge
                      className={
                        inv.status === "paid" ? "bg-emerald-100 text-emerald-800" : "bg-amber-100 text-amber-800"
                      }
                    >
                      {inv.status}
                    </Badge>
                    <span className="font-medium">
                      {(inv.amount_paid / 100).toFixed(2)} {inv.currency?.toUpperCase()}
                    </span>
                    {inv.invoice_pdf && (
                      <Button variant="ghost" size="sm" asChild>
                        <a href={inv.invoice_pdf} target="_blank" rel="noopener noreferrer">
                          PDF
                        </a>
                      </Button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

// UpgradeDialog lists the product's paid plans for a licence nothing is
// billing yet. Picking one goes to Stripe checkout; once paid, the same
// licence moves to that plan.
function UpgradeDialog({ license, onClose }: { license: PortalLicense; onClose: () => void }) {
  const { t } = useI18n()
  const { data: plansData, isLoading } = useQuery({
    queryKey: ["portal", "plans", license.product_id],
    queryFn: () => portal.listPlans(license.product_id),
  })
  // Mirrors the server's upgrade targets: priced, not a trial, and no
  // update period on sale.
  const plans = (plansData?.plans || []).filter(
    (p: Plan) =>
      p.id !== license.plan_id &&
      !!p.stripe_price_id &&
      p.license_type !== "trial" &&
      !(p.license_type === "perpetual" && (p.updates_days ?? 0) > 0),
  )
  const [starting, setStarting] = useState("")
  const start = async (plan: Plan) => {
    setStarting(plan.id)
    try {
      const res = await portal.upgrade({ license_id: license.id, plan_id: plan.id })
      window.location.href = res.url
    } catch (err) {
      showToast(err instanceof Error ? err.message : "An error occurred", "error")
      setStarting("")
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="h-[min(560px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("portal.upgrade")}</DialogTitle>
          <DialogDescription>{t("portal.upgradeDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {isLoading ? (
            <p className="text-sm text-muted-foreground text-center py-4">{t("common.loading")}</p>
          ) : plans.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-4">{t("portal.noUpgradePlans")}</p>
          ) : (
            <div className="space-y-2">
              {plans.map((plan: Plan) => (
                <div key={plan.id} className="flex items-center justify-between bg-muted/50 rounded-lg px-4 py-3">
                  <div>
                    <p className="font-medium text-sm">{plan.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {plan.license_type}
                      {plan.billing_interval ? ` · ${plan.billing_interval}` : ""}
                    </p>
                  </div>
                  <Button size="sm" onClick={() => start(plan)} disabled={starting !== ""}>
                    {starting === plan.id ? t("common.loading") : t("portal.upgrade")}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

function ChangePlanDialog({ license, onClose }: { license: PortalLicense; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()

  // Fetch available plans via portal API (not admin)
  const { data: plansData } = useQuery({
    queryKey: ["portal", "plans", license.product_id],
    queryFn: () => portal.listPlans(license.product_id),
  })
  // Only other subscription plans with a Stripe price: a subscription
  // can move between prices, not become a one-time purchase. Mirrors
  // the backend's change-plan gate.
  const plans = (plansData?.plans || []).filter(
    (p: Plan) => p.id !== license.plan_id && p.license_type === "subscription" && p.stripe_price_id,
  )

  // Switching bills the customer at once (prorated), so it is
  // confirmed first and acknowledged after.
  const [confirming, setConfirming] = useState<Plan | null>(null)
  const changeMut = useMutation({
    mutationFn: (plan: Plan) => portal.changePlan({ license_id: license.id, new_price_id: plan.stripe_price_id || "" }),
    onSuccess: (_, plan) => {
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
      showToast(t("portal.planChanged", { plan: plan.name }), "success")
      onClose()
    },
    onError: (e: Error) => {
      setConfirming(null)
      showToast(e.message, "error")
    },
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="h-[min(560px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("portal.changePlan")}</DialogTitle>
          <DialogDescription>{t("portal.changePlanDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {plans.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-4">{t("portal.noOtherPlans")}</p>
          ) : (
            <div className="space-y-2">
              {plans.map((plan: Plan) => (
                <div key={plan.id} className="flex items-center justify-between bg-muted/50 rounded-lg px-4 py-3">
                  <div>
                    <p className="font-medium text-sm">{plan.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {plan.license_type}
                      {plan.billing_interval ? ` · ${plan.billing_interval}` : ""}
                    </p>
                  </div>
                  <Button size="sm" onClick={() => setConfirming(plan)} disabled={changeMut.isPending}>
                    {t("portal.switchTo")}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </DialogBody>
        <AlertDialog
          open={confirming !== null}
          onOpenChange={(open) => !open && !changeMut.isPending && setConfirming(null)}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("portal.confirmSwitchTitle", { plan: confirming?.name ?? "" })}</AlertDialogTitle>
              <AlertDialogDescription>{t("portal.confirmSwitchDesc")}</AlertDialogDescription>
            </AlertDialogHeader>
            <div className="flex justify-end gap-2">
              <AlertDialogCancel disabled={changeMut.isPending}>{t("common.cancel")}</AlertDialogCancel>
              <Button onClick={() => confirming && changeMut.mutate(confirming)} disabled={changeMut.isPending}>
                {changeMut.isPending ? t("common.loading") : t("portal.confirmSwitch")}
              </Button>
            </div>
          </AlertDialogContent>
        </AlertDialog>
      </DialogContent>
    </Dialog>
  )
}

// ChangeSeatsDialog sets how many seats a per-seat subscription pays
// for. Stripe prorates the difference onto the next invoice. Going
// below the seats in use removes nobody; it only stops new invitations
// until someone is removed, which the dialog says before it happens.
function ChangeSeatsDialog({ license, onClose }: { license: PortalLicense; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const min = license.plan?.min_quantity ?? 1
  const max = license.plan?.max_quantity ?? min
  const current = license.quantity ?? min
  const [quantity, setQuantity] = useState(current)
  const ownerSeat = license.owner_seat_released ? 0 : 1
  const inUse = (license.seats?.filter((seat) => !seat.removed_at).length ?? 0) + ownerSeat
  const valid = Number.isInteger(quantity) && quantity >= min && quantity <= max
  const changeMut = useMutation({
    mutationFn: () => portal.changeQuantity({ license_id: license.id, quantity }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
      showToast(t("portal.seatsChanged", { count: r.quantity }), "success")
      onClose()
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("portal.changeSeats")}</DialogTitle>
          <DialogDescription>{t("portal.changeSeatsDesc", { min, max })}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div className="space-y-2">
            <Label htmlFor="seat-quantity">{t("portal.seatCount")}</Label>
            <Input
              id="seat-quantity"
              type="number"
              min={min}
              max={max}
              value={Number.isNaN(quantity) ? "" : quantity}
              onChange={(e) => setQuantity(e.target.valueAsNumber)}
            />
            <p className="text-xs text-muted-foreground">{t("portal.seatsInUse", { used: inUse, current })}</p>
            {valid && quantity < inUse && (
              <p className="text-xs text-destructive">{t("portal.seatsBelowInUse", { used: inUse })}</p>
            )}
          </div>
          <div className="flex justify-end gap-2 mt-6">
            <Button variant="outline" onClick={onClose} disabled={changeMut.isPending}>
              {t("common.cancel")}
            </Button>
            <Button onClick={() => changeMut.mutate()} disabled={!valid || quantity === current || changeMut.isPending}>
              {changeMut.isPending
                ? t("common.loading")
                : t("portal.confirmSeats", { count: Number.isNaN(quantity) ? current : quantity })}
            </Button>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
