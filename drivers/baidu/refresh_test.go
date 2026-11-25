package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newRefreshDriver 构造带假 token 端点的驱动（rotate/fail/onReq 行为可切换）
func newRefreshDriver(t *testing.T, rotate, fail bool, onReq func(r *http.Request)) (*Driver, *httptest.Server) {
	t.Helper()
	d := newTestDriver(t.TempDir())
	ts := newFakeTokenServer(t, rotate, fail, onReq)
	d.client.tokenURL = ts.URL
	d.SetCredentialGeneration(3)
	return d, ts
}

// TestRefreshRotationPersistsConfig 批 2（§6.5/§6.6）：轮换成功 →
// 先经注入的 persist 写回 config（带当前代次）→ 成功后才提交内存 → state 缓存落盘。
func TestRefreshRotationPersistsConfig(t *testing.T) {
	var persistedGen int
	var persistedRT string
	d, ts := newRefreshDriver(t, true, false, nil)
	defer ts.Close()
	d.SetRotationPersist(func(ctx context.Context, expectedGen int, rt string) error {
		persistedGen, persistedRT = expectedGen, rt
		return nil
	})

	if err := d.RefreshToken(context.Background()); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if persistedGen != 3 || persistedRT != "rt-rotated" {
		t.Errorf("persist = (gen %d, %q), want (3, rt-rotated)", persistedGen, persistedRT)
	}
	if got := d.getRefreshToken(); got != "rt-rotated" {
		t.Errorf("内存 refresh_token = %q, want rt-rotated", got)
	}
	if got := d.client.getAccessToken(); got != "at-fresh" {
		t.Errorf("内存 access_token = %q, want at-fresh", got)
	}
	tok, err := LoadToken("百度", d.stateDir)
	if err != nil || tok.V != 1 || tok.AccessToken != "at-fresh" || tok.CredentialGeneration != 3 {
		t.Errorf("state 缓存应落盘（V=1、access、代次 3）: %+v, %v", tok, err)
	}
}

// TestRefreshGenerationMismatchDropsResult 批 2（§6.5.2）：刷新/写回期间用户提交了新凭据
// （代次变化）→ 丢弃本次刷新结果：不覆盖内存新票与新会话（config 层代次检查在
// conf.RotateBaiduToken 测试中覆盖）。
func TestRefreshGenerationMismatchDropsResult(t *testing.T) {
	persistCalled := false
	d, ts := newRefreshDriver(t, true, false, nil)
	defer ts.Close()
	d.client.SetAccessToken("at-old")
	d.SetRotationPersist(func(ctx context.Context, expectedGen int, rt string) error {
		persistCalled = true
		// 模拟管理页在刷新期间提交了新凭据（conf 层会跳过写回；驱动代次由 main 同步）
		d.SetCredentialGeneration(4)
		return nil
	})
	// 用户新凭据已在内存（模拟 CommitCredential 已生效）
	d.setRefreshToken("rt-user-new")

	if err := d.RefreshToken(context.Background()); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if !persistCalled {
		t.Error("写回回调应被调用（代次不符的写回跳过在 conf 层）")
	}
	if got := d.getRefreshToken(); got != "rt-user-new" {
		t.Errorf("迟到的轮换不得覆盖用户新凭据：got %q, want rt-user-new", got)
	}
	if got := d.client.getAccessToken(); got != "at-old" {
		t.Errorf("迟到的轮换不得覆盖用户新会话：got %q, want at-old", got)
	}
}

// TestRefreshFailureClassification 批 2（§6.6.8/§6.10）：不可恢复（expired_token）→
// Health AuthExpired（红）；网络失败 → Warning（黄）；成功后清除。
func TestRefreshFailureClassification(t *testing.T) {
	// 不可恢复
	d, ts := newRefreshDriver(t, false, true, nil)
	defer ts.Close()
	if err := d.RefreshToken(context.Background()); err == nil {
		t.Fatal("应报错")
	}
	h := d.Health()
	if !h.AuthExpired {
		t.Errorf("expired_token 应判 AuthExpired: %+v", h)
	}

	// 网络失败（连接被拒）→ 黄
	d2 := newTestDriver(t.TempDir())
	ts2 := newFakeTokenServer(t, false, false, nil)
	ts2.Close()
	d2.client.tokenURL = ts2.URL
	d2.SetCredentialGeneration(1)
	if err := d2.RefreshToken(context.Background()); err == nil {
		t.Fatal("应报错")
	}
	h2 := d2.Health()
	if h2.AuthExpired || h2.Warning == "" {
		t.Errorf("网络失败应为暂败警告而非失效: %+v", h2)
	}

	// 成功后清除
	d3, ts3 := newRefreshDriver(t, false, false, nil)
	defer ts3.Close()
	if err := d3.RefreshToken(context.Background()); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if h3 := d3.Health(); h3.AuthExpired || h3.Warning != "" {
		t.Errorf("刷新成功后健康事实应清空: %+v", h3)
	}
}

