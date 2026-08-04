// useCountdown.js — 倒计时 composable
// interval 可为数字或 ref（支持运行时动态修改刷新间隔）
import { ref, unref, onUnmounted } from 'vue'

export function useCountdown(interval = 30) {
  const countdown = ref(unref(interval))
  let timer = null

  function start() {
    stop()
    countdown.value = unref(interval)
    timer = setInterval(() => {
      if (countdown.value > 0) countdown.value--
    }, 1000)
  }

  function stop() {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  function reset() {
    countdown.value = unref(interval)
  }

  onUnmounted(() => {
    stop()
  })

  return { countdown, start, stop, reset }
}