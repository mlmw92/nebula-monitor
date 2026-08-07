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
import { computed } from 'vue'
import { useBrand } from '../composables/useBrand'

const { brand } = useBrand()
const brandName = computed(() => brand.name || 'NebulaEye')
const footerText = computed(() => brand.footer || '')
const visible = computed(() => Boolean(brand.footer))
const icpText = computed(() => '')
const icpUrl = computed(() => '')

const links = computed(() => {
  return []
})
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
