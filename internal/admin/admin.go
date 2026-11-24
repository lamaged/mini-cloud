// Package admin 提供凭证管理页（v0.10.0，docs/design.md §3）：
// GET /admin 渲染表单；POST /admin/update 两阶段更新凭证（先验证后提交）。
// Basic Auth 由 main 前置校验；所有动态内容经 html/template 转义。
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"mini-cloud/internal/driver"
)

// Mount 管理页展示的挂载信息（由 main 注入）
type Mount struct {
	Name       string                   // 挂载名（与配置 name 一致）
	Prefix     string                   // URL 前缀
	Available  bool                     // 挂载当前是否可用（不可用时更新成功提示「重启后生效」）
	Fields     []driver.CredentialField // 凭证字段（静态；FieldsFunc 优先）
	FieldsFunc func() []driver.CredentialField // 渲染时现取（动态标签，如剩余有效期，PM 拍板 2026-11-19）
	Status     func() MountStatus       // 结构化状态（红黄黑 + 原因 + 最近检查时间；v1.3.0 批 1）
	Update     driver.CredentialUpdater // 更新实现
}

// MountStatus 挂载状态展示信息（应用层统一合成后注入，admin 只负责渲染）
type MountStatus struct {
	Label       string // 正常 / 警告 / 不可用
	Color       string // ok / warning / error
	Detail      string // 原因（可为空）
	LastCheckAt string // 最近检查时间（可为空）
}

// fieldList 取当前凭证字段（FieldsFunc 优先，渲染/提交同一来源）
func (m *Mount) fieldList() []driver.CredentialField {
	if m.FieldsFunc != nil {
		return m.FieldsFunc()
	}
	return m.Fields
}

// PersistFunc 把验证通过的新凭证写回配置（由 main 提供）。
// 成功时返回回滚函数（用于后续提交失败时恢复配置，实现零变化语义）；
// 回滚函数须报告成败（I8：还原失败必须如实告知用户，不得假装已还原）。
type PersistFunc func(ctx context.Context, mount string, values map[string]string) (rollback func() error, err error)

// Handler 凭证管理页处理器
type Handler struct {
	mu          sync.Mutex
	tokens      map[string]time.Time // CSRF 一次性令牌 → 过期时间
	mounts      []Mount
	persist     PersistFunc
	OnCommitted func(mount string) // 凭证生效后回调（main 用于使该挂载缓存失效）

	updateMu sync.Mutex // 更新锁：串行化「验证→写盘→生效」全流程（I1：防双标签交错导致配置与运行时不一致）
}

// New 创建管理页处理器
func New(mounts []Mount, persist PersistFunc) *Handler {
	return &Handler{
		tokens:  make(map[string]time.Time),
		mounts:  mounts,
		persist: persist,
	}
}

// randomToken 生成随机十六进制令牌（CSRF 与更新请求标识共用）
func randomToken() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

// newToken 生成一次性 CSRF 令牌（30 分钟有效），并顺带清理过期令牌防膨胀
func (h *Handler) newToken() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if len(h.tokens) > 200 {
		for k, exp := range h.tokens {
			if now.After(exp) {
				delete(h.tokens, k)
			}
		}
	}
	tok := randomToken()
	h.tokens[tok] = now.Add(30 * time.Minute)
	return tok
}

// checkToken 校验并消费一次性令牌
func (h *Handler) checkToken(tok string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	exp, ok := h.tokens[tok]
	if !ok || time.Now().After(exp) {
		return false
	}
	delete(h.tokens, tok)
	return true
}

func (h *Handler) findMount(name string) *Mount {
	for i := range h.mounts {
		if h.mounts[i].Name == name {
			return &h.mounts[i]
		}
	}
	return nil
}

// Page 渲染管理页（GET /admin）
func (h *Handler) Page(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "", "")
}

