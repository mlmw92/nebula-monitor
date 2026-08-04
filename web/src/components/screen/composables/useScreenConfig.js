// useScreenConfig.js — 大屏模块配置 composable
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import http from '../../../api/http'

export function useScreenConfig() {
  const cfg = reactive({ modules: { alerts: true } })
  const settingOpen = ref(false)
  const savingCfg = ref(false)

  async function loadScreenConfig() {
    try {
      const res = await http.get('/api/v1/screen/config')
      if (res && res.modules) cfg.modules = { ...cfg.modules, ...res.modules }
    } catch (e) {
      /* 默认全开 */
    }
  }

  async function saveScreenConfig() {
    savingCfg.value = true
    try {
      await http.put('/api/v1/screen/config', { modules: { ...cfg.modules } })
      ElMessage.success('大屏配置已保存')
      settingOpen.value = false
    } catch (e) {
      ElMessage.error('保存失败')
    } finally {
      savingCfg.value = false
    }
  }

  return { cfg, settingOpen, savingCfg, loadScreenConfig, saveScreenConfig }
}