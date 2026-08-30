// Package industrypack 行业包打包/加密/开包/物化：templates/features 按 pk_{code}_ 前缀写入租户私有层。
package industrypack

// ============================================================
// 行业包 Build / Open（.aipack 打包与开包）
// Build(源目录) → tar.gz → SHA256 写入 manifest → 私钥签名 manifest
//   → 公钥封装随机AES key → 组装容器字节
// Open(容器字节) → 验签 manifest → 解封解密 → 校验内容哈希 → 还原八件套
// ============================================================

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// keysRef 密钥引用（PEM 字节）
type Keys struct {
	Private *rsa.PrivateKey // 签名 + 解封
	Public  *rsa.PublicKey  // 验签 + 封装
}

// LoadKeys 从 PEM 字节装配
func LoadKeys(privPEM, pubPEM []byte) (*Keys, error) {
	k := &Keys{}
	var err error
	if len(privPEM) > 0 {
		if k.Private, err = LoadPrivateKey(privPEM); err != nil {
			return nil, fmt.Errorf("私钥: %w", err)
		}
	}
	if len(pubPEM) > 0 {
		if k.Public, err = LoadPublicKey(pubPEM); err != nil {
			return nil, fmt.Errorf("公钥: %w", err)
		}
	}
	if k.Private == nil && k.Public == nil {
		return nil, errors.New("未提供任何密钥")
	}
	return k, nil
}

// LoadKeysFromPaths 按路径读密钥（env 配置入口）
func LoadKeysFromPaths(privPath, pubPath string) (*Keys, error) {
	var privPEM, pubPEM []byte
	var err error
	if privPath != "" {
		if privPEM, err = os.ReadFile(privPath); err != nil {
			return nil, fmt.Errorf("读私钥 %s: %w", privPath, err)
		}
	}
	if pubPath != "" {
		if pubPEM, err = os.ReadFile(pubPath); err != nil {
			return nil, fmt.Errorf("读公钥 %s: %w", pubPath, err)
		}
	}
	return LoadKeys(privPEM, pubPEM)
}

// Build 将源目录打包为 .aipack 容器字节
// 需要 Private（签名）+ Public（信封）；manifest.Code/Name/Version 必填
func Build(srcDir string, m Manifest, keys *Keys) ([]byte, error) {
	if keys == nil || keys.Private == nil || keys.Public == nil {
		return nil, errors.New("Build 需要完整密钥对（签名私钥+封装公钥）")
	}
	if m.Code == "" || m.Version == "" {
		return nil, errors.New("manifest.code/version 必填")
	}
	targz, files, err := targzDir(srcDir)
	if err != nil {
		return nil, err
	}
	// 内容完整性自检：八件套中声明必含的文件必须存在（缺失即拒绝打包）
	for _, required := range []string{FileScripts, FileProductKB} {
		if _, ok := files[required]; !ok {
			return nil, fmt.Errorf("源目录缺少必需文件 %s", required)
		}
	}
	sum := sha256Hex(targz)

	m.FormatVersion = FormatVersion
	m.CreatedAt = timeNow()
	m.ContentSHA256 = sum
	manJSON, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	sig, err := SignSHA256(keys.Private, manJSON)
	if err != nil {
		return nil, err
	}
	packet, _, err := Seal(keys.Public, targz)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, 4+4+len(sig)+4+len(manJSON)+len(packet))
	out = append(out, Magic...)
	out = appendU32(out, uint32(len(sig)))
	out = append(out, sig...)
	out = appendU32(out, uint32(len(manJSON)))
	out = append(out, manJSON...)
	out = append(out, packet...)
	return out, nil
}

// Open 开包：验签 → 解密 → 哈希校验 → 还原文件表
// 需要 Public（验签）+ Private（解封）
func Open(data []byte, keys *Keys) (*PackContent, error) {
	if keys == nil || keys.Private == nil || keys.Public == nil {
		return nil, errors.New("Open 需要完整密钥对（验签公钥+解封私钥）")
	}
	if len(data) < 4+len(data[4:8]) || string(data[:4]) != Magic {
		return nil, errors.New("非 .aipack 容器或格式版本不符")
	}
	off := 4
	sigLen := int(readU32(data[off:]))
	off += 4
	if off+sigLen+4 > len(data) {
		return nil, errors.New("容器截断(sig)")
	}
	sig := data[off : off+sigLen]
	off += sigLen
	manLen := int(readU32(data[off:]))
	off += 4
	if off+manLen > len(data) {
		return nil, errors.New("容器截断(manifest)")
	}
	manJSON := data[off : off+manLen]
	off += manLen

	// 1. 来源与完整性第一关：验签 manifest
	if err := VerifySHA256(keys.Public, manJSON, sig); err != nil {
		return nil, fmt.Errorf("签名验证失败（非平台签发或已被篡改）: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(manJSON, &m); err != nil {
		return nil, fmt.Errorf("manifest 解析失败: %w", err)
	}
	if m.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("包格式版本不支持: %d", m.FormatVersion)
	}

	// 2. 解密封装内容
	targz, err := OpenSeal(keys.Private, data[off:])
	if err != nil {
		return nil, err
	}
	// 3. 第二关：内容哈希比对
	if sha256Hex(targz) != m.ContentSHA256 {
		return nil, errors.New("内容哈希不匹配，包体损坏")
	}
	files, err := untargz(targz)
	if err != nil {
		return nil, err
	}
	return &PackContent{Manifest: m, Files: files}, nil
}

// ParseScripts 解析 scripts.json
func (pc *PackContent) ParseScripts() ([]ScriptTemplate, error) {
	raw, ok := pc.Files[FileScripts]
	if !ok {
		return nil, errors.New("缺少 scripts.json")
	}
	var out []ScriptTemplate
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("scripts.json 解析失败: %w", err)
	}
	return out, nil
}

// ParseProductKB 解析 product_kb.json
func (pc *PackContent) ParseProductKB() (*ProductKB, error) {
	raw, ok := pc.Files[FileProductKB]
	if !ok {
		return nil, errors.New("缺少 product_kb.json")
	}
	var out ProductKB
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("product_kb.json 解析失败: %w", err)
	}
	return &out, nil
}

// RawFile 取任意原始文件内容（params/mindset/prompts 等 JSON 直存场景）
func (pc *PackContent) RawFile(name string) ([]byte, bool) {
	v, ok := pc.Files[name]
	return v, ok
}

// ---- 内部工具 ----

// targzDir 目录→内存 tar.gz；返回 (tar.gz bytes, 相对路径→内容)
func targzDir(srcDir string) ([]byte, map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
			return nil // 跳过隐藏文件
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, errors.New("源目录为空")
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	// 排序保证确定性输出（同内容同哈希）
	sortStrings(names)
	for _, n := range names {
		hdr := &tar.Header{Name: n, Size: int64(len(files[n])), Mode: 0o644, ModTime: timeZero()}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, nil, err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return nil, nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), files, nil
}

// untargz 内存 tar.gz → 文件表（拒绝路径穿越）
func untargz(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip 解压失败: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(hdr.Name)
		if strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || filepath.IsAbs(name) {
			return nil, fmt.Errorf("非法路径: %s", name)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(tr, 10<<20)) // 单文件上限 10MB
		if err != nil {
			return nil, err
		}
		files[name] = content
	}
	return files, nil
}
