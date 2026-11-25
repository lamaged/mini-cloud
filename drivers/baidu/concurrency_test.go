package baidu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestRefreshFailThenSuccessClears 审核 P1-A②：失败后成功 → lastRefreshErr 清零——
// 后续请求不被陈旧错误误限频（60s 窗口内仍可正常刷新），并发等待者全得 nil。
func TestRefreshFailThenSuccessClears(t *testing.T) {
	var mu sync.Mutex
	reqs := 0
	failNext := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		thisFail := failNext
		if failNext {
			failNext = false
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if thisFail {
			json.NewEncoder(w).Encode(map[string]string{"error": "server_error", "error_description": "临时故障"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "at-fresh", "expires_in": 2592000})
	}))
	defer ts.Close()

	d := newTestDriver(t.TempDir())
	d.client.tokenURL = ts.URL
	d.SetCredentialGeneration(1)

	if err := d.RefreshToken(context.Background()); err == nil {
		t.Fatal("第一次应失败")
	}
	// 拨过 60s 限频窗口（限频本只对失败生效）：下一次应真的打上游并成功
	d.refreshMu.Lock()
	d.lastRefreshAt = time.Now().Add(-2 * time.Minute)
	d.refreshMu.Unlock()
	if err := d.RefreshToken(context.Background()); err != nil {
		t.Fatalf("限频窗口过后应成功: %v", err)
	}
	// 成功已清零陈旧失败：立即（60s 内）再次刷新不得被旧错误误限频，应真的打上游并成功
	mu.Lock()
	before := reqs
	mu.Unlock()
	if err := d.RefreshToken(context.Background()); err != nil {
		t.Fatalf("成功后 60s 内不得被旧失败误限频: %v", err)
	}
	mu.Lock()
	after := reqs
	mu.Unlock()
	if after != before+1 {
		t.Errorf("成功后应立即重新请求上游：reqs %d → %d", before, after)
	}

	// 并发等待者：结果共享且全得 nil
	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = d.RefreshToken(context.Background())
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("并发等待者 %d 应得 nil（本轮成功）: %v", i, e)
		}
	}
}

// TestRefreshWaiterReadsOwnRound 审核 P1-A①：等待者捕获本轮结果对象，
// 醒来直接读本轮 err——即使新一轮已启动，也不丢本轮结果、不误报「进行中」。
func TestRefreshWaiterReadsOwnRound(t *testing.T) {
	d := newTestDriver(t.TempDir())

	// 直接构造在途轮次（单元级：捕获 rf 后等待；期间旧轮结束、新轮开始）
	rf1 := &refreshResult{done: make(chan struct{})}
	d.refreshMu.Lock()
	d.refreshInFlight = rf1
	d.refreshMu.Unlock()

	go func() {
		// 旧轮 leader 收尾：写入本轮结果并关闭；随后新轮开始（refreshInFlight 指向新对象）
		d.refreshMu.Lock()
		rf1.err = nil
		close(rf1.done)
		d.refreshInFlight = &refreshResult{done: make(chan struct{})}
		d.refreshMu.Unlock()
	}()

	<-rf1.done
	// 新设计：等待者读 rf1.err（nil）；旧设计会查全局 refreshInFlight（新轮，未关闭）→ 误报
	if rf1.err != nil {
		t.Errorf("等待者应读到本轮结果: %v", rf1.err)
	}

	// 集成级：慢刷新中，等待者醒来后结果 == 本轮结果（成功）
	var mu sync.Mutex
	reqs := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "at-fresh", "expires_in": 2592000})
	}))
	defer ts.Close()
	d2 := newTestDriver(t.TempDir())
	d2.client.tokenURL = ts.URL
	d2.SetCredentialGeneration(1)

	results := make([]error, 5)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = d2.RefreshToken(context.Background())
		}(i)
	}
	wg.Wait()
	for i, e := range results {
		if e != nil {
			t.Errorf("并发刷新 %d 应成功: %v", i, e)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if reqs != 1 {
		t.Errorf("并发刷新应只打 1 次端点：reqs=%d", reqs)
	}
}

// TestRefreshVsCommitNoOverwrite 审核 P1-B：旧刷新暂停在「兑换完成、内存提交前」，
// 插入新凭据提交 → 旧刷新复检代次后丢弃，内存/config/state 全部属于新授权。
func TestRefreshVsCommitNoOverwrite(t *testing.T) {
	d, ts := newRefreshDriver(t, true, false, nil) // rotate=true：兑换返回 rt-rotated
	defer ts.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	d.SetRotationPersist(func(ctx context.Context, g int, rt string) error {
		close(started)
		<-release // 卡在「兑换完成、写回期间」——内存提交前
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- d.RefreshToken(context.Background()) }()
	<-started

	// 模拟管理页提交新凭据的完整序列（persist 已写 config 并同步代次 → CommitCredential）
	d.SetCredentialGeneration(4)
	d.setPending("req-new", "rt-user-new", "at-user", 1000)
	if err := d.CommitCredential(map[string]string{"__req": "req-new"}); err != nil {
		t.Fatalf("CommitCredential: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}

	// 旧刷新必须丢弃：内存属于新授权
	if got := d.getRefreshToken(); got != "rt-user-new" {
		t.Errorf("旧刷新不得覆盖新授权：refresh_token = %q, want rt-user-new", got)
	}
	if got := d.client.getAccessToken(); got != "at-user" {
		t.Errorf("旧刷新不得覆盖新会话：access_token = %q, want at-user", got)
	}
	// state 缓存代次属于新授权
	tok, err := LoadToken("百度", d.stateDir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if tok.CredentialGeneration != 4 || tok.AccessToken != "at-user" {
		t.Errorf("state 应属于新授权: %+v", tok)
	}
}

// TestSaveTokenConcurrent 审核 P1-C：并发写缓存互不踩踏（唯一临时名）——
// 全部成功、最终文件为完整 JSON（要么 A 要么 B，绝无半截/互删）。
func TestSaveTokenConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 40
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			gen := i%2 + 1
			errs[i] = SaveToken("百度", dir, &TokenState{
				V: 1, AccessToken: "at", ExpiresAt: time.Now().Unix() + 3600, CredentialGeneration: gen,
			})
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("并发 SaveToken %d 失败: %v", i, e)
		}
	}
	tok, err := LoadToken("百度", dir)
	if err != nil {
		t.Fatalf("最终缓存应完整可读: %v", err)
	}
	if tok.V != 1 || tok.AccessToken != "at" || (tok.CredentialGeneration != 1 && tok.CredentialGeneration != 2) {
		t.Errorf("最终缓存内容异常: %+v", tok)
	}
	// 无残留临时文件
	entries, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*"))
	for _, e := range entries {
		t.Errorf("残留临时文件: %s", e)
	}
}
