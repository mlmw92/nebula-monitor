package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/server/report"
)

// stubReportSched 是接口层的替身：真实调度语义（到点判断、跨重启保留、失败落盘）
// 由 report 包的用例覆盖，这里只钉住**接口契约**（权限、503、失败码）。
type stubReportSched struct {
	cfg   report.ScheduleConfig
	run   report.ScheduleRun
	saved []report.ScheduleConfig
}

func (s *stubReportSched) Config() report.ScheduleConfig { return s.cfg }
func (s *stubReportSched) Save(cfg report.ScheduleConfig) error {
	s.saved = append(s.saved, cfg)
	s.cfg = cfg
	return nil
}
func (s *stubReportSched) Status() report.ScheduleStatus {
	return report.ScheduleStatus{Config: s.cfg, Last: &report.ScheduleRun{At: 1700000000000}}
}
func (s *stubReportSched) RunNow(bool) report.ScheduleRun { return s.run }

func TestRoutes_ReportScheduleGetAndSave(t *testing.T) {
	a := scopeTestAPI(t)
	sched := &stubReportSched{cfg: report.ScheduleConfig{Enabled: false, Type: "weekly", IntervalHours: 24}}
	a.SetReportScheduler(sched)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:read"), http.MethodGet, "/api/v1/report/schedule", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	cfg, _ := body["config"].(map[string]any)
	if cfg == nil || cfg["type"] != "weekly" {
		t.Fatalf("应返回调度配置：%v", body)
	}
	if _, ok := body["last"]; !ok {
		t.Fatalf("应返回上次运行结果：%v", body)
	}

	// 保存：热生效（返回保存后的状态）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:export"), http.MethodPut,
		"/api/v1/report/schedule", `{"enabled":true,"type":"daily","intervalHours":6}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if len(sched.saved) != 1 || !sched.saved[0].Enabled || sched.saved[0].IntervalHours != 6 {
		t.Fatalf("保存内容不符：%+v", sched.saved)
	}

	// 请求体坏了要 400（而不是静默保存成零值配置——那会把调度悄悄关掉）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:export"), http.MethodPut, "/api/v1/report/schedule", "{bad"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法请求体应 400，实际 %d", rec.Code)
	}
}

// 权限：读用 report:read，改配置与立即生成用 report:export（它们都会产出报告文件）。
func TestRoutes_ReportSchedulePermission(t *testing.T) {
	a := scopeTestAPI(t)
	a.SetReportScheduler(&stubReportSched{})
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:export"), http.MethodGet, "/api/v1/report/schedule", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 report:read 应 403，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:read"), http.MethodPut, "/api/v1/report/schedule", `{}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 report:export 应 403，实际 %d", rec.Code)
	}
}

// 立即生成：成功 200；失败 502（"生成不出来"是服务端问题，不是请求写错了）。
func TestRoutes_ReportScheduleRunNow(t *testing.T) {
	a := scopeTestAPI(t)
	sched := &stubReportSched{run: report.ScheduleRun{At: 1700000000000, ReportID: "rpt-1", Manual: true}}
	a.SetReportScheduler(sched)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:export"), http.MethodPost, "/api/v1/report/schedule/run", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["reportId"] != "rpt-1" {
		t.Fatalf("应返回报告 ID：%v", body)
	}

	sched.run = report.ScheduleRun{At: 1700000000000, Error: "磁盘只读"}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("report:export"), http.MethodPost, "/api/v1/report/schedule/run", ""))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("生成失败应 502，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["error"] != "磁盘只读" {
		t.Fatalf("应把失败原因回给前端：%v", body)
	}
}

// 未注入调度器：三个接口都 503，而不是假装能用。
func TestRoutes_ReportScheduleDisabled(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	for _, tc := range []struct {
		method string
		target string
		perm   string
	}{
		{http.MethodGet, "/api/v1/report/schedule", "report:read"},
		{http.MethodPut, "/api/v1/report/schedule", "report:export"},
		{http.MethodPost, "/api/v1/report/schedule/run", "report:export"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(globalPrincipal(tc.perm), tc.method, tc.target, "{}"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 应 503，实际 %d", tc.method, tc.target, rec.Code)
		}
	}
}
