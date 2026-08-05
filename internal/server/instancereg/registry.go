// Package instancereg 维护各 agent 最近一次上报的中间件实例清单（内存注册表）。
//
// 问题背景：VictoriaMetrics 的 lookback-delta（约 5 分钟）会使旧样本变为 stale，
// 在 agent 离线超过该窗口后，对应的 *_instance_up 即时查询返回空，导致 API 无法
// 枚举到任何实例，前端误判为"尚未配置 Xxx 监控"。
//
// 解决方式：receiver 在每次上报时把实例清单写入本注册表；API 查询时以注册表为基准
// 枚举所有"已配置"的实例（默认离线），再用实时的 *_instance_up 覆盖在线状态。
// 这样即使 agent 离线，实例仍会以"离线"状态展示，而不是"未配置"。
package instancereg

import (
	"strings"
	"sync"

	"github.com/nebula/monitor/internal/model"
)

// Registry 按节点保存各类型中间件最近一次上报的实例清单。
type Registry struct {
	mu        sync.RWMutex
	mysql     map[string][]model.MySQLInstance
	redis     map[string][]model.RedisInstance
	postgres  map[string][]model.PostgresInstance
	nginx     map[string][]model.NginxInstance
	kafka     map[string][]model.KafkaInstance
	docker    map[string][]model.DockerInstance
	rocketmq  map[string][]model.RocketMQInstance
	k8s       map[string][]model.K8sInstance
	mongodb   map[string][]model.MongoDBInstance
	fastdfs   map[string][]model.FastDFSInstance
}

// Default 全局默认注册表，由 receiver 在每次上报时写入，API 在查询时读取。
var Default = New()

func New() *Registry {
	return &Registry{
		mysql:    map[string][]model.MySQLInstance{},
		redis:    map[string][]model.RedisInstance{},
		postgres: map[string][]model.PostgresInstance{},
		nginx:    map[string][]model.NginxInstance{},
		kafka:    map[string][]model.KafkaInstance{},
		docker:   map[string][]model.DockerInstance{},
		rocketmq: map[string][]model.RocketMQInstance{},
		k8s:      map[string][]model.K8sInstance{},
		mongodb:  map[string][]model.MongoDBInstance{},
		fastdfs:  map[string][]model.FastDFSInstance{},
	}
}

// normalizeNode 规范化节点标识，使其与 *_instance_up 指标写入/查询时使用的 node 标签一致。
func normalizeNode(node string) string {
	node = strings.TrimSpace(strings.ToLower(node))
	if node == "localhost" || node == "127.0.0.1" || node == "::1" {
		return "localhost"
	}
	return node
}

// ---- MySQL ----

func (r *Registry) SetMySQL(node string, instances []model.MySQLInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mysql[normalizeNode(node)] = instances
}

func (r *Registry) MySQLInstances() []model.MySQLInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.MySQLInstance
	for _, v := range r.mysql {
		all = append(all, v...)
	}
	return all
}

// ---- Redis ----

func (r *Registry) SetRedis(node string, instances []model.RedisInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.redis[normalizeNode(node)] = instances
}

func (r *Registry) RedisInstances() []model.RedisInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.RedisInstance
	for _, v := range r.redis {
		all = append(all, v...)
	}
	return all
}

// ---- PostgreSQL ----

func (r *Registry) SetPostgres(node string, instances []model.PostgresInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.postgres[normalizeNode(node)] = instances
}

func (r *Registry) PostgresInstances() []model.PostgresInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.PostgresInstance
	for _, v := range r.postgres {
		all = append(all, v...)
	}
	return all
}

// ---- Nginx ----

func (r *Registry) SetNginx(node string, instances []model.NginxInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nginx[normalizeNode(node)] = instances
}

func (r *Registry) NginxInstances() []model.NginxInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.NginxInstance
	for _, v := range r.nginx {
		all = append(all, v...)
	}
	return all
}

// ---- Kafka ----

func (r *Registry) SetKafka(node string, instances []model.KafkaInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kafka[normalizeNode(node)] = instances
}

func (r *Registry) KafkaInstances() []model.KafkaInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.KafkaInstance
	for _, v := range r.kafka {
		all = append(all, v...)
	}
	return all
}

// ---- Docker ----

func (r *Registry) SetDocker(node string, instances []model.DockerInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docker[normalizeNode(node)] = instances
}

func (r *Registry) DockerInstances() []model.DockerInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.DockerInstance
	for _, v := range r.docker {
		all = append(all, v...)
	}
	return all
}

// ---- RocketMQ ----

func (r *Registry) SetRocketMQ(node string, instances []model.RocketMQInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rocketmq[normalizeNode(node)] = instances
}

func (r *Registry) RocketMQInstances() []model.RocketMQInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.RocketMQInstance
	for _, v := range r.rocketmq {
		all = append(all, v...)
	}
	return all
}

// ---- Kubernetes ----

func (r *Registry) SetK8s(node string, instances []model.K8sInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.k8s[normalizeNode(node)] = instances
}

func (r *Registry) K8sInstances() []model.K8sInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.K8sInstance
	for _, v := range r.k8s {
		all = append(all, v...)
	}
	return all
}

// ---- MongoDB ----

func (r *Registry) SetMongoDB(node string, instances []model.MongoDBInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mongodb[normalizeNode(node)] = instances
}

func (r *Registry) MongoDBInstances() []model.MongoDBInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.MongoDBInstance
	for _, v := range r.mongodb {
		all = append(all, v...)
	}
	return all
}

// ---- FastDFS ----

func (r *Registry) SetFastDFS(node string, instances []model.FastDFSInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fastdfs[normalizeNode(node)] = instances
}

func (r *Registry) FastDFSInstances() []model.FastDFSInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []model.FastDFSInstance
	for _, v := range r.fastdfs {
		all = append(all, v...)
	}
	return all
}
