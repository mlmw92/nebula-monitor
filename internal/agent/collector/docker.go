package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// DockerCollector 采集 Docker 容器指标，通过 Docker Engine API 自动发现宿主机上所有容器。
type DockerCollector struct {
	node      string
	instances []model.DockerInstanceConfig
}

// NewDockerCollector 创建 DockerCollector。
func NewDockerCollector(node string, instances []model.DockerInstanceConfig) *DockerCollector {
	return &DockerCollector{node: node, instances: instances}
}

// Collect 采集所有 Docker 实例（每个 cfg.Addr 对应一个 Docker daemon）下的容器指标。
func (c *DockerCollector) Collect() ([]model.Metric, []model.DockerInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.DockerInstance

	for _, cfg := range c.instances {
		m, dis := c.collectDaemon(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, dis...)
	}
	return metrics, instances
}

// collectDaemon 采集一个 Docker daemon 下所有容器。
func (c *DockerCollector) collectDaemon(cfg model.DockerInstanceConfig, now int64) ([]model.Metric, []model.DockerInstance) {
	client := newDockerHTTPClient(cfg.Addr)
	if client == nil {
		slog.Warn("Docker daemon 地址无效", "addr", cfg.Addr)
		return nil, nil
	}
	// 当前 client 按采集周期创建；显式关闭连接池，避免自定义 Transport
	// 的 keep-alive 连接被遗留到下一周期。
	defer client.CloseIdleConnections()

	// 1. 获取容器列表
	containers, err := c.listContainers(client, cfg.Addr)
	if err != nil {
		slog.Warn("Docker 获取容器列表失败", "addr", cfg.Addr, "err", err)
		return nil, nil
	}

	labels := map[string]string{
		"node": c.node,
		"group": cfg.Name,
	}
	mk := func(name string, val float64, extra map[string]string) model.Metric {
		m := model.Metric{Node: c.node, Name: name, Value: val, Timestamp: now}
		l := map[string]string{}
		for k, v := range labels {
			l[k] = v
		}
		for k, v := range extra {
			l[k] = v
		}
		m.Labels = l
		return m
	}

	var out []model.Metric
	var instances []model.DockerInstance
	totalContainers := len(containers)
	running := 0
	paused := 0
	stopped := 0

	for _, ctr := range containers {
		status := ctr.State
		switch status {
		case "running":
			running++
		case "paused":
			paused++
		case "exited", "dead":
			stopped++
		}
		up := 0.0
		if status == "running" {
			up = 1
		}
		containerName := ""
		if len(ctr.Names) > 0 {
			containerName = strings.TrimPrefix(ctr.Names[0], "/")
		}
		containerLabels := map[string]string{
			"instance": ctr.ID[:12],
			"container_name": containerName,
			"image":          ctr.Image,
			"status":         status,
		}
		out = append(out, mk("docker_container_up", up, containerLabels))

		// 采集容器资源统计（仅 running 容器）
		if status == "running" {
			stats := c.getContainerStats(client, cfg.Addr, ctr.ID)
			if stats != nil {
				cpuPercent := calcCPUPercent(stats)
				memUsage, memLimit := calcMemStats(stats)
				memPercent := 0.0
				if memLimit > 0 {
					memPercent = round2(memUsage / memLimit * 100)
				}
				netRx, netTx := calcNetStats(stats)
				diskRead, diskWrite := calcDiskStats(stats)
				pids := float64(stats.PidsStats.Current)
				out = append(out, mk("docker_container_cpu_percent", cpuPercent, containerLabels))
				out = append(out, mk("docker_container_mem_usage_bytes", memUsage, containerLabels))
				out = append(out, mk("docker_container_mem_limit_bytes", memLimit, containerLabels))
				out = append(out, mk("docker_container_mem_percent", memPercent, containerLabels))
				out = append(out, mk("docker_container_net_rx_bytes", netRx, containerLabels))
				out = append(out, mk("docker_container_net_tx_bytes", netTx, containerLabels))
				out = append(out, mk("docker_container_disk_read_bytes", diskRead, containerLabels))
				out = append(out, mk("docker_container_disk_write_bytes", diskWrite, containerLabels))
				out = append(out, mk("docker_container_pids_current", pids, containerLabels))
			}
		}
		instances = append(instances, model.DockerInstance{
			Instance: ctr.ID[:12],
			Name:     containerName,
			Node:     c.node,
			Group:    cfg.Name,
			Image:    ctr.Image,
			Status:   status,
			Up:       status == "running",
		})
	}

	// daemon 级别汇总指标
	daemonLabels := map[string]string{"instance": normalizeRemoteAddr(cfg.Addr, "")}
	out = append(out, mk("docker_containers_total", float64(totalContainers), daemonLabels))
	out = append(out, mk("docker_containers_running", float64(running), daemonLabels))
	out = append(out, mk("docker_containers_paused", float64(paused), daemonLabels))
	out = append(out, mk("docker_containers_stopped", float64(stopped), daemonLabels))

	// 镜像数
	if images, err := c.listImages(client, cfg.Addr); err == nil {
		out = append(out, mk("docker_images_total", float64(len(images)), daemonLabels))
	}

	return out, instances
}

