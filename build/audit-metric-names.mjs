// 指标名一致性审计（**手工运行**，不进 CI：CI 里的自动守卫见
// internal/server/metrics/catalog_guard_test.go 与 internal/server/alert/service_metric_test.go）。
//
// 用途：把「产出方」与「消费方」引用的中间件指标名做全量比对，找出
// 「消费方引用但无人产出」的名字。这类缺陷的症状不是报错，而是静默失效：
// 「指标浏览」查不到数据、`/metrics/active` 标为未上线、巡检报告字段恒为空。
//
// 用法：node build/audit-metric-names.mjs
// 退出码：发现不一致时非 0（便于在需要时挂进任意流水线）。
//
// 为什么需要它（而不只靠 Go 守卫）：Go 守卫校验的是「指标目录 ↔ 产出方」，
// 而 api / report / 前端等**消费方**的引用不在其覆盖范围——本脚本正是为后者存在。
// 首次运行的战果：目录 30 条中 17 条无产出方、消费侧共 27 处引用了不存在的名字。
//
// 注意两类**已知误报**（脚本会列出，人工判断即可）：
//   1. 动态拼名前缀（如 "mongodb_opcounters_" + op）——消费方写的是拼好后的完整名；
//   2. 前缀判断（如 strings.HasPrefix(name, "redis_cluster_")）。

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, extname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')

// 中间件指标名前缀（与 internal/server/metrics 的分类一致）
const prefixes = ['redis_', 'mysql_', 'postgres_', 'nginx_', 'nginx_access_', 'kafka_', 'docker_', 'rocketmq_', 'k8s_', 'mongodb_', 'fastdfs_']
const isMw = (n) => prefixes.some((p) => n.startsWith(p))
const isTest = (f) => f.endsWith('_test.go') || f.includes('.test.') || f.includes('.spec.')

// 只有这两个位置会「写」指标名
const producerDirs = ['internal/agent/collector', 'internal/server/receiver']
// 读取/展示指标名的地方
const consumerDirs = ['internal/server/api', 'internal/server/report', 'internal/server/metrics', 'internal/server/analysis', 'internal/server/storage']
const consumerExts = ['.go', '.js', '.vue']

function walk(dir, out = []) {
  let entries
  try {
    entries = readdirSync(join(root, dir))
  } catch {
    return out
  }
  for (const f of entries) {
    if (['node_modules', 'dist', '_backup', '.git'].includes(f)) continue
    const rel = join(dir, f)
    if (statSync(join(root, rel)).isDirectory()) walk(rel, out)
    else out.push(rel)
  }
  return out
}

// 指标名里存在 camelCase 段（如 mongodb_db_dataSize_bytes），正则必须区分大小写
const literal = /["'`]([a-zA-Z][a-zA-Z0-9_]*_[a-zA-Z0-9_]+)["'`]/g

function namesIn(file) {
  const set = new Set()
  for (const m of readFileSync(join(root, file), 'utf8').matchAll(literal)) {
    if (isMw(m[1]) && !m[1].endsWith('_')) set.add(m[1])
  }
  return set
}

const produced = new Set()
for (const f of producerDirs.flatMap((d) => walk(d))) {
  if (f.endsWith('.go') && !isTest(f)) for (const n of namesIn(f)) produced.add(n)
}

const consumers = [
  ...consumerDirs.flatMap((d) => walk(d)).filter((f) => f.endsWith('.go') && !isTest(f)),
  ...walk('web/src').filter((f) => consumerExts.includes(extname(f)) && !isTest(f)),
]

const bad = new Map()
for (const f of consumers) {
  for (const n of namesIn(f)) {
    if (produced.has(n)) continue
    if (!bad.has(n)) bad.set(n, new Set())
    bad.get(n).add(f)
  }
}

console.log(`产出方：${produced.size} 个中间件指标名`)
if (!bad.size) {
  console.log('✔ 未发现「消费方引用但无产出方」的指标名')
  process.exit(0)
}
console.log(`\n✘ 发现 ${bad.size} 个可疑名字（请人工确认是否为动态拼名/前缀判断等误报）：`)
for (const [name, files] of [...bad.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
  console.log(`  ${name.padEnd(38)} <- ${[...files].join(', ')}`)
}
process.exit(1)
