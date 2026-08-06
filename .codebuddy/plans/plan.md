## 用户需求

服务器被攻破时，存储在服务器/本机的密码以明文泄露，需对两类密码做加密加固：登录密码、中间件监控密码。加密方案采用国密算法：登录密码用 SM3 加盐哈希（不可逆），中间件监控密码用 SM4-CBC 对称加密 + SM3-HMAC 完整性校验（可还原以连库）。要求升级后旧明文配置自动平滑迁移，用户无需重填或重置。用户额外要求提供 Web 端「修改密码」功能。

> 说明：方案最初设计为 bcrypt + AES-GCM，后经用户要求改为国密算法（SM3 / SM4）。

## 产品概述

在不改变现有功能与使用方式的前提下，对 nebula-monitor 的敏感凭据做静态加密存储。登录密码在 Server 配置中以国密 SM3 加盐哈希存储、登录时哈希比对；中间件（Redis/MySQL/PostgreSQL/MongoDB 等）的连接密码在 Agent 配置中以 SM4-CBC 密文存储，Agent 加载时解密为明文供采集器使用。两类配置均兼容现有明文旧格式，实现无感升级。

## 核心特性

- 登录密码 SM3 加盐哈希存储与比对，替换原明文 ConstantTimeCompare
- 中间件监控密码 SM4-CBC 对称加密 + SM3-HMAC 完整性校验，密文前缀 `enc:` 区分明文旧配置
- 旧明文配置平滑迁移：Server 启动时将明文登录密码转为 SM3 哈希并落盘；Agent 加载时旧明文密码直接按明文使用、密文自动解密
- 采集器上报仍为 `json:"-"`，密码不离开主机，泄露面不变
- Web 端「修改密码」功能：已登录用户校验旧密码后重置，新密码以 SM3 哈希持久化
- 新增单元测试覆盖哈希校验、加解密往返、旧明文兼容

## 技术栈

- 后端：Go 1.21+，国密算法库 `github.com/tjfoc/gmsm v1.4.1`（sm3 / sm4），标准库 `crypto/cipher`(CBC)
- 配置格式：YAML（server `config/config.go` / agent `config/config.go`），不改变现有配置文件路径与字段名
- 前端：Vue3 + Element Plus，新增「修改密码」子页（登录仍提交明文 password，由后端哈希比对）

## 实现方案

### 整体策略

两类密码分两条独立链路处理，互不耦合：

1. **登录密码（server）**：`AuthConfig.Password` 语义从「明文」变为「国密 SM3 加盐哈希（形如 `sm3:<saltHex>:<hashHex>`）」。新增 `internal/server/crypto` 包封装 `HashPassword`/`VerifyPassword`/`IsHashed`。`handleLogin` 与 `handleChangePassword` 改用 `servercrypto.VerifyPassword`。为兼容旧明文，加载配置时若 `Password` 不以 `sm3:` 开头则视为明文，立即 SM3 哈希并写回（内存+落盘）。
2. **中间件密码（agent）**：新增 `internal/agent/crypto` 包封装 SM4-CBC `Encrypt`/`Decrypt`（随机 IV + PKCS7 填充 + SM3-HMAC 校验），master key 取自 agent.yaml 新增字段 `cryptoKey`（yaml 明文，缺省回退到编译期内置派生常量，16 字节 SM4 密钥）。约定密文前缀 `enc:`。在 `agent/config.Load()` 中对各 `*Instances` 的 `Password` 做后处理：以 `enc:` 开头→去前缀后 SM4 解密为明文写入内存；否则保持原明文（旧配置兼容）。采集器读取的内存值已是明文，行为不变。

### 关键技术决策与权衡

- **SM3 而非对称加密**：登录密码只用于验证、永不还原，哈希不可逆，被攻破后无法反推原密码，最安全。采用随机 salt 防止彩虹表。
- **SM4-CBC + SM3-HMAC 而非哈希**：中间件密码需还原以建立数据库连接，只能对称加密；CBC 提供机密性，SM3-HMAC(IV‖密文) 提供完整性/防篡改。
- **master key 缺省内置派生值**：Agent 通常无人工配置密钥能力，内置常量保证开箱即用；用户可在 `cryptoKey` 配置自定义 key 提升安全性（key 本身存于本机 agent.yaml，防御「单文件泄露/只读挂载」场景，与需求一致）。
- **agent 不做强制回写**：已核实 agent 包无 `os.WriteFile`/`yaml.Marshal`，不引入回写逻辑（避免大改动与风险）。磁盘密文化发生在「用户下次修改密码经下发链路写入」时；本次仅保证读时解密与旧明文兼容。
- **平滑迁移零中断**：server 明文→SM3 哈希在启动时自动完成并落盘；agent 明文密码原样可用。用户无需任何手动操作。

### 性能与可靠性

- SM3 为快速哈希，登录为低频操作，无性能瓶颈；`VerifyPassword` 内置常量时间比较防时序攻击。
- SM4-CBC 加解密为内存操作，每个密码仅数十字节，采集间隔（默认 15s）下开销可忽略。
- 解密失败（密钥不匹配/HMAC 校验失败/密文损坏）时记录 `slog.Warn` 并保留原值（或置空使连接失败并告警），避免 panic 导致 agent 启动失败。

## 实现要点

### 目录结构与受影响文件

