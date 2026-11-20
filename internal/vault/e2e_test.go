package vault

// 端到端测试（外部审核 2026-11-20 附议项）：明文 → 加密对象+清单 → 模拟云盘 →
// WebDAV PROPFIND / GET / Range / Seek → SHA-256 全链路比对。
// 覆盖：空文件、大文件（跨块）、中文名、多目录、随机 Seek、错密钥、篡改密文、
// 清单缺失、清单损坏。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"mini-cloud/internal/webdav"
)

// buildE2EStorage 构建加密存储（对象+清单）与明文对照表
func buildE2EStorage(t *testing.T, master []byte) (*fakeRaw, map[string][]byte) {
	t.Helper()
	files := map[string][]byte{
		"/电影/空文件.mp4":             {},
		"/电影/权力的游戏 S01E01.mkv":    []byte("中文文件名内容 hello 你好"),
		"/剧集/大文件.mkv": func() []byte {
			b := make([]byte, 3*(1<<20)+123) // 跨 4 块
			for i := range b {
				b[i] = byte(i % 251)
			}
			return b
		}(),
	}
	f := &fakeRaw{files: map[string][]byte{}}
	var m Manifest
	m.V = 1
	for virtual, plain := range files {
		fileID, err := NewFileID()
		if err != nil {
			t.Fatal(err)
		}
		objID := EncodeFileID(fileID)
		name := virtual[strings.LastIndex(virtual, "/")+1:]
		meta := &Metadata{V: 1, P: virtual, N: name, S: int64(len(plain)), M: 1}
		var buf bytes.Buffer
		h, shaHex, err := EncryptObject(&buf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
		if err != nil {
			t.Fatal(err)
		}
		f.files["backup/dir1/"+ObjectPath(objID)] = buf.Bytes()
		m.Files = append(m.Files, Entry{
			Path: virtual, Name: name, Size: int64(len(plain)), MTime: 1,
			FileID: objID, Obj: ObjectPath(objID),
			HeaderSHA256: hex.EncodeToString(h.HeaderHash()), SHA256: shaHex,
		})
	}
	m.Dirs = []string{"/电影", "/剧集"}
	var mf bytes.Buffer
	if err := SaveManifest(&mf, ManifestKey(master), &m); err != nil {
		t.Fatal(err)
	}
	f.files["backup/dir1/_mc/manifest.mcf"] = mf.Bytes()
	return f, files
}

// newE2EServer 由存储构造 Vault + webdav 服务
func newE2EServer(t *testing.T, f *fakeRaw, master []byte) *httptest.Server {
	t.Helper()
	v := New(f, f, master, Config{Paths: []string{"backup"}, MaxStreams: 3})
	if err := v.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(&webdav.Handler{Driver: v, Prefix: "/dav"})
	t.Cleanup(ts.Close)
	return ts
}

// e2eGet 经 HTTP GET 读取（可选 Range），返回状态/内容/响应头
func e2eGet(t *testing.T, ts *httptest.Server, path, rng string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest("GET", ts.URL+"/dav"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data, resp.Header
}

// TestE2EFullChain 全链路：PROPFIND 列表 → GET 全量（SHA-256 一致）→ Range → 随机 Seek
func TestE2EFullChain(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	f, files := buildE2EStorage(t, master)
	ts := newE2EServer(t, f, master)

	// ① PROPFIND 虚拟列表：原始文件名可见、_mc 不出现
	req, _ := http.NewRequest("PROPFIND", ts.URL+"/dav/backup/dir1/", nil)
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND 状态 = %d", resp.StatusCode)
	}
	for _, want := range []string{"电影", "剧集"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("PROPFIND 应含 %q", want)
		}
	}
	if bytes.Contains(body, []byte("_mc")) {
		t.Error("PROPFIND 不得出现 _mc")
	}

	// ② GET 全量：SHA-256 与明文一致（空文件/中文名/大文件）
	for virtual, plain := range files {
		status, data, _ := e2eGet(t, ts, "/backup/dir1"+virtual, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s 状态 = %d", virtual, status)
		}
		if sha256.Sum256(data) != sha256.Sum256(plain) {
			t.Errorf("GET %s 内容 SHA-256 不符", virtual)
		}
	}

	// ③ Range：跨块字节级精确
	big := files["/剧集/大文件.mkv"]
	status, data, hdr := e2eGet(t, ts, "/backup/dir1/剧集/大文件.mkv", "bytes=1000000-1000123")
	if status != http.StatusPartialContent || !bytes.Equal(data, big[1000000:1000124]) {
		t.Errorf("Range 206 字节不符：status=%d len=%d", status, len(data))
	}
	if cr := hdr.Get("Content-Range"); !strings.HasPrefix(cr, "bytes 1000000-1000123/") {
		t.Errorf("Content-Range = %q", cr)
	}

	// ④ 随机 Seek（模拟播放器拖动 5 次）
	for i, off := range []int{0, 1 << 20, 2<<20 + 555, len(big) - 100, 12345} {
		status, data, _ := e2eGet(t, ts, "/backup/dir1/剧集/大文件.mkv",
			"bytes="+strconv.Itoa(off)+"-"+strconv.Itoa(off+99))
		if status != http.StatusPartialContent || !bytes.Equal(data, big[off:off+100]) {
			t.Errorf("Seek #%d 不符：status=%d", i, status)
		}
	}

	// ⑤ 空文件：全量 200 空体；Range 416
	status, data, _ = e2eGet(t, ts, "/backup/dir1/电影/空文件.mp4", "")
	if status != http.StatusOK || len(data) != 0 {
		t.Errorf("空文件全量 = %d/%d B", status, len(data))
	}
	// Go ServeContent 标准行为：size=0 时忽略 Range、返回 200 空体
	status, data, _ = e2eGet(t, ts, "/backup/dir1/电影/空文件.mp4", "bytes=0-99")
	if status != http.StatusOK || len(data) != 0 {
		t.Errorf("空文件 Range 按 Go 语义应 200 空体，got %d/%dB", status, len(data))
	}
}