// ---- Docker API 类型定义 ----

type dockerContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
}

type dockerImage struct {
	ID string `json:"Id"`
}

type dockerStats struct {
	Read      time.Time `json:"read"`
	PreRead   time.Time `json:"preread"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
	BlkioStats struct {
		IoServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage  uint64 `json:"usage"`
		Limit  uint64 `json:"limit"`
		Stats  map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// ---- Docker HTTP 客户端 ----

func newDockerHTTPClient(addr string) *http.Client {
	if addr == "" {
		return nil
	}
	transport := &http.Transport{}
	if strings.HasPrefix(addr, "unix://") {
		socketPath := strings.TrimPrefix(addr, "unix://")
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "unix", socketPath)
		}
		return &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		}
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func dockerBaseURL(addr string) string {
	if strings.HasPrefix(addr, "unix://") {
		return "http://docker"
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		return "http://" + addr
	}
	return addr
}

func (c *DockerCollector) listContainers(client *http.Client, addr string) ([]dockerContainer, error) {
	url := dockerBaseURL(addr) + "/containers/json?all=true"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var containers []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}
	return containers, nil
}

func (c *DockerCollector) listImages(client *http.Client, addr string) ([]dockerImage, error) {
	url := dockerBaseURL(addr) + "/images/json"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var images []dockerImage
	if err := json.NewDecoder(resp.Body).Decode(&images); err != nil {
		return nil, err
	}
	return images, nil
}

func (c *DockerCollector) getContainerStats(client *http.Client, addr, containerID string) *dockerStats {
	url := fmt.Sprintf("%s/containers/%s/stats?stream=false", dockerBaseURL(addr), containerID)
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var stats dockerStats
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil
	}
	return &stats
}

// calcCPUPercent 计算容器 CPU 使用率。
func calcCPUPercent(stats *dockerStats) float64 {
	if stats == nil {
		return 0
	}
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage)
	onlineCPUs := stats.CPUStats.OnlineCPUs
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}
	if cpuDelta > 0 && systemDelta > 0 {
		return round2((cpuDelta / systemDelta) * float64(onlineCPUs) * 100)
	}
	return 0
}

// calcMemStats 返回 (memUsage, memLimit)。
func calcMemStats(stats *dockerStats) (float64, float64) {
	if stats == nil {
		return 0, 0
	}
	return float64(stats.MemoryStats.Usage), float64(stats.MemoryStats.Limit)
}

// calcNetStats 返回 (netRx, netTx)。
func calcNetStats(stats *dockerStats) (float64, float64) {
	if stats == nil {
		return 0, 0
	}
	var rx, tx uint64
	for _, net := range stats.Networks {
		rx += net.RxBytes
		tx += net.TxBytes
	}
	return float64(rx), float64(tx)
}

// calcDiskStats 返回 (diskRead, diskWrite)。
func calcDiskStats(stats *dockerStats) (float64, float64) {
	if stats == nil {
		return 0, 0
	}
	var read, write uint64
	for _, io := range stats.BlkioStats.IoServiceBytesRecursive {
		switch io.Op {
		case "Read":
			read += io.Value
		case "Write":
			write += io.Value
		}
	}
	return float64(read), float64(write)
}