```
nebula-monitor/
├── go.mod                                   # [MODIFY] 新增 github.com/tjfoc/gmsm v1.4.1 直接依赖（golang.org/x/crypto 因移除 bcrypt 而由 tidy 清理）
├── internal/server/
│   ├── crypto/crypto.go                     # [NEW] SM3 加盐哈希：HashPassword/VerifyPassword/IsHashed
│   ├── crypto/crypto_test.go                # [NEW] 哈希/校验/加盐去重/旧明文兼容测试
│   ├── config/config.go                     # [MODIFY] AuthConfig.Password 注释；启动明文→SM3 哈希迁移并落盘；PatchAuthPassword
│   └── api/auth.go                          # [MODIFY] handleLogin 改用 VerifyPassword；新增 handleChangePassword（POST /api/v1/auth/change-password）
│   └── api/query.go                         # [MODIFY] API 增加 configPath 字段与 New 参数；注册 change-password 路由
│   └── cmd/server/main.go                   # [MODIFY] 启动后调用迁移；api.New 传入 *cfgPath
├── internal/agent/
│   ├── crypto/crypto.go                     # [NEW] SM4-CBC + SM3-HMAC 封装：NewCipher/Encrypt/Decrypt（enc: 前缀）
│   ├── crypto/crypto_test.go                # [NEW] 加解密往返/前缀识别/错误密钥 HMAC 失败测试
│   └── config/config.go                     # [MODIFY] 新增 CryptoKey 字段；Load() 中对 Redis/MySQL/Postgres/MongoDB 等 *Instances.Password 做 enc: 解密/明文兼容后处理
├── internal/model/metric.go                 # [确认] 各实例 Password 字段保持 json:"-" 不变（仅约定 enc: 前缀语义，不改结构）
└── web/src/
    ├── api/http.js                          # [MODIFY] 新增 changePassword API
    ├── components/Sidebar.vue               # [MODIFY] 系统设置分组增加「修改密码」子菜单
    ├── layouts/MainLayout.vue               # [MODIFY] 面包屑支持修改密码标签页
    ├── router/index.js                      # [MODIFY] system/settings 路由指向 SettingsView
    └── components/settings/
        ├── SettingsView.vue                 # [NEW] 标签页容器（站点与品牌 / 修改密码）
        └── ChangePasswordSubView.vue        # [NEW] 修改密码表单页
```

### 关键接口（新增包）

```go
// internal/server/crypto/crypto.go
package crypto
func HashPassword(plain string) (string, error)   // 生成 "sm3:<saltHex>:<hashHex>"，salt 随机 16 字节
func VerifyPassword(stored, plain string) bool     // sm3: 前缀走 SM3(salt||pwd) 常量时间比较；否则明文兜底
func IsHashed(s string) bool                      // 以 sm3: 开头

// internal/agent/crypto/crypto.go
package crypto
func NewCipher(key []byte) (*Cipher, error)        // SM4 密钥归一化为 16 字节；空 key 用内置默认密钥
func (c *Cipher) Encrypt(plain string) (string, error)   // 返回 "enc:" + base64(IV(16)‖密文‖SM3-HMAC(32))
func (c *Cipher) Decrypt(s string) (string, error)        // 识别 "enc:" 前缀并解密+校验；非前缀原样返回（旧明文兼容）
func IsEncrypted(s string) bool
```

### 平滑迁移细节

- **Server**：`cmd/server/main.go` 加载配置后，若 `!servercrypto.IsHashed(cfg.Auth.Password) && cfg.Auth.Password != ""` → SM3 哈希后写回 `cfg.Auth.Password` 并通过 `config.PatchAuthPassword` 以 `yaml.Node` 精确定位 `auth.password` 段原子写回（先备份 `.bak`）。`handleLogin`/`handleChangePassword` 主路径用 `VerifyPassword`。
- **Agent**：`config.Load()` 在 `yaml.Unmarshal` 后对 `RedisInstances/MySQLInstances/PostgresInstances/MongoDBInstances` 逐条调用 `cipher.Decrypt(inst.Password)`，结果写回 `inst.Password`（内存）。`cryptoKey` 为空时使用内置默认密钥。解密失败仅告警、保留原值。

### Web 修改密码

- 路由 `system/settings` 指向 `SettingsView`（标签页：站点与品牌 / 修改密码），侧边栏「系统设置」分组增加「修改密码」入口。
- `ChangePasswordSubView.vue` 表单校验（原密码、新密码≥6 位、确认一致），调用 `http.changePassword(old, new)`。
- 后端 `POST /api/v1/auth/change-password`（authRequired 中间件保证已登录）：校验旧密码 → SM3 哈希新密码 → 更新内存并 `PatchAuthPassword` 持久化到 `server.yaml`。

### 验证方式

- 单元测试：`go test ./internal/server/crypto/... ./internal/agent/crypto/...`（已全部通过）
- 手动：使用旧明文 `agent.yaml`（含 Redis/MySQL 明文密码）启动 agent，确认采集正常；将 `Auth.Password` 设为明文 `admin` 启动 server，确认自动转为 `sm3:` 哈希且可登录；密文 `enc:` 配置项能正确解密连库；Web「修改密码」改密后重新登录成功。
- Agent 改动需与 Server 同步升级并重分发 agent 二进制（项目既有规范）。
