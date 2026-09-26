package api

import (
	"encoding/json"
	"net/http"
	"strings"

	dsl "github.com/nebula/monitor/internal/template"
)

// TemplatesProvider 提供采集项模板 CRUD（由 templates 包实现）。
//
// 声明为接口而非具体类型：API 层不依赖存储实现，测试可注入替身；
// 与 dialtest 等既有模块保持同一种接线方式。
type TemplatesProvider interface {
	List() []dsl.Config
	Get(id string) (dsl.Config, bool)
	Upsert(cfg dsl.Config) error
	Validate(cfg dsl.Config) error
	Delete(id string) error
	Revision() uint64
}

// handleTemplatesList 返回全部模板与当前版本号。
// 版本号一并返回：前端可据此判断「是否有变更待下发」，也便于排查「改了没生效」。
func (a *API) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	if a.templates == nil {
		writeJSON(w, 200, map[string]interface{}{"templates": []dsl.Config{}, "revision": 0})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"templates": a.templates.List(),
		"revision":  a.templates.Revision(),
	})
}

// handleTemplateCreate 新建模板。
//
// id 已存在时返回 409 而不是覆盖：模板 id 同时是指标名前缀，覆盖会静默改变已落库指标的归属，
// 因此「新建」与「修改」必须走不同入口，避免误以为在新建。
func (a *API) handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	cfg, ok := decodeTemplateBody(w, r)
	if !ok {
		return
	}
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if _, exists := a.templates.Get(cfg.ID); exists {
		writeJSON(w, http.StatusConflict, errBody("模板 id "+cfg.ID+" 已存在；如需修改请使用更新"))
		return
	}
	if err := a.templates.Upsert(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, 200, cfg)
}

// handleTemplateUpdate 更新模板。
//
// 不允许改 id：id 决定指标名前缀，改名等价于换一套指标（历史序列会断），
// 这种操作应走「删除 + 新建」让调用方显式确认。
func (a *API) handleTemplateUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := decodeTemplateBody(w, r)
	if !ok {
		return
	}
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if cfg.ID != "" && cfg.ID != id {
		writeJSON(w, http.StatusBadRequest, errBody("不允许修改模板 id（"+id+" → "+cfg.ID+"）：id 决定指标名前缀，请改用「删除 + 新建」"))
		return
	}
	cfg.ID = id
	if _, exists := a.templates.Get(id); !exists {
		writeJSON(w, http.StatusNotFound, errBody("模板 "+id+" 不存在"))
		return
	}
	if err := a.templates.Upsert(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, 200, cfg)
}

// handleTemplateDelete 删除模板。
func (a *API) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if err := a.templates.Delete(id); err != nil {
		writeJSON(w, http.StatusNotFound, errBody("模板 "+id+" 不存在"))
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleTemplateValidate 只校验不保存，返回与保存时一致的校验结果。
//
// 存在的意义：校验规则（含 id 互为前缀、保留指标族、正则、标签越权等）都在 Go 侧，
// 前端无法自行判定；由本接口把**精确原因**交给编辑器，用户不必「保存失败再猜」。
func (a *API) handleTemplateValidate(w http.ResponseWriter, r *http.Request) {
	cfg, ok := decodeTemplateBody(w, r)
	if !ok {
		return
	}
	if a.templates == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("模板存储不可用"))
		return
	}
	if err := a.templates.Validate(cfg); err != nil {
		// errors.Join 的多条错误按行分隔，逐条返回便于前端列表展示
		writeJSON(w, 200, map[string]interface{}{
			"ok":     false,
			"errors": strings.Split(err.Error(), "\n"),
		})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "errors": []string{}})
}

// decodeTemplateBody 解析请求体中的模板配置。
// 解析失败时返回给前端的文案包含具体原因（前端 http.js 会优先展示 error 字段）。
func decodeTemplateBody(w http.ResponseWriter, r *http.Request) (dsl.Config, bool) {
	var cfg dsl.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("请求体不是合法的模板 JSON："+err.Error()))
		return cfg, false
	}
	return cfg, true
}

// errBody 统一错误响应体（前端 http.js 读取 error 字段展示）。
func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}
