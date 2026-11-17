package vault

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"testing"
	"time"

	"mini-cloud/internal/driver"
	"mini-cloud/internal/model"
)

// fakeRaw 内存假云盘：模拟 local 驱动（Link 返回 seekable 数据流）
type fakeRaw struct {
	files map[string][]byte // 挂载内路径 → 内容
}

func (f *fakeRaw) Name() string                          { return "local" }
func (f *fakeRaw) Init(ctx context.Context) error        { return nil }
func (f *fakeRaw) List(ctx context.Context, p string) ([]model.Obj, error) {
	p = path.Clean("/" + p)
	prefix := p
	if prefix != "/" {
		prefix += "/"
	}
	var objs []model.Obj
	seen := map[string]bool{}
	for k := range f.files {
		if path.Dir("/"+k) == p {
			name := path.Base(k)
			if !seen[name] {
				objs = append(objs, model.Obj{Name: name, Size: int64(len(f.files[k])), IsDir: false})
				seen[name] = true
			}
		}
	}
	_ = prefix
	// 目录：从文件路径推断（含显式目录条目）
	dirSeen := map[string]bool{}
	for k := range f.files {
		d := path.Dir("/" + k)
		for ; d != "/" && !dirSeen[d]; d = path.Dir(d) {
			dirSeen[d] = true
		}
	}
	for d := range dirSeen {
		if path.Dir(d) == p {
			objs = append(objs, model.Obj{Name: path.Base(d), IsDir: true})
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return objs, nil
}
func (f *fakeRaw) Link(ctx context.Context, p string) (*model.Link, error) {
	// 与真实驱动一致：路径为挂载内相对形式（无前导斜杠）
	p = path.Clean("/" + p)
	p = p[1:]
	data, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &model.Link{Reader: &memReader{Reader: bytes.NewReader(data)}}, nil
}

type memReader struct {
	*bytes.Reader
}

func (m *memReader) Close() error { return nil }

// buildTestVault 构造一个带 vault 根（backup）的假云盘 + 明文样本
func buildTestVault(t *testing.T, withManifest bool) (*fakeRaw, []byte, []byte) {
	t.Helper()
	master := bytes.Repeat([]byte{0x5A}, 32)
	plain := []byte("你好，加密视频内容 hello vault!")
	fileID := [16]byte{7, 7, 7, 7}
	objID := EncodeFileID(fileID)

	meta := &Metadata{V: 1, P: "/电影/a.mkv", N: "a.mkv", S: int64(len(plain)),
		M: 1712345678, T: "video/x-matroska"}
	var objBuf bytes.Buffer
	h, shaHex, err := EncryptObject(&objBuf, master, bytes.NewReader(plain), int64(len(plain)), fileID, meta)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}

	f := &fakeRaw{files: map[string][]byte{
		"backup/" + ObjectPath(objID): objBuf.Bytes(),
		"普通视频.mkv":                    []byte("normal-video-data"),
	}}

	if withManifest {
		m := &Manifest{V: 1,
			Files: []Entry{{
				Path: "/电影/a.mkv", Name: "a.mkv", Size: int64(len(plain)), MTime: 1712345678,
				MIME: "video/x-matroska", FileID: objID, Obj: ObjectPath(objID),
				HeaderSHA256: hex.EncodeToString(h.HeaderHash()), SHA256: shaHex,
			}},
			Dirs: []string{"/电影"},
		}
		var mf bytes.Buffer
		if err := SaveManifest(&mf, ManifestKey(master), m); err != nil {
			t.Fatalf("SaveManifest: %v", err)
		}
		f.files["backup/_mc/manifest.mcf"] = mf.Bytes()
	}
	return f, master, plain
}

func newTestVault(t *testing.T, f *fakeRaw, master []byte, maxStreams int) *Vault {
	t.Helper()
	v := New(f, f, master, Config{Paths: []string{"backup"}, MaxStreams: maxStreams})
	if err := v.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return v
}

func TestVaultListVirtual(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// vault 根：虚拟树 = 原始文件名，绝不出现 _mc
	objs, err := v.List(context.Background(), "/backup")
	if err != nil {
		t.Fatalf("List /backup: %v", err)
	}
	if len(objs) != 1 || objs[0].Name != "电影" || !objs[0].IsDir {
		t.Errorf("vault 根虚拟列表不符: %+v", objs)
	}
	objs2, err := v.List(context.Background(), "/backup/电影")
	if err != nil {
		t.Fatalf("List /backup/电影: %v", err)
	}
	if len(objs2) != 1 || objs2[0].Name != "a.mkv" || objs2[0].IsDir {
		t.Errorf("虚拟文件列表不符: %+v", objs2)
	}
	// D3：ETag 与 ContentType 唯一来源
	if objs2[0].ETag == "" || objs2[0].ContentType != "video/x-matroska" {
		t.Errorf("ETag/ContentType 未填充: %+v", objs2[0])
	}
	// _mc 屏蔽：PROPFIND/List 403
	if _, err := v.List(context.Background(), "/backup/_mc"); !errors.Is(err, driver.ErrForbidden) {
		t.Errorf("_mc List 应 403，got %v", err)
	}
}

func TestVaultLinkPlaintextAndForbidden(t *testing.T) {
	f, master, plain := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// 虚拟文件 → 明文
	link, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	got, err := io.ReadAll(link.Reader)
	link.Reader.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Error("解密内容与明文不符")
	}
	if link.Header.Get("Content-Type") != "video/x-matroska" {
		t.Errorf("Content-Type = %q", link.Header.Get("Content-Type"))
	}

	// _mc 密文对象：403，绝不返回密文
	if _, err := v.Link(context.Background(), "/backup/_mc/manifest.mcf"); !errors.Is(err, driver.ErrForbidden) {
		t.Errorf("_mc Link 应 403，got %v", err)
	}
	if _, err := v.Link(context.Background(), "/backup/_mc/o/x.bin"); !errors.Is(err, driver.ErrForbidden) {
		t.Errorf("对象 Link 应 403，got %v", err)
	}
	// 虚拟树中不存在的路径：not found
	if _, err := v.Link(context.Background(), "/backup/不存在.mkv"); err == nil {
		t.Error("虚拟树外路径应 not found")
	}
}

