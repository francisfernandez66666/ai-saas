// ============================================================
// 行业包打包工具 CLI（P1，2026-08-25）
//
// 用法：
//   go run ./cmd/pack keygen  -dir keys                          生成密钥对（首次）
//   go run ./cmd/pack build   -src packs-src/auto -out data/packs/auto-1.0.0.aipack \
//                             -keys keys -code auto -name "汽车行业包" \
//                             -version 1.0.0 [-industry auto] [-publisher lexicorn]
//   go run ./cmd/pack inspect -f xxx.aipack -keys keys           校验并查看包内容摘要
//
// 密钥说明：
//   pack_priv.pem 平台侧保管（签名+可解包）——绝不入 git/不分发
//   pack_pub.pem  随引擎部署物分发（验签）；信封加密用公钥封装会话密钥，
//                 引擎侧私钥解封——即部署物需同时带 pub(验签)+priv(解密)。
//                 单对密钥两用属本期务实取舍（防抽取/防篡改已达成）。
// ============================================================
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"ai-scrm/internal/industrypack"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygenCmd(os.Args[2:])
	case "build":
		buildCmd(os.Args[2:])
	case "inspect":
		inspectCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Println(`行业包打包工具
  keygen  -dir keys                                    生成 RSA 密钥对到指定目录
  build   -src SRC -out OUT.aipack -keys KEYS_DIR      打包源目录为 .aipack
          -code auto -name 名称 -version 1.0.0 [-industry x] [-publisher y]
  inspect -f FILE.aipack -keys KEYS_DIR                校验并列出包内容`)
	os.Exit(1)
}

func keygenCmd(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dir := fs.String("dir", "keys", "密钥输出目录")
	bits := fs.Int("bits", 2048, "RSA 位数")
	_ = fs.Parse(args)
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		die("创建目录失败: %v", err)
	}
	privPEM, pubPEM, err := industrypack.GenerateKeyPair(*bits)
	if err != nil {
		die("生成密钥失败: %v", err)
	}
	privPath := filepath.Join(*dir, "pack_priv.pem")
	pubPath := filepath.Join(*dir, "pack_pub.pem")
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		die(err.Error())
	}
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		die(err.Error())
	}
	fmt.Printf("✓ 私钥: %s （平台侧保管，勿入 git/勿分发）\n✓ 公钥: %s\n", privPath, pubPath)
}

func buildCmd(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	src := fs.String("src", "", "源目录（含八件套 json）")
	out := fs.String("out", "", "输出 .aipack 路径")
	keysDir := fs.String("keys", "keys", "密钥目录")
	code := fs.String("code", "", "包唯一码")
	name := fs.String("name", "", "显示名")
	version := fs.String("version", "1.0.0", "版本号")
	industry := fs.String("industry", "", "行业")
	publisher := fs.String("publisher", "", "发布方")
	level := fs.String("level", "", "包层级: industry/enterprise/department（三级树：行业→企业→部门）")
	parent := fs.String("parent", "", "上级包 code（行业包留空；企业包=行业code；部门包=企业code）")
	_ = fs.Parse(args)
	if *src == "" || *out == "" || *code == "" || *name == "" {
		die("build 需要 -src -out -keys -code -name")
	}
	if !industrypack.ValidLevel(*level) {
		die("build 需要 -level industry|enterprise|department（三级树结构）")
	}
	if *level == industrypack.LevelEnterprise && *parent == "" {
		die("企业包必须 -parent 指定所属行业包 code")
	}
	if *level == industrypack.LevelDepartment && *parent == "" {
		die("部门包必须 -parent 指定所属企业包 code")
	}
	keys, err := industrypack.LoadKeysFromPaths(
		filepath.Join(*keysDir, "pack_priv.pem"),
		filepath.Join(*keysDir, "pack_pub.pem"))
	if err != nil {
		die("%v", err)
	}
	m := industrypack.Manifest{
		Code: *code, Name: *name, Version: *version,
		Industry: *industry, Publisher: *publisher,
		PackLevel: *level, ParentCode: *parent,
	}
	data, err := industrypack.Build(*src, m, keys)
	if err != nil {
		die("打包失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		die(err.Error())
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		die(err.Error())
	}
	fmt.Printf("✓ 已生成 %s (%d 字节) code=%s v%s level=%s parent=%s\n",
		*out, len(data), *code, *version, *level, *parent)
}

func inspectCmd(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	file := fs.String("f", "", ".aipack 文件")
	keysDir := fs.String("keys", "keys", "密钥目录")
	_ = fs.Parse(args)
	if *file == "" {
		die("inspect 需要 -f")
	}
	keys, err := industrypack.LoadKeysFromPaths(
		filepath.Join(*keysDir, "pack_priv.pem"),
		filepath.Join(*keysDir, "pack_pub.pem"))
	if err != nil {
		die("%v", err)
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		die(err.Error())
	}
	pc, err := industrypack.Open(raw, keys)
	if err != nil {
		die("开包失败: %v", err)
	}
	mj, _ := json.MarshalIndent(pc.Manifest, "", "  ")
	fmt.Println(string(mj))
	scripts, err1 := pc.ParseScripts()
	kb, err2 := pc.ParseProductKB()
	if err1 == nil {
		fmt.Printf("scripts: %d 条模板\n", len(scripts))
	}
	if err2 == nil {
		fmt.Printf("product_kb: %d 条卖点 / %d 条FAQ\n", len(kb.Features), len(kb.Faqs))
	}
	for name := range pc.Files {
		fmt.Printf("file: %s\n", name)
	}
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "✗ "+f+"\n", a...)
	os.Exit(1)
}
