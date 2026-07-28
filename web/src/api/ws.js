// ws.js — WebSocket 实时订阅封装（自动重连）。
// connectWS(topic, node, { onMessage, onStatus }) 返回一个 { close } 句柄。
export function connectWS(topic, node, handlers = {}) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const url =
    proto +
    '://' +
    location.host +
    '/ws?topic=' +
    encodeURIComponent(topic) +
    (node ? '&node=' + encodeURIComponent(node) : '')

  let ws = null
  let closed = false
  let retry = 0

  function open() {
    ws = new WebSocket(url)
    ws.onopen = () => {
      retry = 0
      handlers.onStatus && handlers.onStatus('open')
    }
    ws.onmessage = (ev) => {
      try {
        handlers.onMessage && handlers.onMessage(JSON.parse(ev.data))
      } catch (e) {
        /* ignore malformed */
      }
    }
    ws.onclose = () => {
      handlers.onStatus && handlers.onStatus('closed')
      if (!closed) {
        retry++
        setTimeout(open, Math.min(1000 * retry, 8000))
      }
    }
    ws.onerror = () => {
      ws && ws.close()
    }
  }
  open()

  return {
    close() {
      closed = true
      ws && ws.close()
    },
  }
}