func TestVaultNonVaultDelegates(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	// 非 vault 路径：原样委托（302 行为不变）
	link, err := v.Link(context.Background(), "/普通视频.mkv")
	if err != nil {
		t.Fatalf("Link 普通: %v", err)
	}
	got, _ := io.ReadAll(link.Reader)
	link.Reader.Close()
	if string(got) != "normal-video-data" {
		t.Error("非 vault 路径未正确委托")
	}
}

func TestVaultMissingManifestEmptyDir(t *testing.T) {
	f, master, _ := buildTestVault(t, false) // backup 目录存在但无 manifest
	v := newTestVault(t, f, master, 4)
	defer v.Stop()

	objs, err := v.List(context.Background(), "/backup")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("manifest 缺失应显示空目录: %+v", objs)
	}
	if v.Warning() == "" {
		t.Error("manifest 缺失应有健康提示")
	}
}

func TestVaultStreamLimit(t *testing.T) {
	f, master, _ := buildTestVault(t, true)
	v := New(f, f, master, Config{Paths: []string{"backup"}, MaxStreams: 1})
	if err := v.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer v.Stop()

	old := acquireTimeout
	acquireTimeout = 100 * time.Millisecond
	defer func() { acquireTimeout = old }()

	link1, err := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err != nil {
		t.Fatalf("Link 1: %v", err)
	}
	// 第 2 路超上限：必须被拒绝（不排队）
	if _, err := v.Link(context.Background(), "/backup/电影/a.mkv"); err == nil {
		link1.Reader.Close()
		t.Fatal("max_streams=1 时第二路应被拒绝")
	}
	// 关闭第一路后重试应成功
	link1.Reader.Close()
	link2, err2 := v.Link(context.Background(), "/backup/电影/a.mkv")
	if err2 != nil {
		t.Fatalf("关闭后 Link 2: %v", err2)
	}
	link2.Reader.Close()
}
