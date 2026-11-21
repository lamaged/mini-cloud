// mcrypt —— mini-cloud Vault 加密工具（Vault Phase 0）。
//
// 用法：
//
//	mcrypt keygen  [-key vault.key]                         生成 32B master key（0600；已存在则拒绝，--force 覆盖）
//	mcrypt encrypt -key vault.key -in 明文目录 -out vault根  加密整个目录树（manifest 最后写入）
//	mcrypt decrypt -key vault.key -in vault根 -out 明文目录  还原明文（校验 SHA-256）
//	mcrypt verify  -key vault.key -in vault根               校验对象与 manifest 的一致性
//	mcrypt bench                                            AES-256-GCM 吞吐基准（AX6 验收用）
//
// 格式规范见 docs/vault-feasibility.md §7.3；不变量：每次加密生成新 fileID（禁止复用）。
package main

import (
	"runtime"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mini-cloud/internal/vault"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = cmdKeygen(os.Args[2:])
	case "encrypt":
		err = cmdEncrypt(os.Args[2:])
	case "decrypt":
		err = cmdDecrypt(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "bench":
		err = cmdBench(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mcrypt —— mini-cloud Vault 加密工具（Phase 0）

  mcrypt keygen  [-key vault.key] [--force]
  mcrypt encrypt -key vault.key -in <明文目录> -out <vault根目录>
  mcrypt decrypt -key vault.key -in <vault根目录> -out <明文目录>
  mcrypt verify  -key vault.key -in <vault根目录>
  mcrypt bench
`)
}

func loadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取密钥失败: %w", err)
	}
	if len(data) != 32 {
		return nil, fmt.Errorf("密钥长度 %d != 32", len(data))
	}
	return data, nil
}

func cmdKeygen(args []string) error {
	fset := flag.NewFlagSet("keygen", flag.ExitOnError)
	keyPath := fset.String("key", "vault.key", "密钥输出路径")
	force := fset.Bool("force", false, "覆盖已存在的密钥文件（危险：旧密钥作废，已加密数据全部无法解密）")
	fset.Parse(args)

	key, err := vault.NewMasterKey()
	if err != nil {
		return err
	}
	// P0（外部审核 2026-11-20）：排他创建，防误覆盖——
	// 覆盖 = 所有用旧密钥加密的数据永久不可解，默认必须拒绝
	if !*force {
		f, err := os.OpenFile(*keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("密钥文件已存在（%s）——覆盖会导致所有已加密数据无法解密；确认要作废旧密钥请加 --force", *keyPath)
			}
			return fmt.Errorf("创建密钥文件失败: %w", err)
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			os.Remove(*keyPath) // 写入失败：清理不完整文件，不留半成品
			return fmt.Errorf("写密钥失败: %w", err)
		}
		if err := f.Close(); err != nil {
			os.Remove(*keyPath) // 关闭失败（如落盘错误）：同样不留半成品
			return fmt.Errorf("关闭密钥文件失败: %w", err)
		}
	} else {
		// R2-2（外部审核 2026-11-20）：--force 原子替换（temp + fsync + rename）——
		// 先截断旧钥再写（O_TRUNC）在写失败时会永久丢失旧密钥；原子替换则
		// 任何失败旧钥纹丝不动，且 temp 显式 0600（存量文件权限不收紧的坑一并解决）
		if err := writeKeyAtomic(*keyPath, key); err != nil {
			return fmt.Errorf("覆盖密钥失败（旧密钥未受影响）: %w", err)
		}
	}
	if *force {
		fmt.Printf("已覆盖 master key：%s（32 字节，0600）——旧密钥已作废！\n", *keyPath)
	} else {
		fmt.Printf("已生成 master key：%s（32 字节，0600）\n", *keyPath)
	}
	fmt.Println("警告：master key 永不传上网盘、必须离线备份；丢失 = 数据不可恢复。")
	return nil
}

// mustDecodeFileID 解码 fileID（解密前已由 DecodeFileID 校验过，此处仅重解码用于路径比对）
func mustDecodeFileID(s string) [16]byte {
	id, _ := vault.DecodeFileID(s)
	return id
}

// writeKeyAtomic 原子写密钥文件（temp 同目录 + chmod 0600 + fsync + rename）；
// 任一步失败删除 temp、正式文件不动（R2-2）
func writeKeyAtomic(keyPath string, key []byte) error {
	dir := filepath.Dir(keyPath)
	tmp, err := os.CreateTemp(dir, ".vault.key.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(key); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, keyPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	// P2-2（外部审核 2026-11-20）：父目录同步——rename 的目录项持久化
	//（与 conf.syncDir 同口径）。第四轮补充：同步失败/目录打不开时打警告
	//（此时 rename 已成功、旧钥已替换，不能返回失败暗示旧钥仍有效——
	// 仅提示「替换动作」的持久性风险）
	if d, err := os.Open(filepath.Dir(keyPath)); err == nil {
		if err := d.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "警告：密钥目录同步失败（替换已成功，断电可能丢失本次替换）：%v\n", err)
		}
		d.Close()
	} else {
		fmt.Fprintf(os.Stderr, "警告：无法打开密钥目录做同步（替换已成功）：%v\n", err)
	}
	return nil
}

func cmdEncrypt(args []string) error {
	fset := flag.NewFlagSet("encrypt", flag.ExitOnError)
	keyPath := fset.String("key", "vault.key", "master key 路径")
	inDir := fset.String("in", "", "明文目录")
	outDir := fset.String("out", "", "vault 根目录（将创建 _mc/）")
	fset.Parse(args)
	if *inDir == "" || *outDir == "" {
		return errors.New("encrypt 需要 -in 与 -out")
	}
	key, err := loadKey(*keyPath)
	if err != nil {
		return err
	}
	// P3：输出目录不得位于输入目录内部（否则把生成的对象再当明文加密）
	inAbs, err := filepath.Abs(*inDir)
	if err != nil {
		return err
	}
	outAbs, err := filepath.Abs(*outDir)
	if err != nil {
		return err
	}
	if outAbs == inAbs || strings.HasPrefix(outAbs, inAbs+string(filepath.Separator)) {
		return errors.New("输出目录不能位于输入目录内部（会把生成的对象再加密一遍）")
	}

	m := &vault.Manifest{V: 1}
	fileCount, totalSize := 0, int64(0)

	err = filepath.WalkDir(*inDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(*inDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		virtual := "/" + filepath.ToSlash(rel)
		// R2-5（外部审核 2026-11-20）：提前校验虚拟路径规范——
		// 超长/超深的本地目录在加密时即报错，而非生成服务端拒载的清单
		if err := vault.ValidateVirtualPath(virtual); err != nil {
			return fmt.Errorf("路径不规范: %w", err)
		}
		if d.IsDir() {
			m.Dirs = append(m.Dirs, virtual)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			fmt.Printf("  跳过 %s（非普通文件）\n", virtual)
			return nil
		}

		fileID, err := vault.NewFileID()
		if err != nil {
			return err
		}
		idStr := vault.EncodeFileID(fileID)
		objPath := vault.ObjectPath(idStr)
		meta := &vault.Metadata{
			V: 1, P: virtual, N: filepath.Base(rel), S: info.Size(),
			M: info.ModTime().Unix(), T: mimeForExt(filepath.Ext(rel)),
		}

		objFull := filepath.Join(*outDir, filepath.FromSlash(objPath))
		if err := os.MkdirAll(filepath.Dir(objFull), 0755); err != nil {
			return err
		}
		f, err := os.Create(objFull)
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			f.Close()
			return err
		}
		h, shaHex, err := vault.EncryptObject(f, key, in, info.Size(), fileID, meta)
		in.Close()
		if err != nil {
			f.Close()
			os.Remove(objFull) // P10：删除半成品，避免 _mc 累积无主对象
			return fmt.Errorf("加密 %s 失败（半成品已删除）: %w", virtual, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
		m.Files = append(m.Files, vault.Entry{
			Path: virtual, Name: meta.N, Size: meta.S, MTime: meta.M, MIME: meta.T,
			FileID: idStr, Obj: objPath,
			HeaderSHA256: hex.EncodeToString(h.HeaderHash()),
			SHA256:       shaHex,
		})
		fileCount++
		totalSize += info.Size()
		fmt.Printf("  已加密 %s（%d B）→ %s\n", virtual, info.Size(), objPath)
		return nil
	})
	if err != nil {
		return err
	}

	// manifest 最后写入（原子性：先有对象后有清单）
	manifestDir := filepath.Join(*outDir, "_mc")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		return err
	}
	// #6b（外部审核 2026-11-20）：临时文件 + 原子 rename——写失败不破坏原 manifest
	finalPath := filepath.Join(manifestDir, "manifest.mcf")
	mf, err := os.CreateTemp(manifestDir, "manifest.mcf.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := mf.Name()
	if err := vault.SaveManifest(mf, vault.ManifestKey(key), m); err != nil {
		mf.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := mf.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("manifest 写入失败: %w", err)
	}
	fmt.Printf("完成：%d 个文件（%d B）加密到 %s，manifest 已写入\n", fileCount, totalSize, *outDir)
	return nil
}

// safeOutPath 统一输出路径安全处理（P2-1，外部审核 2026-11-20）：
// ① 虚拟路径规范校验（含拒反斜杠）；② 词法边界（拼接后在 outDir 内）；
// ③ 逐级检查已存在的目录不是符号链接——词法检查只能证明「路径字符串在目录内」，
// 不能证明「真实写入位置在目录内」（预置符号链接目录可把写入引到 outDir 之外）。
// Files 与 Dirs 必须都走本函数（禁止分别实现）。
func safeOutPath(outDir, virtual string) (string, error) {
	if err := vault.ValidateVirtualPath(virtual); err != nil {
		return "", fmt.Errorf("%s: 路径不规范，拒绝解密: %w", virtual, err)
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return "", fmt.Errorf("输出目录解析失败: %w", err)
	}
	rel := filepath.FromSlash(strings.TrimPrefix(virtual, "/"))
	target := filepath.Join(outAbs, rel)
	if !strings.HasPrefix(target, outAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: 输出路径越界，拒绝解密", virtual)
	}
	// 逐级符号链接检查：已存在的每一级目录不得是符号链接
	//（Windows 交接点/重解析点在 Go 中同样报 ModeSymlink）；未存在的级别安全。
	cur := outAbs
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "" {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				break // 剩余级别尚不存在，可安全创建
			}
			return "", fmt.Errorf("检查输出路径失败: %w", err)
		}
		// 符号链接 + Windows 目录交接点（junction）一并拒绝：
		// Go 1.23 起 Windows 重解析点（Mount Point 类型）不再置 ModeSymlink、
		// 而归入 ModeIrregular——仅查 ModeSymlink 会漏掉 junction（外部审核
		// 2026-11-20 第四轮）。ModeIrregular 仅按 Windows 门控，避免 Linux
		// 上特殊文件类型误伤。
		if fi.Mode()&os.ModeSymlink != 0 || (runtime.GOOS == "windows" && fi.Mode()&os.ModeIrregular != 0) {
			return "", fmt.Errorf("%s: 输出路径含符号链接/交接点目录，拒绝解密", virtual)
		}
	}
	return target, nil
}

func cmdDecrypt(args []string) error {
	fset := flag.NewFlagSet("decrypt", flag.ExitOnError)
	keyPath := fset.String("key", "vault.key", "master key 路径")
	inDir := fset.String("in", "", "vault 根目录")
	outDir := fset.String("out", "", "明文输出目录")
	fset.Parse(args)
	if *inDir == "" || *outDir == "" {
		return errors.New("decrypt 需要 -in 与 -out")
	}
	key, err := loadKey(*keyPath)
	if err != nil {
		return err
	}

	m, err := loadManifestFile(*inDir, key)
	if err != nil {
		return err
	}
	count := 0
	for _, e := range m.Files {
		// R2-4（外部审核 2026-11-20）：对象路径必须与 fileID 推导一致——
		// 防清单把解密指向任意文件（含符号链接目标）
		if e.Obj != vault.ObjectPath(vault.EncodeFileID(mustDecodeFileID(e.FileID))) {
			return fmt.Errorf("%s: 对象路径与 fileID 不一致，拒绝解密", e.Path)
		}
		fileID, err := vault.DecodeFileID(e.FileID)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Path, err)
		}
		headerSHA, err := hex.DecodeString(e.HeaderSHA256)
		if err != nil {
			return fmt.Errorf("%s: header_sha256 解析失败: %w", e.Path, err)
		}
		obj, err := os.Open(filepath.Join(*inDir, filepath.FromSlash(e.Obj)))
		if err != nil {
			return fmt.Errorf("%s: 打开对象失败: %w", e.Path, err)
		}
		r, err := vault.OpenDecryptReader(obj, key, fileID, headerSHA)
		if err != nil {
			obj.Close()
			return fmt.Errorf("%s: %w", e.Path, err)
		}
		// P2-1：统一安全路径处理（规范校验 + 词法边界 + 符号链接检查）
		outPath, err := safeOutPath(*outDir, e.Path)
		if err != nil {
			r.Close()
			return err
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			r.Close()
			return err
		}
		out, err := os.Create(outPath)
		if err != nil {
			r.Close()
			return err
		}
		hasher := sha256.New()
		if _, err := io.Copy(io.MultiWriter(out, hasher), r); err != nil {
			out.Close()
			r.Close()
			return fmt.Errorf("%s: 还原失败: %w", e.Path, err)
		}
		out.Close()
		r.Close()
		if hex.EncodeToString(hasher.Sum(nil)) != e.SHA256 {
			return fmt.Errorf("%s: SHA-256 不一致（数据损坏）", e.Path)
		}
		if e.MTime > 0 {
			os.Chtimes(outPath, time.Unix(e.MTime, 0), time.Unix(e.MTime, 0))
		}
		fmt.Printf("  已还原 %s（%d B）\n", e.Path, e.Size)
		count++
	}
	// 重建空目录（manifest Dirs 记录）——P2-1：与文件同一安全路径处理
	//（此前 Dirs 完全未校验，是漏网）
	for _, d := range m.Dirs {
		dirPath, err := safeOutPath(*outDir, d)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return err
		}
	}
	fmt.Printf("完成：%d 个文件还原到 %s（SHA-256 全部一致）\n", count, *outDir)
	return nil
}

func cmdVerify(args []string) error {
	fset := flag.NewFlagSet("verify", flag.ExitOnError)
	keyPath := fset.String("key", "vault.key", "master key 路径")
	inDir := fset.String("in", "", "vault 根目录")
	fset.Parse(args)
	if *inDir == "" {
		return errors.New("verify 需要 -in")
	}
	key, err := loadKey(*keyPath)
	if err != nil {
		return err
	}
	m, err := loadManifestFile(*inDir, key)
	if err != nil {
		return err
	}
	okCount, failCount := 0, 0
	for _, e := range m.Files {
		fileID, err := vault.DecodeFileID(e.FileID)
		if err != nil {
			fmt.Printf("  ✗ %s: %v\n", e.Path, err)
			failCount++
			continue
		}
		headerSHA, err := hex.DecodeString(e.HeaderSHA256)
		if err != nil || len(headerSHA) != 32 {
			// #8（外部审核 2026-11-20）：解码失败/长度异常 → 校验失败（fail-closed），
			// 不得以 nil 跳过头部哈希校验
			fmt.Printf("  ✗ %s（header_sha256 非法）\n", e.Path)
			failCount++
			continue
		}
		obj, err := os.Open(filepath.Join(*inDir, filepath.FromSlash(e.Obj)))
		if err != nil {
			fmt.Printf("  ✗ %s: 对象缺失\n", e.Path)
			failCount++
			continue
		}
		r, err := vault.OpenDecryptReader(obj, key, fileID, headerSHA)
		if err != nil {
			fmt.Printf("  ✗ %s: %v\n", e.Path, err)
			obj.Close()
			failCount++
			continue
		}
		hasher := sha256.New()
		if _, err := io.Copy(hasher, r); err != nil {
			fmt.Printf("  ✗ %s: 读取失败: %v\n", e.Path, err)
			r.Close()
			failCount++
			continue
		}
		r.Close()
		if hex.EncodeToString(hasher.Sum(nil)) != e.SHA256 {
			fmt.Printf("  ✗ %s: SHA-256 不一致\n", e.Path)
			failCount++
			continue
		}
		okCount++
	}
	fmt.Printf("校验完成：%d 通过，%d 失败\n", okCount, failCount)
	if failCount > 0 {
		return fmt.Errorf("%d 个文件校验失败", failCount)
	}
	return nil
}

// mimeForExt 扩展名 → MIME：内置表优先（视频/音频/字幕类型跨平台一致——
// 部分平台系统表对 .ts 等返回错误类型），再回退系统表
func mimeForExt(ext string) string {
	if ct := builtinMIME[strings.ToLower(ext)]; ct != "" {
		return ct
	}
	return mime.TypeByExtension(ext)
}

var builtinMIME = map[string]string{
	".mkv":  "video/x-matroska",
	".mp4":  "video/mp4",
	".m4v":  "video/x-m4v",
	".mov":  "video/quicktime",
	".ts":   "video/mp2t",
	".m2ts": "video/mp2t",
	".mts":  "video/mp2t",
	".avi":  "video/x-msvideo",
	".wmv":  "video/x-ms-wmv",
	".flv":  "video/x-flv",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".flac": "audio/flac",
	".aac":  "audio/aac",
	".wav":  "audio/wav",
	".m4a":  "audio/mp4",
	".srt":  "application/x-subrip",
	".ass":  "text/x-ssa",
	".ssa":  "text/x-ssa",
	".iso":  "application/x-iso9660-image",
	".rar":  "application/vnd.rar",
	".7z":   "application/x-7z-compressed",
	".zip":  "application/zip",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".pdf":  "application/pdf",
	".txt":  "text/plain",
}

func loadManifestFile(inDir string, key []byte) (*vault.Manifest, error) {
	mf, err := os.Open(filepath.Join(inDir, "_mc", "manifest.mcf"))
	if err != nil {
		return nil, fmt.Errorf("manifest 不存在（vault 根目录下应有 _mc/manifest.mcf）: %w", err)
	}
	defer mf.Close()
	return vault.LoadManifest(mf, vault.ManifestKey(key))
}

func cmdBench(args []string) error {
	fset := flag.NewFlagSet("bench", flag.ExitOnError)
	sizeMB := fset.Int("size", 64, "基准数据量（MB）")
	fset.Parse(args)

	key, _ := vault.NewMasterKey()
	nonce := make([]byte, 12)
	plain := make([]byte, vault.DefaultChunkSize)
	for i := range plain {
		plain[i] = byte(i)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	start := time.Now()
	ct := make([]byte, 0, vault.DefaultChunkSize+vault.TagSize)
	for i := 0; i < *sizeMB; i++ {
		ct = gcm.Seal(ct[:0], nonce, plain, nil)
	}
	sealMBps := float64(*sizeMB) / time.Since(start).Seconds()

	start = time.Now()
	for i := 0; i < *sizeMB; i++ {
		if _, err := gcm.Open(plain[:0], nonce, ct, nil); err != nil {
			return err
		}
	}
	openMBps := float64(*sizeMB) / time.Since(start).Seconds()

	fmt.Printf("AES-256-GCM（1 MiB 分块，%d MB）\n", *sizeMB)
	fmt.Printf("  加密（Seal）: %.1f MB/s\n", sealMBps)
	fmt.Printf("  解密（Open）: %.1f MB/s\n", openMBps)
	return nil
}
