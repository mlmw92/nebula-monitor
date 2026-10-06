package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func TestRabbitMQMetricsHealthFollowsExposition(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "empty"},
		{name: "unrelated", body: "process_cpu_seconds_total 1\n"},
		{name: "native down", body: "rabbitmq_instance_up 0\nrabbitmq_connections 3\n"},
		{name: "business fallback", body: "rabbitmq_connections 3\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			addr := strings.TrimPrefix(srv.URL, "http://")
			collector := NewRabbitMQCollector("node-a", []model.RabbitMQInstanceConfig{{Name: "rabbit", Addr: addr}})
			_, instances := collector.CollectCtx(context.Background())
			if len(instances) != 1 || instances[0].Up != tc.want {
				t.Fatalf("instances = %+v, want up=%v", instances, tc.want)
			}
		})
	}
}
