import { createContext, type ReactNode, useContext, useEffect, useState } from "react"
import { site } from "@/lib/api"

interface SiteConfig {
  site_name: string
  brand_color: string
  logo_url: string
  favicon_url: string
  timezone: string
  language: string
  attribution_text: string
  attribution_url: string
  loading: boolean
}

const defaults: SiteConfig = {
  site_name: "Keygate",
  brand_color: "",
  logo_url: "",
  favicon_url: "",
  timezone: "UTC",
  language: "",
  attribution_text: "Powered by Keygate",
  attribution_url: "https://keygate.app",
  loading: true,
}

const SiteConfigContext = createContext<SiteConfig>(defaults)

export function SiteConfigProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<SiteConfig>(defaults)

  useEffect(() => {
    site
      .config()
      .then((data) => {
        setConfig({
          site_name: data.site_name || "Keygate",
          brand_color: data.brand_color || "",
          logo_url: data.logo_url || "",
          favicon_url: data.favicon_url || data.logo_url || "",
          timezone: data.timezone || "UTC",
          language: data.language || "",
          attribution_text: data.attribution_text || "Powered by Keygate",
          attribution_url: data.attribution_url || "https://keygate.app",
          loading: false,
        })
        // Dynamic favicon from custom logo. index.html declares
        // multiple <link rel="icon"> variants and browsers pick their
        // favorite (often the sizes="32x32" one), so rewriting only
        // the first link never visibly changed the tab icon — update
        // them all.
        const icon = data.favicon_url || data.logo_url
        if (icon) {
          document.querySelectorAll<HTMLLinkElement>("link[rel~='icon']").forEach((link) => {
            link.href = icon
          })
        }
        const root = document.documentElement.style
        if (data.brand_color) {
          root.setProperty("--color-primary", data.brand_color)
          root.setProperty("--color-ring", data.brand_color)
        }
        if (data.font_stylesheet_url && !document.getElementById("site-font-stylesheet")) {
          const link = document.createElement("link")
          link.id = "site-font-stylesheet"
          link.rel = "stylesheet"
          link.href = data.font_stylesheet_url
          document.head.appendChild(link)
        }
        if (data.font_family) {
          root.setProperty("--font-sans", data.font_family)
        }
        // Theme tokens restyle the whole interface: one CSS custom
        // property per line, validated by the server when saved.
        for (const line of (data.theme_tokens || "").split("\n")) {
          const m = line.match(/^\s*(--[a-z0-9-]+)\s*:\s*(.+?);?\s*$/)
          if (m) root.setProperty(m[1], m[2])
        }
        if (data.site_name) {
          document.title = data.site_name
        }
        // Set default language if user hasn't explicitly chosen one
        if (data.language && !localStorage.getItem("keygate_locale")) {
          localStorage.setItem("keygate_locale", data.language)
          document.documentElement.lang = data.language
        }
      })
      .catch(() => setConfig({ ...defaults, loading: false }))
  }, [])

  return <SiteConfigContext.Provider value={config}>{children}</SiteConfigContext.Provider>
}

export function useSiteConfig() {
  return useContext(SiteConfigContext)
}