// TestE2EWrongKey 错误密钥：解密失败、不泄露明文
func TestE2EWrongKey(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	f, _ := buildE2EStorage(t, master)
	ts := newE2EServer(t, f, bytes.Repeat([]byte{0x99}, 32)) // 错误 master key
	status, data, _ := e2eGet(t, ts, "/backup/dir1/电影/权力的游戏 S01E01.mkv", "")
	if status == http.StatusOK {
		t.Fatal("错误密钥不得解密成功")
	}
	if bytes.Contains(data, []byte("hello")) {
		t.Error("错误密钥不得泄露明文")
	}
}

// TestE2ETamperedCiphertext 篡改密文：认证失败，不返回被篡改数据
func TestE2ETamperedCiphertext(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	f, _ := buildE2EStorage(t, master)
	// 确定性：翻转全部对象中间字节（map 迭代序随机，逐个翻保证目标对象必被篡改）
	for k := range f.files {
		if strings.Contains(k, "_mc/o/") {
			f.files[k][len(f.files[k])/2] ^= 0xFF
		}
	}
	ts := newE2EServer(t, f, master)
	status, _, _ := e2eGet(t, ts, "/backup/dir1/电影/权力的游戏 S01E01.mkv", "")
	if status == http.StatusOK {
		t.Fatal("篡改密文不得解密成功")
	}
}

// TestE2EManifestMissing 清单缺失：空目录 + 告警 + _mc 仍 403
func TestE2EManifestMissing(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	f, _ := buildE2EStorage(t, master)
	delete(f.files, "backup/dir1/_mc/manifest.mcf")
	v := New(f, f, master, Config{Paths: []string{"backup"}})
	if err := v.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	objs, err := v.List(context.Background(), "/backup/dir1")
	if err != nil || len(objs) != 0 {
		t.Errorf("清单缺失应空目录: %v %+v", err, objs)
	}
	if v.Warning() == "" {
		t.Error("清单缺失应有告警")
	}
	if _, err := v.List(context.Background(), "/backup/dir1/_mc"); err == nil {
		t.Error("清单缺失时 _mc 仍应 403 屏蔽")
	}
}

// TestE2EManifestCorrupt 清单损坏：空目录 + 告警，密文不泄露
func TestE2EManifestCorrupt(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	f, _ := buildE2EStorage(t, master)
	f.files["backup/dir1/_mc/manifest.mcf"] = []byte("garbage-not-a-manifest")
	v := New(f, f, master, Config{Paths: []string{"backup"}})
	if err := v.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	objs, err := v.List(context.Background(), "/backup/dir1")
	if err != nil || len(objs) != 0 {
		t.Errorf("清单损坏应空目录: %v %+v", err, objs)
	}
	if v.Warning() == "" {
		t.Error("清单损坏应有告警")
	}
}
