package main

import "testing"

// TestMimeForExt 内置 MIME 回退表（Windows 无系统表，mkv/ts 等常见类型必须有类型）
func TestMimeForExt(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".mkv", "video/x-matroska"},
		{".MKV", "video/x-matroska"}, // 大小写不敏感
		{".ts", "video/mp2t"},
		{".m2ts", "video/mp2t"},
		{".srt", "application/x-subrip"},
		{".mp3", "audio/mpeg"},
		{".txt", "text/plain"},
	}
	for _, tc := range tests {
		got := mimeForExt(tc.ext)
		if got != tc.want {
			t.Errorf("mimeForExt(%q) = %q, want %q", tc.ext, got, tc.want)
		}
	}
	// 未知扩展名：返回空（I5：Vault 层已支持空 MIME）
	if got := mimeForExt(".madeup-ext"); got != "" {
		t.Errorf("未知扩展名应返回空，got %q", got)
	}
}
