import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

const here = dirname(fileURLToPath(import.meta.url))
const read = (path: string) => readFileSync(resolve(here, path), 'utf8')

function keyPaths(obj: unknown, prefix = ''): string[] {
  if (typeof obj !== 'object' || obj === null) return [prefix]
  return Object.entries(obj).flatMap(([k, v]) => keyPaths(v, prefix ? `${prefix}.${k}` : k)).sort()
}

describe('Recording integration surface', () => {
  it('registers an admin-only route', () => {
    const router = read('../../../router/index.ts')
    const start = router.indexOf("path: '/admin/recording'")
    expect(start).toBeGreaterThan(-1)
    const route = router.slice(start, router.indexOf('}\n  },', start))
    expect(route).toContain('requiresAuth: true')
    expect(route).toContain('requiresAdmin: true')
    expect(route).toContain("@/features/recording/RecordingView.vue")
  })

  it('adds a sidebar entry', () => {
    expect(read('../../../components/layout/AppSidebar.vue')).toContain("{ path: '/admin/recording', label: t('nav.recording'), icon: RecordIcon }")
  })

  it('keeps en and zh locale trees identical', () => {
    expect(keyPaths(zh.admin.recording)).toEqual(keyPaths(en.admin.recording))
    expect(en.nav.recording).toBeTruthy()
    expect(zh.nav.recording).toBeTruthy()
  })
})
