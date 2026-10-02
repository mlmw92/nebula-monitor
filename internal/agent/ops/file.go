package ops

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// file.push：把中心上传的文件写到本机指定路径（**写操作**）。
//
// 这是本通道里最强的动作——其它写动作是"让某个已知单元回到它的标准状态"，
// 这个是"用中心手上的内容替换掉机器上的一个文件"。因此三道硬约束全部按
// "中心可能被绕过、版本可能不对"来设计（Agent 以 root 运行，不能假设中心是对的）：
//
//  1. 目标路径必须落在 guards.ops.file.dirs 列出的目录**之下**（带路径分隔符边界的前缀匹配）；
//  2. 父目录解析软链之后仍要在白名单内——否则 /opt/app/conf 指向 /etc 的软链就是一条逃逸路径；
//  3. 内容必须通过 sha256 校验才允许替换；原文件先改名备份，新内容先写成临时文件，
//     最后原子 rename——任何一步失败都不会留下"写了一半的配置"。
//
// 护栏在写之前判：run() 已经判过一次，这里再判不是重复劳动，而是让这个函数单独看也是安全的。

// maxBackupAttempts 是备份名冲突的重试上限（同一秒内对同一路径重复分发时会撞名）。
const maxBackupAttempts = 100

// defaultFileMode 是未指定权限时的落盘权限。
//
// 0644 而不是 0600：分发的多半是被服务读取的配置，权限过紧会让服务起不来，
// 而"内容敏感"的场景本来就应当由操作者显式指定 0600。
const defaultFileMode = 0o644

// runFilePush 执行一次文件分发。
func (e *Executor) runFilePush(cmd model.OpsCommand) model.OpsResult {
	fail := func(msg string) model.OpsResult {
		return model.OpsResult{State: model.OpsStateFailed, Message: msg}
	}

	// ---- 1. 本机护栏：总开关 + 允许目录，缺一不放行（与 svc.restart 同一形态）----
	if !e.guards.File.Write {
		return fail("本机护栏未放行文件分发（需在 agent.yaml 的 guards.ops.file 中设置 write: true）")
	}
	dirs := allowedDirForms(e.guards.File.OpsAllowedDirs())
	if len(dirs) == 0 {
		return fail("本机护栏未列出允许写入的目录（agent.yaml 的 guards.ops.file.dirs），拒绝分发")
	}

	// ---- 2. 参数本地复校（不假设服务端一定校验过）----
	target := strings.TrimSpace(cmd.Params["path"])
	if !model.OpsFilePathPattern.MatchString(target) {
		return fail("目标路径不合法：" + target)
	}
	mode := os.FileMode(defaultFileMode)
	if m := strings.TrimSpace(cmd.Params["mode"]); m != "" {
		if !model.OpsFileModePattern.MatchString(m) {
			return fail("文件权限不合法（应为 0xxx 四位八进制）：" + m)
		}
		n, err := strconv.ParseUint(m, 8, 32)
		if err != nil {
			return fail("文件权限不合法：" + m)
		}
		// 只接受 0xxx：setuid/setgid 位在服务端已被拒，这里再拒一次——分发一个 setuid
		// 文件等于远程提权，不能只靠一端把关。
		mode = os.FileMode(n)
	}
	modeStr := fmt.Sprintf("0%o", mode)

	// ---- 3. 载荷校验：长度 + 摘要，任何一步不过都不碰文件系统 ----
	blob := cmd.File
	if blob == nil {
		return fail("指令没有携带文件内容（服务端版本过旧或传输被截断），请重新下发")
	}
	if blob.Size <= 0 || blob.Size > model.OpsFileMaxBytes {
		return fail(fmt.Sprintf("文件大小 %d 超出本机接受的范围（1 字节 ~ %d KiB）", blob.Size, model.OpsFileMaxBytes>>10))
	}
	content, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil {
		return fail("文件内容不是合法的 base64：" + err.Error())
	}
	if int64(len(content)) != blob.Size {
		return fail(fmt.Sprintf("文件长度与声明不符（声明 %d 字节，实际 %d 字节）", blob.Size, len(content)))
	}
	sum := sha256.Sum256(content)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, blob.SHA256) {
		return fail("文件内容校验失败（sha256 不符）：声明 " + blob.SHA256 + "，实际 " + got)
	}

	// ---- 4. 落点判定：目录白名单（字符串）+ 软链逃逸（解析后）----
	if !inAllowedDir(target, dirs) {
		return fail("目标路径不在允许写入的目录内（本机允许：" + strings.Join(dirs, "、") + "）")
	}
	dir := filepath.Dir(target)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fail("目标目录不可用（不存在或无法解析）：" + dir + "：" + err.Error())
	}
	if !inAllowedDirOrSelf(resolvedDir, dirs) {
		return fail("目标目录解析后不在允许写入的范围内：" + resolvedDir)
	}

	// 目标是目录/设备等非普通文件时拒绝：把目录改名搬走再放一个文件上去，
	// 破坏性远超"分发一个文件"的本意。软链除外——rename 替换的是链接本身，不会写到链接指向处。
	if st, err := os.Lstat(target); err == nil && !st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0 {
		return fail("目标已存在且不是普通文件（" + st.Mode().String() + "），拒绝覆盖")
	}

	// ---- 5. 写临时文件 → 定权限 → 备份原文件 → 原子替换 ----
	f, err := os.CreateTemp(dir, ".nebula-filepush-*")
	if err != nil {
		return fail("创建临时文件失败：" + err.Error())
	}
	tmp := f.Name()
	discard := func() { _ = os.Remove(tmp) }

	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		discard()
		return fail("写入临时文件失败：" + err.Error())
	}
	// 先 sync 再 rename：否则掉电后可能出现"文件名已经换过去、内容还是空的"。
	if err := f.Sync(); err != nil {
		_ = f.Close()
		discard()
		return fail("刷新临时文件失败：" + err.Error())
	}
	if err := f.Close(); err != nil {
		discard()
		return fail("关闭临时文件失败：" + err.Error())
	}
	// 权限在 rename 之前设好：否则目标路径上会有一瞬间是个权限不对的文件。
	if err := os.Chmod(tmp, mode); err != nil {
		discard()
		return fail("设置文件权限失败：" + err.Error())
	}

	backupMsg := "无（新增文件）"
	if _, err := os.Lstat(target); err == nil {
		bak, err := backupName(target)
		if err != nil {
			discard()
			return fail(err.Error())
		}
		if err := os.Rename(target, bak); err != nil {
			discard()
			return fail("备份原文件失败：" + err.Error())
		}
		backupMsg = "已备份为 " + bak
	}
	if err := os.Rename(tmp, target); err != nil {
		discard()
		// 原文件此刻已经改名，目标位置是空的——必须在回执里说清楚，并给出备份路径，
		// 否则"配置没了"会变成一个无从下手的事故。
		return model.OpsResult{
			State:   model.OpsStateFailed,
			Message: "替换目标文件失败：" + err.Error(),
			Data: map[string]string{
				"目标路径": target,
				"原文件":  backupMsg,
				"提示":   "原文件已改名备份，目标位置当前不存在；可按上面的备份路径恢复或原样重试",
			},
		}
	}
	// 让目录项也落盘：保证 rename 的可见性在掉电后仍然成立（尽力而为，失败不影响本次结果）。
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	name := strings.TrimSpace(blob.Name)
	if name == "" {
		name = "未命名文件"
	}
	return model.OpsResult{
		State:   model.OpsStateSucceeded,
		Message: fmt.Sprintf("已写入 %s（%d 字节，权限 %s）", target, blob.Size, modeStr),
		Data: map[string]string{
			"目标路径": target,
			"文件":   fmt.Sprintf("%s（%d 字节）", name, blob.Size),
			"内容校验": "sha256 通过：" + got,
			"原文件":  backupMsg,
			"写入权限": modeStr,
		},
	}
}

