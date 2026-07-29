# 版本升级流程 (VERSION Upgrade)

> 目的：保证 Server / Agent 二进制、运行时显示的版本号、仓库 `VERSION` 文件三者始终一致。
> 历史踩坑：曾用手写 PowerShell 构建脚本把版本号写死（如 `-X ...Version=1.3.2`），未同步 `VERSION` 文件，
> 导致文件停留在旧号、与编译产物不一致。本流程用官方脚本消除该问题。

## 1. 版本单一来源

- **根目录 `VERSION` 文件是唯一权威版本号**（当前 `1.0.0`，**不带 `v` 前缀**）。
- 编译链路：`build/version.sh` 读取 `VERSION` → `build/cross-compile.sh` 通过
  `-ldflags "-X github.com/nebula/monitor/internal/version.Version=..."` 注入到 `internal/version` 包。
- 运行时 `cmd/server` / `cmd/agent` 启动日志的 `version` 字段、HTTP `/api/v1/version` 接口返回的都是注入值。
- `internal/version/version.go` 中 `Version = "dev"` 只是默认值（编译时被 ldflags 覆盖），**不要把它当作发布版本号来改**。
- **GitHub tag 命名**：`v{VERSION}`（如 `v1.0.0`），其中 `v` 前缀仅用于 tag，VERSION 文件本身不带 v。
  `.github/workflows/release.yml` 在发布时会校验两者一致，否则拒绝发布。

## 2. 升级步骤

1. **改 `VERSION` 文件**：按语义化版本递增。
   - 仅 bug 修复 / UI 微调 → patch 级：`x.y.Z`（如 `1.3.1` → `1.3.2`）
   - 含新功能 → minor 级：`x.Y.0`
   - **不要加 `v` 前缀**（与现有约定一致；加前缀会让日志/接口显示 `v1.3.2`，与历史 `1.3.2` 不一致）。
2. **重新编译（必须用官方脚本，禁止手写写死版本的构建脚本）**：
   - Linux / macOS：
     ```bash
     cd <repo>
     GOPROXY=https://goproxy.cn,direct bash build/cross-compile.sh
     ```
   - Windows (PowerShell，需 Git Bash)：
     ```powershell
     cd <repo>
     $env:GOPROXY='https://goproxy.cn,direct'
     & 'C:\Program Files\Git\bin\bash.exe' build/cross-compile.sh
     ```
   脚本自动读取 `VERSION`，交叉编译 `dist/artifacts/bin/{server,agent}/linux/{amd64,arm64,arm}` 共 6 个产物。
3. **提交**：`VERSION` 文件 + 任何源文件改动（编译产物已在 `dist/` 下被 `.gitignore` 忽略，不需要提交）。

## 3. 常见坑（已踩过）

- ❌ 自定义构建脚本把版本号写死 → `VERSION` 文件不同步。一律改 `VERSION` 文件 + 跑 `cross-compile.sh`。
- ❌ 只改 `internal/version/version.go` 默认值 → 编译时被 ldflags 覆盖，无意义。
- ❌ 打 tag 时版本号与 VERSION 文件不一致（如 tag `v1.0.0` 但 VERSION 写成 `1.0.1`）→ GitHub Actions 会校验失败。
- ⚠️ **agent 端若有采集/上报改动**（如在线用户字段、进程 CPU 修正），必须 server 与 agent 同时升级并重分发 agent 二进制，
  否则旧 agent 上报结构不匹配会导致功能回归（如在线用户变空白）。
- ⚠️ 构建会产生临时文件（`build_*.ps1`、`server.log`、`agent.log`、`build.log` 等），提交前清理，避免污染仓库。
- ℹ️ 纯脚本/文档改动（如 `install-server.sh` 健康检查修复）不需要重编译二进制，但 `VERSION` 不变、无需走本流程。

## 4. 验证（部署后）

```bash
curl -s http://<server>:8080/api/v1/version        # server 字段应为新版本号
journalctl -u monitor-server | grep 'Server 启动'   # 日志 version 字段应为新号
```

## 5. 离线发布物（运维分发）

`cross-compile.sh` 只产散落的 `dist/artifacts/bin/{server,agent}/linux/<arch>/` 二进制，
不便于直接交给运维。运行 `build/release.sh` 会把它们打包为两个 tarball 到 `dist/release/`：

- `nebula-monitor-v{VERSION}-full.tar.gz` — 首次部署（含 deploy + packages + install.sh）
- `nebula-monitor-v{VERSION}-upgrade.tar.gz` — 增量升级（仅 bin + web，轻量）

```bash
# 在第 2 步之后追加：
bash build/build-web.sh           # 构建前端 → dist/artifacts/web/
bash build/fetch-packages.sh      # 可选：下载 node + vm → dist/artifacts/packages/
bash build/release.sh             # 组装两个 tarball
```

### full 包用法

```bash
tar -xzf nebula-monitor-v{VERSION}-full.tar.gz
cd nebula-monitor-v{VERSION}-full
sudo ./install.sh server --listen :8080 --tsdb-addr http://10.0.0.10:8428 --agent-auth --yes
sudo ./install.sh agent  --server http://10.0.0.10:8080 --secret <key>
```

### upgrade 包用法

只需替换二进制 + 重启（详见包内 `UPGRADE.md`）：

```bash
tar -xzf nebula-monitor-v{VERSION}-upgrade.tar.gz
cd nebula-monitor-v{VERSION}-upgrade
# 用 install -m 0755：规避运行中二进制的 "Text file busy"，且保证执行位
sudo install -m 0755 bin/server/linux/<arch>/server /usr/local/bin/monitor-server
sudo rsync -a --delete web/ /etc/monitor-server/web/
sudo systemctl restart monitor-server
```

## 6. GitHub Release 自动发布

打 `v{VERSION}` tag 推送到 GitHub 后，`.github/workflows/release.yml` 自动：

1. 校验 tag 与 VERSION 文件一致
2. 跑 `cross-compile.sh` + `build-web.sh` + `fetch-packages.sh` + `release.sh`
3. 把 `dist/release/*.tar.gz` 上传到同名 Release 的 Assets

```bash
# 在 VERSION 已更新、提交已 push 后：
git tag v$(cat VERSION | tr -d '[:space:]')
git push --tags
```

> 整个流程全自动，无需本地构建产物。如需在本地试运行（不依赖 GitHub Actions），
> `bash build/release.sh` 即可手工产出两个 tarball。
