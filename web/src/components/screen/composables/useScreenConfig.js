// useScreenConfig.js — 大屏模块配置 composable
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../../../api/http'

// 大屏刷新间隔可选档位（秒），与后端 config.ScreenRefreshIntervals 保持一致
export const REFRESH_INTERVALS = [10, 20, 30, 60]

export function useScreenConfig() {
  const cfg = reactive({ modules: { alerts: true }, refreshInterval: 30 })
  const settingOpen = ref(false)
  const savingCfg = ref(false)

  async function loadScreenConfig() {
    try {
      const res = await http.get('/api/v1/screen/config')
      if (res && res.modules) cfg.modules = { ...cfg.modules, ...res.modules }
      // 仅接受预设档位，历史遗留的其它值回退默认 30 秒
      if (res && REFRESH_INTERVALS.includes(res.refreshInterval)) {
        cfg.refreshInterval = res.refreshInterval
      }
    } catch (e) {
      /* 默认全开 */
    }
  }

  async function saveScreenConfig() {
    savingCfg.value = true
    try {
      await http.put('/api/v1/screen/config', {
        modules: { ...cfg.modules },
        refreshInterval: cfg.refreshInterval,
      })
      ElMessage.success('大屏配置已保存')
      settingOpen.value = false
    } catch (e) {
      ElMessage.error(e?.message || '保存失败')
    } finally {
      savingCfg.value = false
    }
  }

  return { cfg, settingOpen, savingCfg, loadScreenConfig, saveScreenConfig }
}