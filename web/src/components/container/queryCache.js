// 容器页的查询结果缓存（模块级，跨路由切换保留）。
//
// 为什么必须放在组件之外：这一页的结果来自**异步下行任务**——下发指令 → 等 Agent 下一轮上报 → 回执，
// 通常 15 秒以上。若结果只存在组件状态里，每次路由切换重新挂载都会丢掉它，
// 于是"点进这一页"本身就等于重新下发一轮查询，用户看到的现象就是"每次点击都在刷新"。
// 缓存到模块级之后：结果与**刷新时间**跨路由保留，只有用户点「查询」时才重新下发。
//
// 缓存键是「集群 + 动作」而不是只按动作：换集群是**换视图**、不是刷新，
// 切回来应该直接看到那个集群上次的结果与时间（按 Tab 一把清掉就会把这条也做丢）。
import { reactive } from 'vue'

const states = reactive({})

// containerQueryState 取（或建）某个「集群 + 动作」的查询状态。
// at 是**结果落地的时间**（毫秒）：界面唯一能回答"这份数据有多旧"的依据。
export function containerQueryState(clusterKey, kind) {
  const key = clusterKey + '|' + kind
  if (!states[key]) states[key] = newState()
  return states[key]
}

// containerDetailState 是详情抽屉的状态，键里带上对象身份：
// 重复点同一行同样是一次 15 秒的下发，没理由重来一遍。
export function containerDetailState(clusterKey, kind, namespace, name) {
  const key = 'detail|' + clusterKey + '|' + kind + '|' + namespace + '|' + name
  if (!states[key]) states[key] = newState()
  return states[key]
}

function newState() {
  // ns 是这份结果**当初是按哪个命名空间查的**：结果会被保留（本地缓存或从服务端认领），
  // 而输入框里的条件随时会变。不记下它，就会出现"输入框写着 default、表里却是全部命名空间的对象"
  // 而界面一言不发——那比数据旧更糟。
  return { busy: false, task: null, error: '', result: null, at: 0, ns: '' }
}

// resetContainerQueryCache 清空缓存（仅测试用：避免用例之间互相污染）。
export function resetContainerQueryCache() {
  for (const key of Object.keys(states)) delete states[key]
}
