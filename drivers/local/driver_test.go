package local_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mini-cloud/drivers/local"
)

func TestNewDriver(t *testing.T) {
	dir := t.TempDir()
	drv, err := local.New(dir)
	if err != nil {
		t.Fatalf("New(%q) error: %v", dir, err)
	}
	if drv.Name() != "local" {
		t.Errorf("Name() = %q, want %q", drv.Name(), "local")
	}
}

func TestNewDriverCreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "subdir", "data")
	drv, err := local.New(dir)
	if err != nil {
		t.Fatalf("New(%q) error: %v", dir, err)
	}

	ctx := context.Background()
	if err := drv.Init(ctx); err != nil {
		t.Errorf("Init() error: %v", err)
	}

	// 验证目录已创建
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Errorf("New should create directory: %s", dir)
	}
}

func TestListRoot(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	objs, err := drv.List(ctx, "/")
	if err != nil {
		t.Fatalf("List(/) error: %v", err)
	}

	if len(objs) == 0 {
		t.Error("List(/) should not be empty")
	}

	// 验证有文件和目录
	var foundFile, foundDir bool
	for _, obj := range objs {
		if obj.IsDir {
			foundDir = true
		} else {
			foundFile = true
			if obj.Size <= 0 {
				t.Errorf("file %q should have Size > 0, got %d", obj.Name, obj.Size)
			}
			if obj.ModTime.IsZero() {
				t.Errorf("file %q should have ModTime set", obj.Name)
			}
		}
	}
	if !foundFile {
		t.Error("should find at least one file")
	}
	if !foundDir {
		t.Error("should find at least one directory")
	}
}

func TestListSubDir(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	objs, err := drv.List(ctx, "/subdir")
	if err != nil {
		t.Fatalf("List(/subdir) error: %v", err)
	}

	if len(objs) != 1 {
		t.Errorf("List(/subdir): expected 1 file, got %d", len(objs))
	}
	if len(objs) > 0 && objs[0].Name != "hello.txt" {
		t.Errorf("List(/subdir): expected 'hello.txt', got %q", objs[0].Name)
	}
}

func TestListNotFound(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = drv.List(ctx, "/nonexistent")
	if err == nil {
		t.Error("List of nonexistent path should return error")
	}
}

func TestLinkFile(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	link, err := drv.Link(ctx, "/readme.txt")
	if err != nil {
		t.Fatalf("Link(/readme.txt) error: %v", err)
	}
	defer link.Reader.Close()

	if link.Reader == nil {
		t.Fatal("Link should have a Reader")
	}

	data, err := io.ReadAll(link.Reader)
	if err != nil {
		t.Fatalf("reading link data: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("Link data: got %q, want %q", string(data), "hello world")
	}
	if link.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("Link Content-Type: got %q, want %q", link.Header.Get("Content-Type"), "text/plain")
	}
}

func TestLinkFileInSubDir(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	link, err := drv.Link(ctx, "/subdir/hello.txt")
	if err != nil {
		t.Fatalf("Link(/subdir/hello.txt) error: %v", err)
	}
	defer link.Reader.Close()

	data, err := io.ReadAll(link.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sub file" {
		t.Errorf("got %q, want %q", string(data), "sub file")
	}
}

func TestLinkNotFound(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = drv.Link(ctx, "/nonexistent.txt")
	if err == nil {
		t.Error("Link of nonexistent file should return error")
	}
}

func TestLinkDirectory(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = drv.Link(ctx, "/subdir")
	if err == nil {
		t.Error("Link of directory should return error")
	}
}

func TestPathTraversal(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 尝试路径穿越
	_, err = drv.List(ctx, "/../../../etc")
	if err == nil {
		t.Error("path traversal should be blocked for List")
	}

	_, err = drv.Link(ctx, "/../../../etc/passwd")
	if err == nil {
		t.Error("path traversal should be blocked for Link")
	}
}

func TestMimeType(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"test.mp4", "video/mp4"},
		{"test.mp3", "audio/mpeg"},
		{"test.mkv", "video/x-matroska"},
		{"test.jpg", "image/jpeg"},
		{"test.jpeg", "image/jpeg"},
		{"test.png", "image/png"},
		{"test.pdf", "application/pdf"},
		{"test.txt", "text/plain"},
		{"test.srt", "text/plain"},
		{"test.unknown", "application/octet-stream"},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			dir := t.TempDir()
			// 在驱动根目录下创建文件
			os.WriteFile(filepath.Join(dir, tc.filename), []byte("test"), 0644)

			drv, err := local.New(dir)
			if err != nil {
				t.Fatal(err)
			}

			link, err := drv.Link(context.Background(), "/"+tc.filename)
			if err != nil {
				t.Fatalf("Link error: %v", err)
			}
			defer link.Reader.Close()

			got := link.Header.Get("Content-Type")
			if got != tc.want {
				t.Errorf("MIME type for %s: got %q, want %q", tc.filename, got, tc.want)
			}
		})
	}
}

func TestListEntries(t *testing.T) {
	dir := setupTestDir(t)

	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	objs, err := drv.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}

	// 检查 entry.Info() 失败的情况不会导致崩溃
	// (通过正常文件系统操作即可覆盖)
	for _, obj := range objs {
		if obj.Name == "" {
			t.Error("Obj should have a non-empty Name")
		}
		if !obj.IsDir && obj.Size == 0 {
			// 允许空文件
		}
	}
}

// setupTestDir 创建测试目录结构
func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// 创建文件和子目录
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello world"), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "subdir", "hello.txt"), []byte("sub file"), 0644)

	return dir
}

// 确保 local.Driver 实现了 model 所需的接口
func TestImplementsInterface(t *testing.T) {
	dir := t.TempDir()
	drv, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 验证基本接口方法存在
	_ = drv.Name()
	_ = drv.Init(context.Background())

	objs, err := drv.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = objs // List 正常工作
}

// 检查 Obj 字段完整性
func TestObjFields(t *testing.T) {
	dir := setupTestDir(t)
	drv, _ := local.New(dir)
	ctx := context.Background()

	objs, _ := drv.List(ctx, "/")
	for _, obj := range objs {
		if obj.Name == "readme.txt" {
			if obj.Size != 11 {
				t.Errorf("readme.txt size: got %d, want 11", obj.Size)
			}
			if obj.IsDir {
				t.Error("readme.txt should not be a directory")
			}
			if obj.ModTime.IsZero() {
				t.Error("readme.txt should have ModTime")
			}
		}
	}
}

// 测试 List 后获得的文件能被 Link 访问
func TestListThenLink(t *testing.T) {
	dir := setupTestDir(t)
	drv, _ := local.New(dir)
	ctx := context.Background()

	objs, _ := drv.List(ctx, "/")
	for _, obj := range objs {
		if !obj.IsDir {
			link, err := drv.Link(ctx, "/"+obj.Name)
			if err != nil {
				t.Errorf("Link of listed file %q should succeed: %v", obj.Name, err)
				continue
			}
			link.Reader.Close()
		}
	}
}

func TestEmptyDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "empty"), 0755)

	drv, _ := local.New(dir)
	ctx := context.Background()

	objs, err := drv.List(ctx, "/empty")
	if err != nil {
		t.Fatalf("List of empty dir: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("Empty dir should return 0 entries, got %d", len(objs))
	}
}
