// useCountdown.js — 倒计时 composable
import { ref, onUnmounted } from 'vue'

export function useCountdown(interval = 30) {
  const countdown = ref(interval)
  let timer = null

  function start() {
    stop()
    countdown.value = interval
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
    countdown.value = interval
  }

  onUnmounted(() => {
    stop()
  })

  return { countdown, start, stop, reset }
}