# NebulaEye monitor-server 崩溃日志
抓取时间: 2026-08-28 21:xx (北京时间)
主机: 124.223.77.206 (VM-0-10-ubuntu, 上海腾讯云)
服务: monitor-server.service (nebula-monitor Server)

---

## 一、崩溃概况

24小时内 panic 崩溃次数: 21 次

触发源 IP: 39.71.17.51 (持续触发报告生成接口导致 panic)

崩溃类型: assignment to entry in nil map
(Go 程序向未初始化的 map 写入, 触发 panic)

---

## 二、典型崩溃堆栈 (2026-08-28 20:37:14)

Aug 28 20:37:14 VM-0-10-ubuntu monitor-server[2243234]: {"time":"2026-08-28T20:37:14.447630924+08:00","level":"INFO","msg":"http: panic serving 39.71.17.51:52264: assignment to entry in nil map
goroutine 2625 [running]:
net/http.(*conn).serve.func1()
    C:/Program Files/Go/src/net/http/server.go:1907 +0xbd
panic({0x9a4340?, 0x197ea00?})
    C:/Program Files/Go/src/runtime/panic.go:860 +0x13a
github.com/nebula/monitor/internal/server/report.(*Generator).collectSecurity(0x29f537cae0f0, 0x1a04339760b, 0x1a0485fd20b)
    E:/codebuddy_project/nebula-monitor/internal/server/report/report.go:618 +0x271
github.com/nebula/monitor/internal/server/report.(*Generator).collectData(_, {_, _, _}, {_, _, _}, {_, _})
    E:/codebuddy_project/nebula-monitor/internal/server/report/report.go:419 +0x17fa
github.com/nebula/monitor/internal/server/report.(*Generator).Generate(0x29f537cae0f0, {0x29f539c19414, 0x5})
    E:/codebuddy_project/nebula-monitor/internal/server/report/report.go:264 +0x125
github.com/nebula/monitor/internal/server/api.(*API).handleReportGenerate(0x29f538078820, {0xa879c0, 0x29f538fc7b48}, 0x29f538d1a140)
    E:/codebuddy_project/nebula-monitor/internal/server/api/middleware_api.go:1423 +0xee
net/http.HandlerFunc.ServeHTTP(...)
    C:/Program Files/Go/src/net/http/server.go:2286 +0x29
net/http.(*ServeMux).ServeHTTP(...)
    C:/Program Files/Go/src/net/http/server.go:2828 +0x1c7
main.main.AuditMiddleware.func3(...)
    E:/codebuddy_project/nebula-monitor/internal/server/api/audit.go:54 +0x255
net/http.HandlerFunc.ServeHTTP(...)
    C:/Program Files/Go/src/net/http/server.go:2286 +0x29
main.main.AuthMiddleware.func4(...)
    E:/codebuddy_project/nebula-monitor/internal/server/api/auth.go:169 +0x49d
net/http.HandlerFunc.ServeHTTP(...)
    C:/Program Files/Go/src/net/http/server.go:2286 +0x29
net/http.serverHandler.ServeHTTP(...)
    C:/Program Files/Go/src/net/http/server.go:3311 +0x8e
net/http.(*conn).serve(...)
    C:/Program Files/Go/src/net/http/server.go:2073 +0x650
created by net/http.(*Server).Serve in goroutine 1
    C:/Program Files/Go/src/net/http/server.go:3464 +0x485"}

---

## 三、崩溃点定位

核心崩溃函数:
  github.com/nebula/monitor/internal/server/report.(*Generator).collectSecurity
  文件: report/report.go 第 618 行

调用链:
  handleReportGenerate (middleware_api.go:1423)
  → Generator.Generate (report.go:264)
  → collectData (report.go:419)
  → collectSecurity (report.go:618)  ← 崩溃在这里

错误: assignment to entry in nil map
含义: collectSecurity 向一个未 make() 初始化的 map 写入 key，导致 Go runtime panic

触发者: 39.71.17.51 反复请求报告生成接口 (handleReportGenerate)，
每次请求都会触发一次 panic，导致 server 反复崩溃。

---

## 四、相关日志 (agent 侧)

monitor-agent:
  安全事件超过单周期上限，截断上报  total: 984, cap: 200
  (一次上报 984 个安全事件，超过 200 上限被截断)

  上报失败，将重试  attempt 1-4:
  Post "http://124.223.77.206:8080/api/v1/report": context deadline exceeded
  (安全事件过多导致上报超时，监控数据传不上去)

monitor-server:
  安全存储落盘失败:
  rename /var/lib/monitor-server/security_store.json.tmp
    /var/lib/monitor-server/security_store.json: no such file or directory
  (新增问题：安全事件存储文件写入失败，可能是 panic 导致的状态损坏)

---

## 五、根因分析

1. 上海机 SSH 端口 (22/10022/10222) 暴露公网
2. IP 39.71.17.51 等持续暴力破解，产生大量 SSH 失败事件 (每周期 984 个)
3. monitor-server 的 collectSecurity 处理大量事件时，
   向 nil map 写入触发 panic (report.go:618 的 bug)
4. panic 导致 server 反复崩溃，agent 上报超时，监控数据丢失

---

## 六、已采取措施

✅ 2026-08-28 20:56 关闭 sshd 的 22 端口 (sshd_config 注释 Port 22)
   预期：针对 22 端口的攻击流量归零，安全事件量大幅减少

待处理:
1. 关闭密码登录，只用密钥 (进一步减少 10022/10222 的攻击)
2. monitor-server report.go:618 的 nil map bug 需代码修复/升级
3. security_store.json 落盘失败需处理
