package driver

import (
	"context"
	"errors"
	"time"

	"mini-cloud/internal/model"
)

// Driver 云盘驱动接口（只读）
type Driver interface {
	Meta
	Reader
}

// Meta 驱动元信息和生命周期
type Meta interface {
	Name() string
	Init(ctx context.Context) error
}

// Reader 读取操作
type Reader interface {
	// List 列出目录内容。path 是相对于云盘根目录的路径
	List(ctx context.Context, path string) ([]model.Obj, error)
	// Link 获取文件的下载链接或数据流。path 是相对于云盘根目录的文件路径
	Link(ctx context.Context, path string) (*model.Link, error)
}

// TokenManager Token 生命周期管理（可选接口，云盘驱动可实现此接口支持后台自动刷新）
type TokenManager interface {
	// NeedRefresh 检查 Token 是否需要刷新
	NeedRefresh() bool
	// RefreshToken 刷新 Token
	RefreshToken(ctx context.Context) error
}

// DriverHealth 驱动健康事实（v1.3.0，DESIGN-2026-11-24-01 §5.2）。
// 驱动只报事实、不报最终健康结论——红黄黑由应用层统一合成（全项目唯一判定规则）。
type DriverHealth struct {
	Warning       string     // 人读警告文案（如「刷新失败，会话暂可用」）；空 = 无警告
	ExpiresAt     *time.Time // 凭据到期时间（驱动能可靠解析时提供）
	AuthExpired   bool       // 凭据已失效/不可恢复（区别于「临近到期」；优先级最高，探测成功不得覆盖）
	StorageFailed bool       // 本地存储异常（如轮换写回配置失败；红，区别于凭据失效，批 2 审核 P2-1）
}

// HealthReporter 健康报告（可选接口，云盘驱动可实现此接口向应用层上报健康事实）
type HealthReporter interface {
	// Health 返回驱动健康事实（凭据到期、是否已失效、警告文案）
	Health() DriverHealth
}

// CredentialField 凭证字段描述（供管理页渲染表单）
type CredentialField struct {
	Key   string // 表单字段名
	Label string // 显示名
	Kind  string // "password" 或 "text"
}

// ErrStateWriteFailed 凭证已生效但本地 token state 写入失败。
// CommitCredential 用 %w 包裹该哨兵返回，调用方据此区分「生效但有告警」与「提交失败」。
var ErrStateWriteFailed = errors.New("credential applied but state write failed")

// ErrForbidden 禁止访问（Vault 的 _mc 密文对象目录：PROPFIND 隐藏、GET 403，绝不回落 302）
var ErrForbidden = errors.New("forbidden")

// ErrTooManyStreams 并发解密流超上限（max_streams 信号量：等待超时后拒绝，webdav 映射 503）
var ErrTooManyStreams = errors.New("too many concurrent streams")

// ErrUpstream 上游（云盘 CDN）失败（网络/签名/截断等；webdav 映射 502，契约 design §7.7）
var ErrUpstream = errors.New("upstream failure")

// CredentialUpdater 凭证更新（可选接口）：两阶段——先验证后提交（docs/design.md §3.2）
//
// values 中的保留键：调用方在 ValidateCredential 前写入 "__req"（本次更新请求的唯一标识，
// 驱动按它关联 Validate 与 Commit，隔离并发更新）；驱动可回填 "__expiry"（显示用，
// 格式 2006-01-02 15:04）。其余键为 CredentialField.Key。
//
// 新增保留键 "__refresh_token"（v1.3.0 批 1，DESIGN-2026-11-24-01 §6.2）：验证成功后，
// 驱动把「最终 refresh_token」（上游可能轮换过的新值）写入该键——配置写回与内存提交
// 必须使用该最终值而非表单原始输入；验证失败不得留下任何残余值；该值不得在管理页
// 回显、不得写入日志。
type CredentialUpdater interface {
	// CredentialFields 返回可更新的凭证字段（供管理页渲染表单）
	CredentialFields() []CredentialField
	// ValidateCredential 校验格式并做网络验证（调用方须带超时，建议 10s，ctx 必须传导）。
	// 不得改变任何生效状态（内存凭证、token state、config 均不动）。
	ValidateCredential(ctx context.Context, values map[string]string) error
	// CommitCredential 应用该次验证过的凭证到内存并写入 token state；同步、不联网、不回滚。
	// token state 写入失败时凭证仍生效（重启后以 config.json 为准），
	// 返回 fmt.Errorf("%w: ...", ErrStateWriteFailed, err) 由调用方按告警提示。
	CommitCredential(values map[string]string) error
}

// OAuthAuthorizer 一键授权（可选接口，批 2，DESIGN-2026-11-24-01 §6.4）：
// 授权码手工回填流程——OAuthStart 生成一次性 state 与授权 URL；
// OAuthExchange 校验 state、用授权码兑换 token，成功时回填 values["__refresh_token"]
//（最终 refresh_token，供配置写回与 CommitCredential 使用同一值）与
// values["__expiry"]（显示用），并按 values["__req"] 暂存待提交凭据。
// 断言对象必须是原始驱动（铁律 7）。
type OAuthAuthorizer interface {
	// OAuthStart 生成一次性 state（短时有效）并返回百度授权 URL（redirect_uri=oob）
	OAuthStart() (authURL string, err error)
	// OAuthExchange 校验 state（一次性消费）并用 code 兑换 token；
	// 不修改客户端内存（无副作用，验证语义同 CredentialUpdater.ValidateCredential）
	OAuthExchange(ctx context.Context, state, code string, values map[string]string) error
}
