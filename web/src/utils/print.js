/**
 * 打印工具
 *
 * 背景：平台主视觉是深色，直接 Ctrl+P 出来是一张黑底废纸。
 * 样式层面由 src/assets/print.css 的 @media print 兜底（令牌反转 + 隐藏 chrome）；
 * 这个文件负责「文档抬头」——打印件如果没写清是什么、什么时间、按什么条件筛的，
 * 拿到手的人无法复现，等于白印。
 *
 * 用法：
 *   import { printPage } from '../utils/print'
 *   printPage({ title: '主机列表', meta: ['在线 12 / 共 15', '分组：生产环境'] })
 */
const HOST_ID = '__nebula_print_head'

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }[c]))
}

function pad(n) {
  return String(n).padStart(2, '0')
}

function stamp(d = new Date()) {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/**
 * 打印当前页面。
 * @param {object} o
 * @param {string} o.title   文档标题（默认取 document.title）
 * @param {string[]} o.meta  抬头元信息，如筛选条件、统计口径
 * @param {Function} [o.before] window.print() 之前执行（可用于临时展开折叠区）
 */
export function printPage(o = {}) {
  const { title = document.title || '运维平台', meta = [], before } = o

  let host = document.getElementById(HOST_ID)
  if (!host) {
    host = document.createElement('div')
    host.id = HOST_ID
    host.className = 'print-only'
    document.body.prepend(host)
  }

  const lines = [`生成时间 ${stamp()}`, ...meta.map((m) => esc(m))]
  host.innerHTML =
    `<h1 class="print-title">${esc(title)}</h1>` +
    `<p class="print-meta">${lines.join(' &nbsp;·&nbsp; ')}</p>`

  const cleanup = () => host && host.remove()
  window.addEventListener('afterprint', cleanup, { once: true })

  try {
    before && before()
  } catch (e) {
    /* 页面自己的准备逻辑失败不应阻断打印 */
  }

  // 让浏览器先完成抬头节点的重排，再弹打印对话框
  requestAnimationFrame(() => window.print())
  return cleanup
}

/**
 * 只打印某个区块（挂在 .no-print 之外的内容会被打印层裁掉 chrome）。
 * 适用于「只要这张表格」的场景，如报告预览、单次巡检结果。
 * @param {HTMLElement|string} target 元素或选择器
 */
export function printElement(target, o = {}) {
  const el = typeof target === 'string' ? document.querySelector(target) : target
  if (!el) return
  const marked = []
  // 给目标之外的顶层兄弟打标记，打印层会把它们全部隐藏
  document.querySelectorAll('.content > *, .view > *').forEach((node) => {
    if (node === el || el.contains(node)) return
    node.classList.add('no-print')
    marked.push(node)
  })
  const restore = () => marked.forEach((n) => n.classList.remove('no-print'))
  window.addEventListener('afterprint', restore, { once: true })
  printPage(o)
}
