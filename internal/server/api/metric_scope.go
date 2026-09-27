package api

import (
	"net/http"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/auth"
)

// metricTarget resolves the single effective node selector before querying storage.
// A supplied labels.node is never forwarded as a second matcher.
func (a *API) metricTarget(w http.ResponseWriter, r *http.Request, perm string, labels map[string]string) (string, bool) {
	q := r.URL.Query()
	nodes := q["node"]
	labelNodes := q["labels.node"]
	for _, values := range [][]string{nodes, labelNodes} {
		if len(values) > 1 {
			for _, value := range values[1:] {
				if value != values[0] {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点参数冲突"})
					return "", false
				}
			}
		}
	}
	node := q.Get("node")
	labelNode, hasLabel := labels["node"]
	if hasLabel {
		if labelNode == "" || (node != "" && node != labelNode) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点参数冲突"})
			return "", false
		}
		if node == "" {
			node = labelNode
		}
		delete(labels, "node")
	}
	// node here is always a query-parameter target (node / labels.node), never a path
	// parameter, so fail-closed scope checking (checkNodeTarget) is the correct helper —
	// an unregistered node has no resource ownership and must not reach TSDB.
	if !a.checkNodeTarget(w, r, perm, node) {
		return "", false
	}
	return node, true
}

// visibleMetricNodes reports whether a scoped principal owns any registered node.
func (a *API) visibleMetricNodes(p *auth.Principal) bool {
	if p == nil || p.Scope.IsGlobal() {
		return true
	}
	if a.nodeMgr == nil {
		return false
	}
	for _, n := range a.nodeMgr.ListNodes() {
		if n.Group != "" && p.CanAccessGroup(n.Group) {
			return true
		}
	}
	return false
}

// visibleMetricSeries filters storage results by registered node membership for
// restricted users. Explicit targets also require matching result labels.
func (a *API) visibleMetricSeries(p *auth.Principal, node string, series []model.Series) []model.Series {
	if p == nil || p.Scope.IsGlobal() {
		return series
	}
	out := make([]model.Series, 0, len(series))
	for _, s := range series {
		name := s.Labels["node"]
		if name != "" && (node == "" || node == name) && a.nodeGroup(name) != "" && a.nodeInScope(p, name) {
			out = append(out, s)
		}
	}
	return out
}
