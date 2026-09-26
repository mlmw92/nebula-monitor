// 命令 fakevm：验证用的假时序库（实现 VictoriaMetrics 的写入接口）。
//
// 为什么需要它：Server 写入走 Prometheus remote_write（protobuf + snappy），
// 用真实 VictoriaMetrics 需要外网下载二进制；这里只做「解压 + 落盘」，
// 用 snappy 解压后的字节即可断言「哪些指标与标签真的走到了时序库写入路径」
// （protobuf 的字符串字段是明文）。查询接口返回空结果——验证只关心写入路径。
//
// 用法（在模块内构建）：go build -o /tmp/fakevm ./build/template-fakes/fakevm
//
//	fakevm -addr 127.0.0.1:18428 -out /tmp/vm-received.bin
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/golang/snappy"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18428", "监听地址")
	out := flag.String("out", "/tmp/vm-received.bin", "解码后的写入内容落盘位置")
	flag.Parse()

	http.HandleFunc("/api/v1/write", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, err := snappy.Decode(nil, body)
		if err != nil {
			log.Printf("snappy 解压失败: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fh, err := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer fh.Close()
		if _, err := fh.Write(raw); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("remote_write 收到 %d 字节（解码后 %d 字节）", len(body), len(raw))
		w.WriteHeader(http.StatusNoContent)
	})

	empty := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": []any{}},
		})
	}
	http.HandleFunc("/api/v1/query", empty)
	http.HandleFunc("/api/v1/query_range", empty)

	log.Printf("假时序库监听 %s，写入内容落盘 %s", *addr, *out)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
