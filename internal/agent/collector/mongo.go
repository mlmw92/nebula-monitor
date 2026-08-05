package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoDBCollector 采集 MongoDB 实例，支持两种模式：
//   - exporter 模式：抓取 mongodb_exporter 的 /metrics（ExporterURL 非空）
//   - 直连模式：通过官方驱动连接实例，执行 serverStatus / dbStats / hello / replSetGetStatus
type MongoDBCollector struct {
	node      string
	instances []model.MongoDBInstanceConfig
}

// NewMongoDBCollector 构造 MongoDB 采集器。
func NewMongoDBCollector(node string, instances []model.MongoDBInstanceConfig) *MongoDBCollector {
	return &MongoDBCollector{node: node, instances: instances}
}

// Collect 遍历所有实例采集指标与实例元信息。
func (c *MongoDBCollector) Collect() ([]model.Metric, []model.MongoDBInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.MongoDBInstance
	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, mi := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, mi)
			continue
		}
		m, mi := c.collectDirect(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, mi)
	}
	return metrics, instances
}

func mongoInstanceMeta(c *MongoDBCollector, cfg model.MongoDBInstanceConfig) model.MongoDBInstance {
	return model.MongoDBInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, "27017"),
		Name:     cfg.Name,
		Node:     c.node,
		Topology: cfg.Topology,
		Group:    cfg.Name,
	}
}