// Update 处理凭证更新（POST /admin/update）。
// 「验证→写盘→生效」全流程串行化（updateMu）：双标签并发提交按先后完整执行，避免配置与运行时不一致（I1）。
// 锁在表单解析与 CSRF 校验之后获取（I7）：慢请求不长期占锁，串行化只覆盖需要原子性的三步。
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, http.StatusBadRequest, "", "表单解析失败")
		return
	}
	if !h.checkToken(r.PostFormValue("csrf")) {
		h.render(w, http.StatusBadRequest, "", "安全校验失败，请返回管理页重试")
		return
	}

	m := h.findMount(r.PostFormValue("mount"))
	if m == nil || m.Update == nil {
		h.render(w, http.StatusBadRequest, "", "未找到挂载，或该挂载不支持在线更新")
		return
	}

	h.updateMu.Lock()
	defer h.updateMu.Unlock()

	values := make(map[string]string)
	values["__req"] = randomToken() // 更新请求唯一标识：驱动按它关联验证与提交，隔离并发更新
	for _, f := range m.fieldList() {
		values[f.Key] = strings.TrimSpace(r.PostFormValue(f.Key))
	}

	// 两阶段（docs/design.md §3.3）：先验证（零状态变化）→ 持久化配置 → 生效
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := m.Update.ValidateCredential(ctx, values); err != nil {
		// 502：验证涉及上游网络，无法区分「凭证无效」与「上游不可达」
		h.render(w, http.StatusBadGateway, "", fmt.Sprintf("验证失败，未做任何修改：%v", err))
		return
	}
	rollback, err := h.persist(ctx, m.Name, values)
	if err != nil {
		h.render(w, http.StatusInternalServerError, "", fmt.Sprintf("写入配置文件失败，未做任何修改：%v", err))
		return
	}

	err = m.Update.CommitCredential(values)
	if err == nil || errors.Is(err, driver.ErrStateWriteFailed) {
		// 凭证已生效（含「生效但本地缓存写入失败」的告警路径）
		if h.OnCommitted != nil {
			h.OnCommitted(m.Name) // 使该挂载的目录/链接缓存失效
		}
		if err != nil {
			h.render(w, http.StatusOK, fmt.Sprintf("更新成功（%v）", err), "")
			return
		}
		msg := "更新成功"
		if exp, ok := values["__expiry"]; ok && exp != "" {
			msg = fmt.Sprintf("更新成功，新凭证有效期至 %s", exp)
		}
		// 更新 ≠ 恢复（D3）：挂载当前不可用时保持红色，提示重启后生效
		if !m.Available {
			msg += "；该挂载当前不可用，重启后生效"
		}
		h.render(w, http.StatusOK, msg, "")
		return
	}

	// 提交失败（凭证未生效）：回滚配置（I6，PM 拍板方案①）；
	// 回滚失败必须如实告知（I8），不得假装已还原
	if rollback == nil {
		h.render(w, http.StatusInternalServerError, "", fmt.Sprintf("凭证生效失败：%v", err))
		return
	}
	if rbErr := rollback(); rbErr != nil {
		slog.Error("回滚配置失败", "err", rbErr)
		h.render(w, http.StatusInternalServerError, "",
			fmt.Sprintf("凭证生效失败：%v；且配置回滚失败，请手工核对 config.json（.bak 仍保留）", err))
		return
	}
	h.render(w, http.StatusInternalServerError, "", fmt.Sprintf("凭证生效失败，配置已还原：%v", err))
}

// render 渲染管理页（message 为成功提示，errMsg 为错误提示；二者互斥）
func (h *Handler) render(w http.ResponseWriter, status int, message, errMsg string) {
	type formMount struct {
		Name   string
		Prefix string
		Status MountStatus
		Fields []driver.CredentialField
		CSRF   string
	}
	data := struct {
		Message string
		Error   string
		Mounts  []formMount
	}{Message: message, Error: errMsg}

	for i := range h.mounts {
		m := &h.mounts[i]
		fields := m.fieldList() // 渲染时现取（动态标签）
		fm := formMount{Name: m.Name, Prefix: m.Prefix, Fields: fields}
		if len(fields) > 0 {
			fm.CSRF = h.newToken() // 仅凭证挂载需要 CSRF（无表单的状态卡不产生提交）
		}
		if m.Status != nil {
			fm.Status = m.Status()
		} else {
			fm.Status = MountStatus{Label: "正常", Color: "ok"}
		}
		data.Mounts = append(data.Mounts, fm)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 安全响应头（P2）：页面含凭证输入框，禁止缓存/嵌入/外部脚本
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	w.WriteHeader(status)
	if err := pageTmpl.Execute(w, data); err != nil {
		// 响应已开始写入，仅记录
		slog.Error("管理页渲染失败", "err", err)
	}
}

var pageTmpl = template.Must(template.New("admin").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>mini-cloud 管理</title>
<style>
body{font-family:sans-serif;max-width:640px;margin:30px auto;padding:0 20px}
h1{font-size:20px;border-bottom:2px solid #0366d6;padding-bottom:10px}
.err{color:#b00020;background:#fdecea;padding:10px;border-radius:6px}
.ok{color:#1b5e20;background:#e8f5e9;padding:10px;border-radius:6px}
.warn{color:#b26a00;background:#fff3e0;padding:8px;border-radius:6px;font-size:14px}
.st-ok{color:#1b5e20;font-size:14px;margin:8px 0 2px}
.st-warning{color:#b26a00;font-size:14px;margin:8px 0 2px}
.st-error{color:#b00020;font-size:14px;margin:8px 0 2px}
.card{border:1px solid #ddd;border-radius:8px;padding:16px 20px;margin:14px 0}
.card h2{margin:0 0 6px;font-size:17px}
.path{color:#666;font-weight:normal;font-size:13px}
label{display:block;margin:10px 0 4px;font-size:14px;color:#333}
input[type=password],input[type=text]{width:100%;box-sizing:border-box;padding:8px;border:1px solid #ccc;border-radius:4px;font-size:14px}
button{margin-top:12px;padding:8px 22px;background:#0366d6;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:14px}
a{color:#0366d6}
</style></head><body>
<h1>☁️ mini-cloud 凭证管理</h1>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Message}}<p class="ok">{{.Message}}</p>{{end}}
{{range .Mounts}}
<div class="card">
<h2>{{.Name}} <span class="path">{{.Prefix}}</span></h2>
<p class="st-{{.Status.Color}}">● {{.Status.Label}}</p>
{{if .Status.Detail}}<p class="warn">{{.Status.Detail}}</p>{{end}}
{{if .Status.LastCheckAt}}<p class="path">最近检查：{{.Status.LastCheckAt}}</p>{{end}}
{{if .Fields}}
<form method="POST" action="/admin/update">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="mount" value="{{.Name}}">
{{range .Fields}}
<label>{{.Label}}</label>
{{if eq .Kind "password"}}<input type="password" name="{{.Key}}" autocomplete="off" required>{{else}}<input type="text" name="{{.Key}}" required>{{end}}
{{end}}
<button type="submit">更新</button>
</form>
{{end}}
</div>
{{end}}
<p><a href="/">返回导航页</a></p>
</body></html>
`))
