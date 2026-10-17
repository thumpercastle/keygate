import { useMutation, useQuery } from "@tanstack/react-query"
import { CheckCircle2, Monitor, XCircle } from "lucide-react"
import { useSearchParams } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useSiteConfig } from "@/hooks/use-site-config"
import { useI18n } from "@/i18n"
import { type DesktopSignin, desktopSignin } from "@/lib/api"

// The page an emailed desktop sign-in link opens. Opening it changes
// nothing — mail scanners fetch links in messages before the person
// does, so the sign-in only happens on the Approve button (a POST).
export default function DesktopSigninPage() {
  const { t } = useI18n()
  const [params] = useSearchParams()
  const token = params.get("t") || ""

  const describe = useQuery({
    queryKey: ["desktop-signin", token],
    queryFn: () => desktopSignin.describe(token),
    enabled: !!token,
    retry: false,
  })
  const approve = useMutation({ mutationFn: () => desktopSignin.approve(token) })

  if (!token || describe.isError || approve.isError) {
    return (
      <Shell>
        <Outcome
          ok={false}
          title={t("desktopSignin.invalidTitle")}
          desc={approve.error?.message && !describe.isError ? approve.error.message : t("desktopSignin.invalidDesc")}
        />
      </Shell>
    )
  }
  if (describe.isPending) {
    return (
      <Shell>
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      </Shell>
    )
  }

  const req: DesktopSignin = approve.data ?? describe.data
  const product = req.product_name || t("desktopSignin.theApp")
  const device = req.label || t("desktopSignin.thisComputer")

  if (req.status === "approved") {
    return (
      <Shell>
        <Outcome
          ok
          title={t("desktopSignin.approvedTitle", { product })}
          desc={t("desktopSignin.approvedDesc", { device, email: req.email })}
        />
      </Shell>
    )
  }
  if (req.status === "refused") {
    const desc =
      req.refuse_code === "DEVICE_TRANSFER_LIMIT"
        ? req.next_move_at
          ? t("desktopSignin.moveLimitDesc", { date: new Date(req.next_move_at).toLocaleDateString() })
          : t("desktopSignin.moveLimitNever")
        : t("desktopSignin.noSeatDesc")
    return (
      <Shell>
        <Outcome ok={false} title={t("desktopSignin.refusedTitle")} desc={desc} />
      </Shell>
    )
  }

  return (
    <Shell>
      <Card className="max-w-md w-full">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Monitor className="h-5 w-5" />
            {t("desktopSignin.title", { product })}
          </CardTitle>
          <CardDescription>{t("desktopSignin.desc", { device, product, email: req.email })}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">{t("desktopSignin.oneComputer")}</p>
          <Button className="w-full" onClick={() => approve.mutate()} disabled={approve.isPending}>
            {approve.isPending ? t("common.loading") : t("desktopSignin.approve")}
          </Button>
          <p className="text-xs text-muted-foreground">{t("desktopSignin.notYou")}</p>
        </CardContent>
      </Card>
    </Shell>
  )
}

function Outcome({ ok, title, desc }: { ok: boolean; title: string; desc: string }) {
  return (
    <Card className="max-w-md w-full">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {ok ? (
            <CheckCircle2 className="h-5 w-5 text-emerald-600" />
          ) : (
            <XCircle className="h-5 w-5 text-destructive" />
          )}
          {title}
        </CardTitle>
        <CardDescription>{desc}</CardDescription>
      </CardHeader>
    </Card>
  )
}

function Shell({ children }: { children: React.ReactNode }) {
  const { attribution_text, attribution_url } = useSiteConfig()
  return (
    <div className="min-h-screen flex flex-col items-center justify-center p-4">
      {children}
      {/* Attribution required by AGPL v3 Section 7(b) — see NOTICE */}
      <a
        href={attribution_url}
        target="_blank"
        rel="noopener noreferrer"
        className="mt-4 text-[10px] text-muted-foreground/40 hover:text-muted-foreground transition-colors"
      >
        {attribution_text}
      </a>
    </div>
  )
}
