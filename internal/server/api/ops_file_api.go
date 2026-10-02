package api

import (
	"fmt"
	"io"
	"net/http"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/ops"
)

// 文件分发（file.push）的上传侧接口。
//
// 上传与下发刻意分成两步：先 POST /ops/files 拿到引用号（fileId），再用它创建 file.push 任务。
// 为什么不把内容直接塞进任务：任务存储是每次状态流转都**整体重写**的一份 JSON
// （internal/server/ops/store.go 的 save），内容进去会以「条数 × 文件大小」的量级膨胀。
// 内容落 ops_files/，任务里只有引用，领取时再注入到下发的那一份副本里。

// opsFileMaxUploadBytes 是上传请求体的上限。
//
// 比内容上限留出余量：multipart 有边界与表单头开销。上限收在这里而不是只靠内容校验，
// 是因为不设限时一个超大请求会先被完整读进内存，那时再拒绝已经太晚。
const opsFileMaxUploadBytes = model.OpsFileMaxBytes + (64 << 10)

// handleOpsFileUpload 上传一个待分发的文件（multipart，表单字段 file）。
func (a *API) handleOpsFileUpload(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, opsFileMaxUploadBytes)
	if err := r.ParseMultipartForm(model.OpsFileMaxBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("解析上传内容失败（单文件上限 %d KiB）：%s", model.OpsFileMaxBytes>>10, err.Error()),
		})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少上传文件（表单字段 file）"})
		return
	}
	defer file.Close()

	// 多读 1 字节：恰好卡在上限的内容要能通过，超出的才被 FileStore 拒掉，
	// 这样"超限"的错误信息由一处（FileStore）给出，不会出现两套说辞。
	content, err := io.ReadAll(io.LimitReader(file, model.OpsFileMaxBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取上传内容失败：" + err.Error()})
		return
	}
	name := ""
	if header != nil {
		name = header.Filename
	}
	operator := AuthenticatedUser(r)
	rec, err := a.ops.SaveFile(name, content, operator)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 上传本身不改变任何机器的状态，但它决定了"能往机器上写什么"，所以也留一条审计。
	// 审计中间件对 multipart 请求不读请求体、也不记录，这里显式记一次（带引用与摘要）。
	RecordChangeAudit(a.audit, r, "ops.file.upload", nil, map[string]interface{}{
		"ref": rec.Ref, "name": rec.Name, "size": rec.Size, "sha256": rec.SHA256,
	})
	writeJSON(w, http.StatusCreated, rec)
}

// handleOpsFiles 列出已上传的文件（供界面复用，不必为同一次分发重复上传）。
func (a *API) handleOpsFiles(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	files := a.ops.ListFiles()
	if files == nil {
		// 刻意给空数组而不是 null：前端列表不该为"没有数据"多写一个分支。
		files = []*ops.FileRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"files": files})
}
