// 由 build 自动生成 web/src/version.js，版本号与 Go 端保持一致（来自仓库根目录 VERSION 文件）。
// 通过 npm run version:sync 调用，已接入 npm run build 的 prebuild 阶段。
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const webDir = resolve(__dirname, '..') // web/
const repoRoot = resolve(__dirname, '..', '..') // 仓库根目录

// 与 Go 端共用根目录 VERSION 文件的语义化版本号（如 v1.0.0）
let version = '0.1.0'
try {
  version = readFileSync(resolve(repoRoot, 'VERSION'), 'utf8').trim()
} catch {}

const outPath = resolve(webDir, 'src/version.js')
mkdirSync(dirname(outPath), { recursive: true })
const content = `// 此文件由 build 自动生成（web/scripts/gen-version.mjs），请勿手动修改。
// 版本号来源：仓库根目录 VERSION 文件；编译前修改后，与 Go 端一致（server/agent 经 ldflags 注入）。
export const WEB_VERSION = ${JSON.stringify(version)}
`

writeFileSync(outPath, content)
console.log('[gen-version] WEB_VERSION =', version)
