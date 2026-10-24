import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Code, Eye, Mail, Pencil, RotateCcw, Send } from "lucide-react"
import { useState } from "react"
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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogBody, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useI18n } from "@/i18n"
import { admin } from "@/lib/api"

// Every email the server sends, in the order a customer meets them.
// SiteName, PortalURL and BrandColor are available in every template
// (see EmailService.templateData); they are listed last on each. A
// template can set its own subject with {{define "subject"}}…{{end}}.
const TEMPLATE_META: Record<string, { label: string; variables: string[] }> = {
  otp_code: { label: "Sign-in code", variables: ["Code", "SiteName", "PortalURL", "BrandColor"] },
  welcome: { label: "Welcome", variables: ["Name", "SiteName", "PortalURL", "BrandColor"] },
  license_created: {
    label: "Licence ready",
    variables: ["Product", "Plan", "LicenseKey", "DownloadURL", "SiteName", "PortalURL", "BrandColor"],
  },
  seat_invite: {
    label: "Seat invitation",
    variables: ["Product", "Inviter", "InviteURL", "DownloadURL", "SiteName", "PortalURL", "BrandColor"],
  },
  desktop_signin: {
    label: "App sign-in link",
    variables: ["Product", "Device", "Link", "SiteName", "PortalURL", "BrandColor"],
  },
  admin_invite: {
    label: "Admin added",
    variables: ["Inviter", "Role", "LoginURL", "SiteName", "PortalURL", "BrandColor"],
  },
  trial_ending: {
    label: "Trial ending (subscription)",
    variables: ["Product", "TrialEnd", "SiteName", "PortalURL", "BrandColor"],
  },
  license_expiring: {
    label: "Licence or trial expiring",
    variables: ["Product", "LicenseKey", "ExpiresAt", "DaysLeft", "IsTrial", "SiteName", "PortalURL", "BrandColor"],
  },
  trial_expired: { label: "Trial ended", variables: ["Product", "SiteName", "PortalURL", "BrandColor"] },
  renewal_reminder: {
    label: "Renewal reminder",
    variables: ["Product", "RenewalDate", "SiteName", "PortalURL", "BrandColor"],
  },
  plan_changed: {
    label: "Plan changed",
    variables: ["Product", "OldPlan", "NewPlan", "SiteName", "PortalURL", "BrandColor"],
  },
  payment_action_required: {
    label: "Payment needs confirming",
    variables: ["Product", "InvoiceURL", "SiteName", "PortalURL", "BrandColor"],
  },
  payment_failed: { label: "Payment failed", variables: ["Product", "SiteName", "PortalURL", "BrandColor"] },
  payment_overdue: {
    label: "Payment still outstanding (day 7)",
    variables: ["Product", "SiteName", "PortalURL", "BrandColor"],
  },
  payment_final_notice: {
    label: "Final payment notice (day 14)",
    variables: ["Product", "SiteName", "PortalURL", "BrandColor"],
  },
  payment_recovered: {
    label: "Payment received after failure",
    variables: ["Product", "SiteName", "PortalURL", "BrandColor"],
  },
  subscription_canceled: {
    label: "Subscription cancelled",
    variables: ["Product", "Immediate", "SiteName", "PortalURL", "BrandColor"],
  },
  license_expired: { label: "Licence expired", variables: ["Product", "SiteName", "PortalURL", "BrandColor"] },
  license_suspended: {
    label: "Licence suspended",
    variables: ["Product", "Reason", "SiteName", "PortalURL", "BrandColor"],
  },
  updates_ending: {
    label: "Updates ending",
    variables: ["Product", "LicenseKey", "UpdatesUntil", "SiteName", "PortalURL", "BrandColor"],
  },
  quota_warning: {
    label: "Usage quota warning",
    variables: ["Product", "Feature", "Used", "Limit", "Pct", "SiteName", "PortalURL", "BrandColor"],
  },
}