// TestRefreshRateLimit 批 2（§6.6.7）：失败后 60s 内复用失败结果，不打上游（防刷新风暴）。
func TestRefreshRateLimit(t *testing.T) {
	reqs := 0
	d, ts := newRefreshDriver(t, false, true, func(r *http.Request) { reqs++ })
	defer ts.Close()

	if err := d.RefreshToken(context.Background()); err == nil {
		t.Fatal("第一次应失败")
	}
	first := reqs
	if err := d.RefreshToken(context.Background()); err == nil {
		t.Fatal("限频窗口内应返回缓存失败")
	}
	if reqs != first {
		t.Errorf("限频窗口内不得再次请求上游：reqs = %d, want %d", reqs, first)
	}
}

// TestRefreshPersistFailureKeepsOldMemory 批 2（§6.6.4/§6.6.5）：轮换写回 config 失败
// = 严重错误——不得提交内存新值，健康事实记录失败。
func TestRefreshPersistFailureKeepsOldMemory(t *testing.T) {
	d, ts := newRefreshDriver(t, true, false, nil)
	defer ts.Close()
	d.SetRotationPersist(func(ctx context.Context, expectedGen int, rt string) error {
		return fmt.Errorf("磁盘只读")
	})

	if err := d.RefreshToken(context.Background()); err == nil {
		t.Fatal("写回失败应报错")
	}
	if got := d.getRefreshToken(); got != "rt-input" {
		t.Errorf("落盘失败不得提交内存新值：got %q, want rt-input", got)
	}
	h := d.Health()
	if h.Warning == "" || !h.StorageFailed {
		t.Errorf("写回失败应报存储异常红事实（审核 P2-1）: %+v", h)
	}
}

// TestInitRotationPersistsConfig Init 期轮换同样走写回（§6.5.2：Init 轮换不写回会让
// 旧票在下一次重启时「has been used」——11-23 事故路径）。
func TestInitRotationPersistsConfig(t *testing.T) {
	dir := t.TempDir()
	d := New("百度", "rt-input", "cid", "cs", dir)
	ts := newFakeTokenServer(t, true, false, nil)
	defer ts.Close()
	d.client.tokenURL = ts.URL

	persisted := false
	d.SetCredentialGeneration(3)
	d.SetRotationPersist(func(ctx context.Context, expectedGen int, rt string) error {
		persisted = rt == "rt-rotated" && expectedGen == 3
		return nil
	})

	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !persisted {
		t.Error("Init 期轮换必须写回 config（否则重启后旧票 has been used）")
	}
}

// TestRefreshTokenUsesCtx 批 2（§6.7）：RefreshToken(ctx) 的 ctx 必须传导到交换请求。
func TestRefreshTokenUsesCtx(t *testing.T) {
	d, ts := newRefreshDriver(t, false, false, nil)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// 让 token 端点慢响应：ctx 超时应中止
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "at", "expires_in": 100})
	})

	start := time.Now()
	err := d.RefreshToken(ctx)
	if err == nil {
		t.Fatal("ctx 超时应报错")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("ctx 未传导：耗时 %v", time.Since(start))
	}
	if strings.Contains(err.Error(), "context deadline") == false {
		t.Errorf("应为超时错误: %v", err)
	}
}

// TestRefreshInFlightShared 审核 P1-1：N 并发刷新 → token 端点仅 1 次请求，全部复用结果。
func TestRefreshInFlightShared(t *testing.T) {
	var mu sync.Mutex
	reqs := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		mu.Unlock()
		time.Sleep(100 * time.Millisecond) // 拉长窗口，确保并发等待者同时到达
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "at-fresh", "refresh_token": "rt-rotated", "expires_in": 2592000})
	}))
	defer ts.Close()

	d := newTestDriver(t.TempDir())
	d.client.tokenURL = ts.URL
	d.SetCredentialGeneration(3)
	d.SetRotationPersist(func(ctx context.Context, g int, rt string) error { return nil })

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
			t.Fatalf("并发刷新 %d 失败: %v", i, e)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if reqs != 1 {
		t.Errorf("N 并发刷新应只打 1 次端点：reqs=%d", reqs)
	}
	if got := d.getRefreshToken(); got != "rt-rotated" {
		t.Errorf("内存 refresh_token = %q, want rt-rotated", got)
	}
}
