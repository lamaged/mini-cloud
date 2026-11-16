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
	"net/http"
	"strings"
	"sync"
	"time"

	"mini-cloud/internal/driver"
)

// Mount 管理页展示的挂载信息（由 main 注入）
type Mount struct {
	Name    string                   // 挂载名（与配置 name 一致）
	Prefix  string                   // URL 前缀
	Fields  []driver.CredentialField // 凭证字段
	Warning func() string            // 当前健康提示（可为 nil）
	Update  driver.CredentialUpdater // 更新实现
}

// PersistFunc 把验证通过的新凭证写回配置（由 main 提供）。
// 失败时运行时状态必须保持零变化（两阶段语义）。
type PersistFunc func(ctx context.Context, mount string, values map[string]string) error

// Handler 凭证管理页处理器
type Handler struct {
	mu          sync.Mutex
	tokens      map[string]time.Time // CSRF 一次性令牌 → 过期时间
	mounts      []Mount
	persist     PersistFunc
	OnCommitted func(mount string) // 凭证生效后回调（main 用于使该挂载缓存失效）
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
	h.render(w, "", "")
}

// Update 处理凭证更新（POST /admin/update）
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, "", "表单解析失败")
		return
	}
	if !h.checkToken(r.PostFormValue("csrf")) {
		h.render(w, "", "安全校验失败，请返回管理页重试")
		return
	}

	m := h.findMount(r.PostFormValue("mount"))
	if m == nil || m.Update == nil {
		h.render(w, "", "未找到挂载，或该挂载不支持在线更新")
		return
	}

	values := make(map[string]string)
	values["__req"] = randomToken() // 更新请求唯一标识：驱动按它关联验证与提交，隔离并发更新
	for _, f := range m.Fields {
		values[f.Key] = strings.TrimSpace(r.PostFormValue(f.Key))
	}

	// 两阶段（docs/design.md §3.3）：先验证（零状态变化）→ 持久化配置 → 生效
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := m.Update.ValidateCredential(ctx, values); err != nil {
		h.render(w, "", fmt.Sprintf("验证失败，未做任何修改：%v", err))
		return
	}
	if err := h.persist(ctx, m.Name, values); err != nil {
		h.render(w, "", fmt.Sprintf("写入配置文件失败，未做任何修改：%v", err))
		return
	}

	err := m.Update.CommitCredential(values)
	applied := err == nil || errors.Is(err, driver.ErrStateWriteFailed)
	if applied && h.OnCommitted != nil {
		h.OnCommitted(m.Name) // 凭证已生效：使该挂载的目录/链接缓存失效
	}
	if err != nil {
		if errors.Is(err, driver.ErrStateWriteFailed) {
			// 凭证已生效，仅本地缓存写入失败——提示而非报错
			h.render(w, fmt.Sprintf("更新成功（%v）", err), "")
		} else {
			h.render(w, "", fmt.Sprintf("凭证生效失败：%v", err))
		}
		return
	}
	msg := "更新成功"
	if exp, ok := values["__expiry"]; ok && exp != "" {
		msg = fmt.Sprintf("更新成功，新凭证有效期至 %s", exp)
	}
	h.render(w, msg, "")
}

// render 渲染管理页（message 为成功提示，errMsg 为错误提示；二者互斥）
func (h *Handler) render(w http.ResponseWriter, message, errMsg string) {
	type formMount struct {
		Name   string
		Prefix string
		Warn   string
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
		fm := formMount{Name: m.Name, Prefix: m.Prefix, Fields: m.Fields, CSRF: h.newToken()}
		if m.Warning != nil {
			fm.Warn = m.Warning()
		}
		data.Mounts = append(data.Mounts, fm)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, data); err != nil {
		// 响应已开始写入，仅记录
		http.Error(w, "render failed", http.StatusInternalServerError)
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
<h2>{{.Name}} <span class="path">{{.Prefix}}/</span></h2>
{{if .Warn}}<p class="warn">当前状态：{{.Warn}}</p>{{else}}<p class="warn" style="color:#1b5e20;background:#e8f5e9">当前状态：正常</p>{{end}}
<form method="POST" action="/admin/update">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="mount" value="{{.Name}}">
{{range .Fields}}
<label>{{.Label}}</label>
{{if eq .Kind "password"}}<input type="password" name="{{.Key}}" autocomplete="off" required>{{else}}<input type="text" name="{{.Key}}" required>{{end}}
{{end}}
<button type="submit">更新</button>
</form>
</div>
{{end}}
<p><a href="/">返回导航页</a></p>
</body></html>
`))
