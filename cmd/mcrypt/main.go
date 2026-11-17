// mcrypt —— mini-cloud Vault 加密工具（Vault Phase 0）。
//
// 用法：
//
//	mcrypt keygen  [-key vault.key]                         生成 32B master key（0600）
//	mcrypt encrypt -key vault.key -in 明文目录 -out vault根  加密整个目录树（manifest 最后写入）
//	mcrypt decrypt -key vault.key -in vault根 -out 明文目录  还原明文（校验 SHA-256）
//	mcrypt verify  -key vault.key -in vault根               校验对象与 manifest 的一致性
//	mcrypt bench                                            AES-256-GCM 吞吐基准（AX6 验收用）
//
// 格式规范见 docs/vault-feasibility.md §7.3；不变量：每次加密生成新 fileID（禁止复用）。
package main

import (
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

  mcrypt keygen  [-key vault.key]
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
	fset.Parse(args)

	key, err := vault.NewMasterKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*keyPath, key, 0600); err != nil {
		return fmt.Errorf("写密钥失败: %w", err)
	}
	// P2：WriteFile 的 perm 只对新建文件生效，存量文件显式收紧
	if err := os.Chmod(*keyPath, 0600); err != nil {
		return fmt.Errorf("设置密钥权限失败: %w", err)
	}
	fmt.Printf("已生成 master key：%s（32 字节，0600）\n", *keyPath)
	fmt.Println("警告：master key 永不传上网盘、必须离线备份；丢失 = 数据不可恢复。")
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
			M: info.ModTime().Unix(), T: mime.TypeByExtension(filepath.Ext(rel)),
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
			return fmt.Errorf("加密 %s 失败: %w", virtual, err)
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
	mf, err := os.Create(filepath.Join(manifestDir, "manifest.mcf"))
	if err != nil {
		return err
	}
	if err := vault.SaveManifest(mf, vault.ManifestKey(key), m); err != nil {
		mf.Close()
		return err
	}
	if err := mf.Close(); err != nil {
		return err
	}
	fmt.Printf("完成：%d 个文件（%d B）加密到 %s，manifest 已写入\n", fileCount, totalSize, *outDir)
	return nil
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
		outPath := filepath.Join(*outDir, filepath.FromSlash(strings.TrimPrefix(e.Path, "/")))
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
	// 重建空目录（manifest Dirs 记录）
	for _, d := range m.Dirs {
		dirPath := filepath.Join(*outDir, filepath.FromSlash(strings.TrimPrefix(d, "/")))
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
		headerSHA, _ := hex.DecodeString(e.HeaderSHA256)
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