// inAllowedDir 判断**文件路径**是否落在允许目录之下。
//
// 必须带路径分隔符边界：允许 /opt/app 不等于允许 /opt/application。
// 也要注意这里刻意要求"严格在其下"——传进来的若是目录，请用 inAllowedDirOrSelf。
func inAllowedDir(target string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(target, d+"/") {
			return true
		}
	}
	return false
}

// inAllowedDirOrSelf 判断某个**目录**是否等于或在允许目录之下。
//
// 与 inAllowedDir 的差别只有"等于也算通过"，但这条差别是必须的：白名单项本身就是一个目录，
// 目标文件直接放在 /opt/app 下时它的父目录恰好等于 /opt/app。用严格前缀去判会把
// "往允许目录里放文件"这个最常见的用法整体误拒。
func inAllowedDirOrSelf(dir string, dirs []string) bool {
	if inAllowedDir(dir, dirs) {
		return true
	}
	for _, d := range dirs {
		if dir == d {
			return true
		}
	}
	return false
}

// allowedDirForms 把允许目录展开成"配置里写的形态 + 解析软链后的形态"。
//
// 为什么需要后者：允许的目录本身可能是软链（如 /opt/app → /srv/app），
// 而落点校验用的是**解析后**的路径。只认配置原文的话，一个完全正当的软链目录
// 会让所有分发都被拒，而错误信息看起来像配置写错了。
func allowedDirForms(dirs []string) []string {
	out := make([]string, 0, len(dirs)*2)
	for _, d := range dirs {
		out = append(out, d)
		if resolved, err := filepath.EvalSymlinks(d); err == nil && resolved != d {
			out = append(out, resolved)
		}
	}
	return out
}

// backupName 为原文件挑一个不冲突的备份名：<路径>.bak-<秒级时间戳>。
func backupName(target string) (string, error) {
	base := target + ".bak-" + strconv.FormatInt(time.Now().Unix(), 10)
	for i := 0; i < maxBackupAttempts; i++ {
		cand := base
		if i > 0 {
			cand = base + "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(cand); os.IsNotExist(err) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("备份名冲突过多（%s 附近已有 %d 个备份），请先清理后再分发", base, maxBackupAttempts)
}
