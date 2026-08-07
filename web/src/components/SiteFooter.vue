<template>
  <footer v-if="visible" class="site-footer">
    <div class="site-footer__inner">
      <div class="site-footer__brand">
        <span class="site-footer__mark" aria-hidden="true">✦</span>
        <span>{{ brandName }}</span>
      </div>

      <nav v-if="links.length" class="site-footer__links" aria-label="页脚链接">
        <a
          v-for="link in links"
          :key="`${link.label}-${link.url}`"
          :href="link.url"
          :target="link.external ? '_blank' : undefined"
          :rel="link.external ? 'noreferrer' : undefined"
        >{{ link.label }}</a>
      </nav>

      <div class="site-footer__meta">
        <span v-if="footerText">{{ footerText }}</span>
        <a v-if="icpUrl && icpText" :href="icpUrl" target="_blank" rel="noreferrer">{{ icpText }}</a>
        <span v-else-if="icpText">{{ icpText }}</span>
      </div>
    </div>
  </footer>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'

const settings = ref({})

const asObject = (value) => value && typeof value === 'object' ? value : {}

function unwrap(payload) {
  let value = asObject(payload)
  for (const key of ['data', 'config', 'settings', 'site', 'brand']) {
    if (value[key] && typeof value[key] === 'object') value = value[key]
  }
  return value
}

function pick(...values) {
  return values.find((value) => value !== undefined && value !== null && String(value).trim() !== '')
}

const footer = computed(() => asObject(
  settings.value.footer ||
  settings.value.footerSettings ||
  settings.value.branding?.footer ||
  settings.value.brand?.footer
))
const brandName = computed(() => pick(
  footer.value.brandName,
  settings.value.siteName,
  settings.value.brandName,
  settings.value.name,
  'Nebula Monitor'
))
const footerText = computed(() => pick(
  footer.value.text,
  footer.value.content,
  footer.value.copyright,
  footer.value.copyrightText,
  settings.value.footerText,
  settings.value.footerContent,
  settings.value.copyright,
  `© ${new Date().getFullYear()} ${brandName.value}`
))
const icpText = computed(() => pick(
  footer.value.icp,
  footer.value.icpNumber,
  footer.value.beian,
  footer.value.record,
  settings.value.icp,
  settings.value.icpNumber,
  settings.value.beian
))
const icpUrl = computed(() => pick(footer.value.icpUrl, footer.value.beianUrl, settings.value.icpUrl))
const visible = computed(() => footer.value.enabled !== false && footer.value.visible !== false && footer.value.show !== false && settings.value.footerEnabled !== false)

const links = computed(() => {
  let value = footer.value.links || settings.value.footerLinks || []
  if (typeof value === 'string') {
    try { value = JSON.parse(value) } catch { value = [] }
  }
  const list = Array.isArray(value) ? value : Object.entries(asObject(value)).map(([label, url]) => ({ label, url }))
  return list.map((item) => {
    const link = typeof item === 'string' ? { label: item, url: item } : asObject(item)
    return {
      label: pick(link.label, link.title, link.name, link.text),
      url: pick(link.url, link.href, '#'),
      external: link.external !== false
    }
  }).filter((item) => item.label)
})

async function loadSettings() {
  for (const key of ['siteSettings', 'brandSettings', 'siteConfig', 'branding']) {
    try {
      const payload = JSON.parse(localStorage.getItem(key) || 'null')
      if (payload && typeof payload === 'object') settings.value = { ...settings.value, ...unwrap(payload) }
    } catch {
      // Ignore malformed legacy cache entries.
    }
  }

  const endpoints = [
    '/api/v1/settings/brand',
    '/api/v1/settings/branding',
    '/api/v1/site/brand',
    '/api/v1/site/branding',
    '/api/v1/config/brand',
    '/api/v1/config/site',
    '/api/v1/settings/site',
    '/api/v1/site-settings',
    '/api/v1/site/config',
    '/api/v1/site-config',
    '/api/v1/branding',
    '/api/v1/settings'
  ]
  for (const endpoint of endpoints) {
    try {
      const response = await fetch(endpoint)
      if (!response.ok) continue
      const payload = unwrap(await response.json())
      if (Object.keys(payload).length) {
        settings.value = { ...settings.value, ...payload }
        if (payload.footer || payload.footerSettings || payload.branding?.footer || payload.brand?.footer) return
      }
    } catch {
      // Branding is optional; keep the local fallback when the endpoint is unavailable.
    }
  }
}

onMounted(loadSettings)
</script>

<style scoped>
.site-footer {
  flex: 0 0 auto;
  width: 100%;
  padding: 18px 28px 22px;
  color: var(--text-secondary, #8490a7);
  border-top: 1px solid color-mix(in srgb, var(--border-color, #263149) 75%, transparent);
  background: color-mix(in srgb, var(--bg-primary, #0c1220) 84%, transparent);
}

.site-footer__inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  max-width: 1440px;
  margin: 0 auto;
  font-size: 12px;
}

.site-footer__brand,
.site-footer__links,
.site-footer__meta {
  display: flex;
  align-items: center;
  gap: 12px;
}

.site-footer__brand {
  color: var(--text-primary, #e6ebf5);
  font-weight: 600;
  letter-spacing: .02em;
  white-space: nowrap;
}

.site-footer__mark {
  display: inline-grid;
  width: 22px;
  height: 22px;
  place-items: center;
  color: #83a7ff;
  border: 1px solid color-mix(in srgb, #83a7ff 45%, transparent);
  border-radius: 7px;
  background: color-mix(in srgb, #83a7ff 12%, transparent);
}

.site-footer__links {
  justify-content: center;
  flex-wrap: wrap;
}

.site-footer a {
  color: inherit;
  text-decoration: none;
  transition: color .2s ease;
}

.site-footer a:hover {
  color: var(--text-primary, #f2f5fb);
}

.site-footer__meta {
  justify-content: flex-end;
  flex-wrap: wrap;
  text-align: right;
}

@media (max-width: 760px) {
  .site-footer { padding: 16px 18px 20px; }
  .site-footer__inner { flex-direction: column; gap: 12px; text-align: center; }
  .site-footer__meta { justify-content: center; text-align: center; }
}
</style>
