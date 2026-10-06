// 保留策略页：日志走外部后端时的展示口径。
//
// 为什么值得一条用例：切到外部后端（VictoriaLogs）后，平台拿不到文件口径的占用，
// 接口回的是 0 文件 / 0 B。界面上必须把它显示成"由外部后端负责"——
// 否则运维看到「集中日志 0 个文件 0 B」会以为日志功能坏了，
// 而真实情况是一次有意的架构选择（保留期归后端自己管）。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import http from '../../api/http'
import RetentionSubView from './RetentionSubView.vue'

vi.mock('../../api/http', () => ({
  default: {
    getRetention: vi.fn(),
    saveRetention: vi.fn(),
    cleanupRetention: vi.fn(),
  },
}))

const BASE_STATUS = {
  config: { enabled: true, acksDays: 90, reportsDays: 180, logsDays: 7, intervalHours: 24 },
  acks: { total: 0, handled: 0 },
  reports: { files: 0, bytes: 0 },
  logs: { files: 0, bytes: 0, sources: 0, days: 0 },
  audit: { count: 0, cap: 1000 },
  security: { count: 0, cap: 1000 },
  tsdb: {},
}

let wrappers = []

function mountView() {
  const w = mount(RetentionSubView, { global: { plugins: [ElementPlus] }, attachTo: document.body })
  wrappers.push(w)
  return w
}

beforeEach(() => {
  vi.clearAllMocks()
  wrappers = []
  http.getRetention.mockResolvedValue(BASE_STATUS)
  http.saveRetention.mockResolvedValue(BASE_STATUS)
  http.cleanupRetention.mockResolvedValue({ at: Date.now(), acksRemoved: 0 })
})

afterEach(() => {
  for (const w of wrappers) w.unmount()
  document.body.innerHTML = ''
})

describe('RetentionSubView 日志后端口径', () => {
  it('自研落盘：展示文件占用与日期分片', async () => {
    http.getRetention.mockResolvedValue({
      ...BASE_STATUS,
      logsBackend: 'local',
      logs: { files: 12, bytes: 2048, sources: 2, days: 3 },
    })
    const w = mountView()
    await flushPromises()

    expect(w.text()).toContain('个文件')
    expect(w.text()).toContain('日期分片')
    expect(w.text()).not.toContain('外部后端')
  })

  it('外部后端：说明由后端负责，而不是显示成 0 个文件', async () => {
    http.getRetention.mockResolvedValue({ ...BASE_STATUS, logsBackend: 'victorialogs' })
    const w = mountView()
    await flushPromises()

    const text = w.text()
    expect(text).toContain('victorialogs')
    expect(text).toContain('外部后端')
    expect(text).toContain('-retentionPeriod')
    // 保留天数这一项此时不生效，必须说清楚（否则用户改了没反应会以为坏了）
    expect(text).toContain('不生效')
    // 不再走本地文件口径（"日期分片"只出现在本地分支里）
    expect(text).not.toContain('日期分片')
  })

  it('后端标识缺失（旧版服务端）时按本地口径展示，不误报外部后端', async () => {
    const w = mountView()
    await flushPromises()

    expect(w.text()).toContain('个文件')
    expect(w.text()).not.toContain('外部后端')
  })
})