func (c *MongoDBCollector) collectExporter(cfg model.MongoDBInstanceConfig, now int64) ([]model.Metric, model.MongoDBInstance) {
	inst := mongoInstanceMeta(c, cfg)
	body, err := fetchPrometheusText(cfg.ExporterURL)
	if err != nil {
		slog.Warn("MongoDB exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		inst.Up = false
		return nil, inst
	}
	metrics := parsePrometheusTextWithPrefix(body, c.node, inst.Instance, "mongodb_", now)
	inst.Up = true
	for _, m := range metrics {
		switch m.Name {
		case "mongodb_up":
			inst.Up = m.Value > 0.5
		case "mongodb_connections_current":
			inst.ConnCurrent = m.Value
		case "mongodb_mem_resident_bytes":
			inst.MemResident = m.Value / 1024 / 1024
		case "mongodb_instance_version", "mongodb_version":
			if v, ok := m.Labels["version"]; ok {
				inst.Version = v
			}
		}
	}
	return metrics, inst
}

func (c *MongoDBCollector) collectDirect(cfg model.MongoDBInstanceConfig, now int64) ([]model.Metric, model.MongoDBInstance) {
	inst := mongoInstanceMeta(c, cfg)
	base := map[string]string{
		"node":     c.node,
		"instance": inst.Instance,
		"addr":     cfg.Addr,
		"topology": cfg.Topology,
	}

	uri := buildMongoURI(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		slog.Warn("MongoDB 连接失败", "addr", cfg.Addr, "err", err)
		inst.Up = false
		return emitMongoUp(c.node, inst.Instance, base, 0, now), inst
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	if err = client.Ping(ctx, nil); err != nil {
		slog.Warn("MongoDB Ping 失败", "addr", cfg.Addr, "err", err)
		inst.Up = false
		return emitMongoUp(c.node, inst.Instance, base, 0, now), inst
	}
	inst.Up = true
	var metrics []model.Metric
	metrics = append(metrics, mongodbMetric(c.node, "mongodb_up", 1, base, now))

	// serverStatus
	var ss bson.M
	if err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&ss); err == nil {
		if v, ok := bsonString(ss, "version"); ok {
			inst.Version = v
			base["version"] = v
		}
		if v, ok := bsonFloat(ss, "uptime"); ok {
			inst.Uptime = v
			metrics = append(metrics, mongodbMetric(c.node, "mongodb_uptime_seconds", v, base, now))
		}
		if v, ok := bsonFloat(ss, "connections", "current"); ok {
			inst.ConnCurrent = v
			metrics = append(metrics, mongodbMetric(c.node, "mongodb_connections_current", v, base, now))
		}
		if v, ok := bsonFloat(ss, "connections", "available"); ok {
			inst.ConnAvailable = v
			metrics = append(metrics, mongodbMetric(c.node, "mongodb_connections_available", v, base, now))
		}
		if v, ok := bsonFloat(ss, "mem", "resident"); ok {
			inst.MemResident = v
			metrics = append(metrics, mongodbMetric(c.node, "mongodb_mem_resident_bytes", v*1024*1024, base, now))
		}
		if v, ok := bsonFloat(ss, "mem", "virtual"); ok {
			inst.MemVirtual = v
			metrics = append(metrics, mongodbMetric(c.node, "mongodb_mem_virtual_bytes", v*1024*1024, base, now))
		}
		for _, op := range []string{"insert", "query", "update", "delete", "getmore", "command"} {
			if v, ok := bsonFloat(ss, "opcounters", op); ok {
				metrics = append(metrics, mongodbMetric(c.node, "mongodb_opcounters_"+op, v, base, now))
				switch op {
				case "insert":
					inst.OpInsert = v
				case "query":
					inst.OpQuery = v
				case "update":
					inst.OpUpdate = v
				case "delete":
					inst.OpDelete = v
				case "command":
					inst.OpCommand = v
				}
			}
		}
	}

	// dbStats（按配置的库采集存储/对象统计）
	if cfg.Database != "" {
		var ds bson.M
		if err = client.Database(cfg.Database).RunCommand(ctx, bson.D{
			{Key: "dbStats", Value: 1},
			{Key: "scale", Value: 1},
		}).Decode(&ds); err == nil {
			if v, ok := bsonFloat(ds, "dataSize"); ok {
				inst.DbDataSize = v / 1024 / 1024
				metrics = append(metrics, mongodbMetric(c.node, "mongodb_db_dataSize_bytes", v, base, now))
			}
			if v, ok := bsonFloat(ds, "storageSize"); ok {
				inst.DbStorageSize = v / 1024 / 1024
				metrics = append(metrics, mongodbMetric(c.node, "mongodb_db_storageSize_bytes", v, base, now))
			}
			if v, ok := bsonFloat(ds, "indexSize"); ok {
				inst.DbIndexSize = v / 1024 / 1024
				metrics = append(metrics, mongodbMetric(c.node, "mongodb_db_indexSize_bytes", v, base, now))
			}
			if v, ok := bsonFloat(ds, "objects"); ok {
				inst.DbObjects = v
				metrics = append(metrics, mongodbMetric(c.node, "mongodb_db_objects", v, base, now))
			}
			if v, ok := bsonFloat(ds, "indexes"); ok {
				inst.DbIndexes = v
				metrics = append(metrics, mongodbMetric(c.node, "mongodb_db_indexes", v, base, now))
			}
		}
	}

	// hello：角色判定（primary / secondary / arbiter / mongos / standalone）
	var hello bson.M
	if err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err == nil {
		ismaster, _ := hello["ismaster"].(bool)
		secondary, _ := hello["secondary"].(bool)
		arbiterOnly, _ := hello["arbiterOnly"].(bool)
		setName, _ := bsonString(hello, "setName")
		msg, _ := bsonString(hello, "msg")
		switch {
		case msg == "isdbgrid":
			inst.Role = "MONGOS"
		case ismaster:
			inst.Role = "PRIMARY"
		case secondary:
			inst.Role = "SECONDARY"
		case arbiterOnly:
			inst.Role = "ARBITER"
		case setName != "":
			inst.Role = "OTHER"
		default:
			inst.Role = "STANDALONE"
		}
		base["role"] = inst.Role

		// 副本集状态（仅 replica set 有意义）
		if setName != "" {
			var rs bson.M
			if err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Decode(&rs); err == nil {
				if members, ok := rs["members"].(bson.A); ok {
					for _, item := range members {
						mm, ok := item.(bson.M)
						if !ok {
							continue
						}
						name, _ := bsonString(mm, "name")
						if name == cfg.Addr || name == inst.Instance {
							if v, ok := bsonFloat(mm, "state"); ok {
								inst.ReplState = v
								metrics = append(metrics, mongodbMetric(c.node, "mongodb_repl_state", v, base, now))
							}
							if v, ok := bsonFloat(mm, "health"); ok {
								inst.ReplHealth = v
								metrics = append(metrics, mongodbMetric(c.node, "mongodb_repl_health", v, base, now))
							}
							if od, ok := mm["optimeDate"].(primitive.DateTime); ok {
								lag := time.Since(od.Time()).Seconds()
								if lag >= 0 {
									inst.ReplLag = lag
									metrics = append(metrics, mongodbMetric(c.node, "mongodb_repl_lag", lag, base, now))
								}
							}
						}
					}
				}
			}
		}
	}

	return metrics, inst
}

func emitMongoUp(node, instance string, base map[string]string, v float64, now int64) []model.Metric {
	return []model.Metric{mongodbMetric(node, "mongodb_up", v, base, now)}
}

func mongodbMetric(node, name string, value float64, labels map[string]string, now int64) model.Metric {
	if labels == nil {
		labels = map[string]string{}
	}
	return model.Metric{Node: node, Name: name, Labels: labels, Value: value, Timestamp: now}
}

func buildMongoURI(cfg model.MongoDBInstanceConfig) string {
	var b strings.Builder
	b.WriteString("mongodb://")
	if cfg.User != "" {
		b.WriteString(url.QueryEscape(cfg.User))
		b.WriteString(":")
		b.WriteString(url.QueryEscape(cfg.Password))
		b.WriteString("@")
	}
	b.WriteString(cfg.Addr)
	b.WriteString("/?connectTimeoutMS=6000&socketTimeoutMS=6000&serverSelectionTimeoutMS=6000")
	if cfg.AuthSource != "" {
		b.WriteString("&authSource=")
		b.WriteString(cfg.AuthSource)
	}
	return b.String()
}

// fetchPrometheusText 拉取 exporter 的 /metrics 文本。
func fetchPrometheusText(rawURL string) (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("exporter 返回状态码 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// bsonFloat 从嵌套 bson.M 中按路径取出数值（兼容 float64/int32/int64/int）。
func bsonFloat(m bson.M, keys ...string) (float64, bool) {
	var cur interface{} = m
	for _, k := range keys {
		mm, ok := cur.(bson.M)
		if !ok {
			return 0, false
		}
		v, exists := mm[k]
		if !exists {
			return 0, false
		}
		cur = v
	}
	switch n := cur.(type) {
	case float64:
		return n, true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// bsonString 从嵌套 bson.M 中按路径取出字符串。
func bsonString(m bson.M, keys ...string) (string, bool) {
	var cur interface{} = m
	for _, k := range keys {
		mm, ok := cur.(bson.M)
		if !ok {
			return "", false
		}
		v, exists := mm[k]
		if !exists {
			return "", false
		}
		cur = v
	}
	s, ok := cur.(string)
	return s, ok
}
