// 巡检报告页的**周期化生成**回归测试（全景表 11-4）。
//
// 为什么要有这个文件：周期化生成是"以后会自动产出文件"的开关，三种失败都不显眼——
// 开关看着是开的但服务端没接受（保存失败后界面没回滚）、上次生成失败了却不说、
// 该能力未启用却把开关显示成"关着"（用户以为打开就好）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../api/http'
import ReportView from './ReportView.vue'

vi.mock('../api/http', () => ({
  default: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  getToken: vi.fn(() => ''),
}))

const HISTORY = { reports: [{ id: 'rpt-1', type: 'weekly', period: '2026-09-29 ~ 2026-10-06', generatedAt: 1700000000000 }] }
const SCHEDULE = {
  config: { enabled: true, type: 'weekly', intervalHours: 24 },
  last: { at: 1700000000000, reportId: 'rpt-1' },
  nextAt: 1700000000000 + 24 * 3600 * 1000,
}

let wrappers = []

function mountView() {
  const w = mount(ReportView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  http.get.mockImplementation(async (path) => {
    if (String(path).includes('/api/v1/report/schedule')) return SCHEDULE
    return HISTORY
  })
  http.put.mockResolvedValue(SCHEDULE)
  http.post.mockResolvedValue({ at: 1700000000000, reportId: 'rpt-2', manual: true })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('ReportView 周期化生成', () => {
  it('展示上次运行与下次时间，并说明手动生成不受影响', async () => {
    const w = mountView()
    await flushPromises()
    expect(w.text()).toContain('周期化生成')
    expect(w.text()).toContain('上次运行')
    expect(w.text()).toContain('rpt-1')
    expect(w.text()).toContain('下次约')
  })

  it('从未运行过时如实说明，而不是留空', async () => {
    http.get.mockImplementation(async (path) => {
      if (String(path).includes('/api/v1/report/schedule')) {
        return { config: { enabled: false, type: 'weekly', intervalHours: 24 } }
      }
      return { reports: [] }
    })
    const w = mountView()
    await flushPromises()
    expect(w.text()).toContain('尚未运行过')
    expect(w.text()).toContain('不影响上方的手动生成')
  })

  it('上次自动生成失败要显式提示（否则"报告怎么不出了"只能靠翻文件时间猜）', async () => {
    http.get.mockImplementation(async (path) => {
      if (String(path).includes('/api/v1/report/schedule')) {
        return {
          config: { enabled: true, type: 'weekly', intervalHours: 24, lastError: '磁盘只读' },
          last: { at: 1700000000000, error: '磁盘只读' },
        }
      }
      return HISTORY
    })
    const w = mountView()
    await flushPromises()
    expect(w.text()).toContain('磁盘只读')
    expect(w.text()).toContain('上次自动生成失败')
  })

  it('该能力未启用（503）时如实说明，而不是把开关显示成"关着"', async () => {
    http.get.mockImplementation(async (path) => {
      if (String(path).includes('/api/v1/report/schedule')) throw new Error('报告调度未启用')
      return HISTORY
    })
    const w = mountView()
    await flushPromises()
    expect(w.text()).toContain('报告调度未启用')
  })

  it('保存失败后回读服务端真实状态（不把界面留在"我改成了这样"）', async () => {
    let calls = 0
    http.get.mockImplementation(async (path) => {
      if (String(path).includes('/api/v1/report/schedule')) {
        calls++
        // 第一次给"关着"的状态，保存失败后回读仍是"关着"
        return { config: { enabled: false, type: 'weekly', intervalHours: 24 } }
      }
      return HISTORY
    })
    http.put.mockRejectedValue(new Error('保存失败'))
    const w = mountView()
    await flushPromises()

    const sw = w.find('.el-switch')
    await sw.trigger('click')
    await flushPromises()
    expect(http.put).toHaveBeenCalled()
    // 回读：至少再次拉取了状态
    expect(calls).toBeGreaterThan(1)
  })

  it('立即生成会调用调度接口并刷新历史', async () => {
    const w = mountView()
    await flushPromises()
    const btn = w.findAll('button').find((b) => b.text().includes('立即生成一次'))
    expect(btn).toBeTruthy()
    await btn.trigger('click')
    await flushPromises()
    expect(http.post).toHaveBeenCalledWith('/api/v1/report/schedule/run', {})
    const paths = http.get.mock.calls.map((c) => String(c[0]))
    expect(paths.some((p) => p.includes('/api/v1/report/history'))).toBe(true)
  })
})