export default function EmailTemplatesManager() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<string | null>(null)
  const [editValue, setEditValue] = useState("")
  const [previewing, setPreviewing] = useState<string | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ["admin", "email-templates"],
    queryFn: admin.getEmailTemplates,
  })

  const saveMut = useMutation({
    mutationFn: (params: { key: string; value: string }) =>
      admin.updateSettings({ [`email_template_${params.key}`]: params.value }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "email-templates"] })
      setEditing(null)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  // confirmReset holds the template key pending revert. Reset
  // discards the admin's custom HTML and falls back to the bundled
  // default — non-recoverable without an external backup. The
  // AlertDialog stops a misclick from wiping work.
  const [confirmReset, setConfirmReset] = useState<string | null>(null)
  const resetMut = useMutation({
    mutationFn: (key: string) => admin.updateSettings({ [`email_template_${key}`]: "" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "email-templates"] })
      setConfirmReset(null)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const templates = data?.templates || {}

  const startEdit = (key: string) => {
    const tmpl = templates[key]
    setEditValue(tmpl?.custom || tmpl?.default || "")
    setEditing(key)
  }

  // Previews come from the server, so they show exactly what is sent:
  // subject, layout and attribution, with sample data. In the editor the
  // preview is of the draft, before it is saved.
  const [preview, setPreview] = useState<{ subject: string; html: string } | null>(null)
  const previewMut = useMutation({
    mutationFn: (p: { key: string; template?: string }) => admin.previewEmailTemplate(p.key, p.template),
    onSuccess: (r) => setPreview(r),
    onError: (e: Error) => showToast(e.message, "error"),
  })
  const sendTestMut = useMutation({
    mutationFn: (p: { key: string; template?: string }) => admin.sendTemplateTest(p.key, p.template),
    onSuccess: (r) => showToast(t("settings.testEmailSentTo", { email: r.to }), "success"),
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const openPreview = (key: string, draft?: string) => {
    setPreview(null)
    setPreviewing(key)
    previewMut.mutate({ key, template: draft })
  }

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <Mail className="h-4 w-4" />
            {t("settings.emailTemplates")}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <p className="text-sm text-muted-foreground mb-4">{t("settings.templateDesc")}</p>

          {isLoading ? (
            <div className="h-32 bg-muted rounded-lg animate-pulse" />
          ) : (
            <div className="space-y-2">
              {Object.entries(TEMPLATE_META).map(([key, meta]) => {
                const tmpl = templates[key]
                const isCustomized = !!tmpl?.custom

                return (
                  <div key={key} className="flex items-center justify-between p-3 border rounded-lg">
                    <div className="flex items-center gap-3">
                      <Code className="h-4 w-4 text-muted-foreground" />
                      <div>
                        <span className="text-sm font-medium">{meta.label}</span>
                        {isCustomized && (
                          <Badge variant="secondary" className="ml-2 text-xs">
                            customized
                          </Badge>
                        )}
                        <div className="text-xs text-muted-foreground mt-0.5">
                          {meta.variables.map((v) => `{{.${v}}}`).join(", ")}
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        onClick={() => openPreview(key)}
                        title={t("settings.templatePreview")}
                      >
                        <Eye className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        disabled={sendTestMut.isPending}
                        onClick={() => sendTestMut.mutate({ key })}
                        title={t("settings.sendTestEmailToMe")}
                        aria-label={t("settings.sendTestEmailToMe")}
                      >
                        <Send className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8"
                        onClick={() => startEdit(key)}
                        title={t("settings.editTemplate")}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                      {isCustomized && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 text-orange-600"
                          onClick={() => setConfirmReset(key)}
                          title={t("settings.resetTemplate")}
                          disabled={resetMut.isPending && resetMut.variables === key}
                        >
                          <RotateCcw className="h-4 w-4" />
                        </Button>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </CardContent>
      </Card>

      {/* Edit Dialog */}
      <Dialog open={!!editing} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>
              {t("settings.editTemplate")}: {editing && TEMPLATE_META[editing]?.label}
            </DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="space-y-3">
              <div className="text-xs text-muted-foreground">
                {t("settings.templateVariables")}:{" "}
                {editing && TEMPLATE_META[editing]?.variables.map((v) => `{{.${v}}}`).join(", ")}
              </div>
              <textarea
                className="w-full h-64 font-mono text-xs p-3 border rounded-lg bg-muted/50 resize-y focus:outline-none focus:ring-2 focus:ring-primary"
                value={editValue}
                onChange={(e) => setEditValue(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("settings.templateSubjectHint")}</p>
              <div className="flex justify-end gap-2">
                <Button variant="outline" onClick={() => editing && openPreview(editing, editValue)}>
                  {t("settings.templatePreview")}
                </Button>
                <Button
                  variant="outline"
                  disabled={sendTestMut.isPending}
                  onClick={() => editing && sendTestMut.mutate({ key: editing, template: editValue })}
                >
                  {t("settings.sendTestEmailToMe")}
                </Button>
                <Button variant="outline" onClick={() => setEditing(null)}>
                  {t("common.cancel")}
                </Button>
                <Button
                  onClick={() => editing && saveMut.mutate({ key: editing, value: editValue })}
                  disabled={saveMut.isPending}
                >
                  {saveMut.isPending ? t("common.loading") : t("common.save")}
                </Button>
              </div>
            </div>
          </DialogBody>
        </DialogContent>
      </Dialog>

      {/* Preview Dialog — uses sandboxed iframe for safe HTML rendering */}
      <Dialog open={!!previewing} onOpenChange={(open) => !open && setPreviewing(null)}>
        <DialogContent className="max-w-2xl h-[min(760px,85vh)]">
          <DialogHeader>
            <DialogTitle>
              {t("settings.templatePreview")}: {previewing && TEMPLATE_META[previewing]?.label}
            </DialogTitle>
          </DialogHeader>
          <DialogBody>
            {previewing &&
              (preview ? (
                <div className="space-y-2">
                  <p className="text-sm">
                    <span className="text-muted-foreground">{t("settings.templateSubject")}:</span> {preview.subject}
                  </p>
                  <iframe
                    title="Email Preview"
                    sandbox=""
                    srcDoc={preview.html}
                    className="w-full h-[28rem] border rounded-lg bg-white"
                  />
                </div>
              ) : (
                <div className="h-96 animate-pulse bg-muted rounded-lg" />
              ))}
          </DialogBody>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!confirmReset} onOpenChange={() => setConfirmReset(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("settings.resetTemplateTitle", {
                template: (confirmReset && TEMPLATE_META[confirmReset]?.label) || "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>{t("settings.resetTemplateDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => confirmReset && resetMut.mutate(confirmReset)}
            >
              {t("settings.resetTemplate")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
