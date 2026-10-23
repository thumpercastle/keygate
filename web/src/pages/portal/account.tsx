import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { showToast } from "@/components/toast"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/hooks/use-auth"
import { useI18n } from "@/i18n"
import { portal } from "@/lib/api"

// The account page holds what a customer can change about themselves:
// the name shown on their licences and seats. Licences have their own
// page; the email is the sign-in identity and is shown, not edited.
export default function PortalAccountPage() {
  const { t } = useI18n()
  const { user, refetch } = useAuth()
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState("")

  const saveMut = useMutation({
    mutationFn: () => portal.updateProfile({ name: name.trim() }),
    onSuccess: () => {
      setEditing(false)
      refetch() // Refresh auth user context
      qc.invalidateQueries({ queryKey: ["portal"] })
      showToast(t("portal.profileSaved"), "success")
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const startEdit = () => {
    setName(user?.name || "")
    setEditing(true)
  }

  const cancelEdit = () => {
    setEditing(false)
    setName("")
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">{t("portal.account")}</h1>

      <section className="rounded-xl border bg-card text-card-foreground divide-y">
        <div className="flex items-center justify-between gap-4 px-6 py-4">
          <div className="min-w-0 flex-1">
            {editing ? (
              <form
                className="flex items-end gap-2"
                onSubmit={(e) => {
                  e.preventDefault()
                  if (name.trim()) saveMut.mutate()
                }}
              >
                <div className="flex-1 space-y-1.5">
                  <Label htmlFor="profile-name">{t("portal.displayName")}</Label>
                  <Input
                    id="profile-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    maxLength={100}
                    autoFocus
                    onKeyDown={(e) => {
                      if (e.key === "Escape") cancelEdit()
                    }}
                  />
                </div>
                <Button type="submit" size="sm" disabled={!name.trim() || saveMut.isPending}>
                  {t("portal.saveProfile")}
                </Button>
                <Button type="button" size="sm" variant="ghost" onClick={cancelEdit}>
                  {t("common.cancel")}
                </Button>
              </form>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">{t("portal.displayName")}</p>
                <p className="font-medium truncate">{user?.name || t("portal.noName")}</p>
              </>
            )}
          </div>
          {!editing && (
            <Button variant="outline" size="sm" onClick={startEdit}>
              {t("portal.editProfile")}
            </Button>
          )}
        </div>
        <div className="px-6 py-4">
          <p className="text-sm text-muted-foreground">{t("common.email")}</p>
          <p className="font-medium truncate">{user?.email}</p>
          <p className="text-xs text-muted-foreground mt-1">{t("portal.emailSignInNote")}</p>
        </div>
      </section>
    </div>
  )
}
