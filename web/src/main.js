import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './assets/style.css'

// Element Plus + 暗色主题
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

// ECharts 按需
import Echarts from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart, BarChart, GaugeChart, PieChart } from 'echarts/charts'
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  TitleComponent,
  DataZoomComponent,
} from 'echarts/components'

use([
  CanvasRenderer,
  LineChart,
  BarChart,
  GaugeChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  TitleComponent,
  DataZoomComponent,
])

// 启用暗色模式
document.documentElement.classList.add('dark')

// 启动时还原主题（登录页也生效），默认极光蓝 'b'
const savedTheme = localStorage.getItem('nebula_theme') || 'b'
document.body.dataset.theme = savedTheme

const app = createApp(App)
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}
app.use(router)
app.use(ElementPlus)
app.use(Echarts)
app.mount('#app')
