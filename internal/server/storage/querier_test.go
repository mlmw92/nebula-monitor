package storage

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestQueryInstantWithLookbackUsesLatestMatrixSample(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		queries = append(queries, query)
		if !strings.Contains(query, `metric_name{node="node-1"}[720h]`) {
			t.Fatalf("query = %q, want range selector", query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"metric_name","node":"node-1","instance":"one"},"values":[[1700000000,"1"],[1700000100,"2"]]},{"metric":{"instance":"two","node":"node-1","__name__":"metric_name"},"values":[[1700000050,"3"]]}]}}`)
	}))
	defer srv.Close()

	s := &PromStorage{queryURL: srv.URL, httpClient: srv.Client()}
	series, err := s.QueryInstantWithLookback("node-1", "metric_name", nil, 0)
	if err != nil {
		t.Fatalf("QueryInstantWithLookback() error = %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("series count = %d, want 2", len(series))
	}
	if len(series[0].Points) != 1 || series[0].Points[0].Timestamp != 1700000100000 || series[0].Points[0].Value != 2 {
		t.Fatalf("first latest point = %+v, want timestamp/value 1700000100000/2", series[0].Points)
	}
	if len(series[1].Points) != 1 || series[1].Points[0].Timestamp != 1700000050000 || series[1].Points[0].Value != 3 {
		t.Fatalf("second latest point = %+v, want timestamp/value 1700000050000/3", series[1].Points)
	}
	if len(queries) != 1 || strings.Contains(queries[0], "timestamp(last_over_time") {
		t.Fatalf("queries = %v, want one raw range query without timestamp(last_over_time)", queries)
	}
}

func TestQueryInstantWithLookbackPreservesEmptySeriesAndReturnsErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
		want int
	}{
		{name: "empty matrix", body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`, code: http.StatusOK, want: 0},
		{name: "empty series", body: `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"node":"n"},"values":[]}]}}`, code: http.StatusOK, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			s := &PromStorage{queryURL: srv.URL, httpClient: srv.Client()}
			got, err := s.QueryInstantWithLookback("", "metric_name", nil, time.Hour)
			if tt.code == http.StatusOK && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.code != http.StatusOK && err == nil {
				t.Fatal("expected HTTP error")
			}
			if err == nil && len(got) != tt.want {
				t.Fatalf("series count = %d, want %d", len(got), tt.want)
			}
		})
	}
}

func TestQueryInstantWithLookbackEncodesSelector(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer srv.Close()

	s := &PromStorage{queryURL: srv.URL, httpClient: srv.Client()}
	if _, err := s.QueryInstantWithLookback("n", "metric_name", map[string]string{"instance": "i"}, 2*time.Hour+30*time.Minute); err != nil {
		t.Fatalf("QueryInstantWithLookback() error = %v", err)
	}
	if q := got.Get("query"); !strings.Contains(q, `metric_name{node="n",instance="i"}[2h30m0s]`) && !strings.Contains(q, `metric_name{instance="i",node="n"}[2h30m0s]`) {
		t.Fatalf("query = %q, want encoded selector with lookback", q)
	}
}
