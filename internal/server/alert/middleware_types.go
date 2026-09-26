package alert

import "github.com/nebula/monitor/internal/server/mwreg"

// mwTypes 是中间件类型注册表：内置类型 + 由采集项模板派生的类型。
//
// 为什么用包级变量（而不是 Engine 字段）：
//   - serviceMetric 被多个评估函数直接调用，validService 更是在**规则校验**路径上被调用
//     （那里拿不到 Engine 句柄），层层透传只会让调用链变长；
//   - 两者都只做「读一份清单」，不涉及状态。
//
// 时序要求：必须在引擎启动前由 main 注入（SetMiddlewareRegistry），启动后只读。
// 未注入时退化为内置类型，行为与改造前完全一致。
var mwTypes = mwreg.BuiltinOnly()

// SetMiddlewareRegistry 注入中间件类型注册表。传入 nil 时保持内置类型不变。
func SetMiddlewareRegistry(r *mwreg.Registry) {
	if r != nil {
		mwTypes = r
	}
}

// upLabelsFor 返回该服务存活指标上的附加过滤条件。
//
// 内置类型为空（各自有独立的存活指标名）；模板派生类型为 {"template": <id>}——
// 所有模板共用 template_target_up，不过滤会让「A 模板离线」被 B 模板的实例误触发。
func upLabelsFor(svc string) map[string]string {
	if t, ok := mwTypes.Get(svc); ok {
		return t.UpLabels
	}
	return nil
}
