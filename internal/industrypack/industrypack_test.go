// §八-7 零测试包最小单测（2026-09-18）：industrypack 的行业包资产防护四道闸——
//  1. ValidCode 白名单（P1-39）：code 进物化前缀 pk_{code}_ 与解绑 LIKE 'prefix%'，
//     含 %/-/斜杠/大写就能劫持查询或路径穿越，必须一律拒；
//  2. IDPrefix 含租户域：跨租户绑同一包时 templates.id 全局主键不撞车，且前缀互不包含
//     （否则 LIKE 'prefix%' 解绑会误删他包产物）；
//  3. Seal↔OpenSeal / Build↔Open 回环：签名 + 内容哈希 + 信封解密三道关；
//     篡改长度头/截尾/错私钥必须报错且不 panic，密文里不得出现明文（=IP 保护生效）；
//  4. untargz 路径穿越：包体里 `../evil`、绝对路径、内嵌 `../` 条目必拒。
//
// 密钥全部内存生成（RSA-2048，包级 init 只生成一次），不落盘、不依赖 keys 目录。
package industrypack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：本包若有 DB 用例被 testutil 跳过，收尾把跳过条数打到 stderr（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// ---- 内存密钥（RSA-2048 生成开销大，整包共享两份） ----

var (
	packKey1 *rsa.PrivateKey // 平台侧密钥对（正常打包/开包）
	packKey2 *rsa.PrivateKey // 仿冒侧密钥对（错误私钥/未授权签名）
)

func init() {
	// 生成失败直接 panic：本包全部用例都依赖密钥，早失败比逐个 t.Fatal 更清楚
	var err error
	if packKey1, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic("生成平台测试密钥失败: " + err.Error())
	}
	if packKey2, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic("生成仿冒测试密钥失败: " + err.Error())
	}
}

func keys1() *Keys { return &Keys{Private: packKey1, Public: &packKey1.PublicKey} }
func keys2() *Keys { return &Keys{Private: packKey2, Public: &packKey2.PublicKey} }

// TestValidCodeRejectsInjection 包 code 白名单：非法形态一律拒（方向不可反）
func TestValidCodeRejectsInjection(t *testing.T) {
	bad := []string{
		"",                      // 空
		"a",                     // 长度 <2
		strings.Repeat("a", 33), // 长度 >32
		"Auto",                  // 大写
		"auto-rox",              // 连字符不在白名单
		"auto/rox",              // 路径分隔
		"../etc",                // 路径穿越
		"pk%",                   // LIKE 通配劫持
		"pk_%",                  // 同上（下划线本身合法，% 不合法）
		"auto rox",              // 空格
		"auto.rox",              // 点
		"auto行业",                // 非 ASCII
		"auto\n",                // 控制字符
	}
	for _, code := range bad {
		if ValidCode(code) {
			t.Errorf("ValidCode(%q) 应为 false（注入/穿越面）", code)
		}
	}
	good := []string{
		"au", "auto", "auto_rox", "auto_rox_sales", "b2b", "edu_01",
		strings.Repeat("a", 32), // 边界：正好 32
	}
	for _, code := range good {
		if !ValidCode(code) {
			t.Errorf("ValidCode(%q) 应为 true（合法包码被误杀）", code)
		}
	}
}

// TestIDPrefixTenantScoped 物化前缀含租户域：跨租户同包不撞全局主键
func TestIDPrefixTenantScoped(t *testing.T) {
	a := IDPrefix("auto_rox", 11)
	b := IDPrefix("auto_rox", 12)
	if a != "pk_auto_rox_t11_" {
		t.Errorf("IDPrefix 格式异常: %q want pk_auto_rox_t11_", a)
	}
	if a == b {
		t.Fatalf("跨租户同 code 前缀必须不同: %q == %q", a, b)
	}
	// 前缀互不为前缀，否则解绑 LIKE 'prefix%' 会误删他包/他租户产物
	if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
		t.Errorf("不同租户前缀不应互相前缀包含: %q %q", a, b)
	}
	if strings.HasPrefix(IDPrefix("auto", 1), IDPrefix("auto_rox", 1)) {
		t.Errorf("同租户不同包前缀不应互相前缀包含")
	}
	if IDPrefix("auto", 0) != "pk_auto_t0_" {
		t.Errorf("tenantID=0（系统层）前缀格式异常: %q", IDPrefix("auto", 0))
	}
}

// TestParseScriptsAndProductKB 八件套解析：缺文件报错含文件名，坏 JSON 报错不 panic
func TestParseScriptsAndProductKB(t *testing.T) {
	// 1) 空文件表：两个解析器都必须点名缺哪个文件
	empty := &PackContent{Manifest: Manifest{Code: "auto"}}
	if _, err := empty.ParseScripts(); err == nil || !strings.Contains(err.Error(), FileScripts) {
		t.Errorf("缺 scripts.json 应报错且含文件名，实际 %v", err)
	}
	if _, err := empty.ParseProductKB(); err == nil || !strings.Contains(err.Error(), FileProductKB) {
		t.Errorf("缺 product_kb.json 应报错且含文件名，实际 %v", err)
	}

	// 2) 坏 JSON：报错且错误串指出是哪个文件
	broken := &PackContent{Files: map[string][]byte{
		FileScripts:   []byte("{不是数组}"),
		FileProductKB: []byte(`{"features": [`),
	}}
	if _, err := broken.ParseScripts(); err == nil || !strings.Contains(err.Error(), FileScripts) {
		t.Errorf("坏 scripts.json 应报错并指向该文件，实际 %v", err)
	}
	if _, err := broken.ParseProductKB(); err == nil || !strings.Contains(err.Error(), FileProductKB) {
		t.Errorf("坏 product_kb.json 应报错并指向该文件，实际 %v", err)
	}

	// 3) 正常解析：字段映射（物化进 templates/features 的唯一入口）
	ok := &PackContent{Files: map[string][]byte{
		FileScripts:   []byte(`[{"id":"tpl_scarcity_001","anchor_type":5,"name":"稀缺锚","prompt_template":"只剩两台了","trigger_tags":["high_intent"],"priority":10,"status":1}]`),
		FileProductKB: []byte(`{"features":[{"id":"feat_4wd","feature_name":"四驱","category":"性能","params":{"扭矩":"700Nm"},"applicable_tags":["越野"],"priority":3,"status":1}],"faqs":[{"q":"续航多少","a":"见参数表"}],"notes":"泛行业化说明"}`),
	}}
	scripts, err := ok.ParseScripts()
	if err != nil {
		t.Fatalf("ParseScripts 失败: %v", err)
	}
	if len(scripts) != 1 {
		t.Fatalf("模板数应为 1，实际 %d", len(scripts))
	}
	s := scripts[0]
	if s.ID != "tpl_scarcity_001" || s.AnchorType != 5 || s.Name != "稀缺锚" || s.Priority != 10 {
		t.Errorf("模板字段映射异常: %+v", s)
	}
	if len(s.TriggerTags) != 1 || s.TriggerTags[0] != "high_intent" {
		t.Errorf("trigger_tags 解析异常: %+v", s.TriggerTags)
	}
	kb, err := ok.ParseProductKB()
	if err != nil {
		t.Fatalf("ParseProductKB 失败: %v", err)
	}
	if len(kb.Features) != 1 || kb.Features[0].ID != "feat_4wd" || kb.Features[0].Params["扭矩"] != "700Nm" {
		t.Errorf("features 解析异常: %+v", kb.Features)
	}
	if len(kb.Faqs) != 1 || kb.Notes == "" {
		t.Errorf("P2 消费字段不应在解析期丢: faqs=%d notes=%q", len(kb.Faqs), kb.Notes)
	}
	// RawFile：未声明的文件返回 ok=false 而非 panic
	if _, ok2 := ok.RawFile(FileMindset); ok2 {
		t.Errorf("RawFile 对不存在文件应返回 false")
	}
	if v, ok2 := ok.RawFile(FileScripts); !ok2 || len(v) == 0 {
		t.Errorf("RawFile 对存在文件应返回内容")
	}
}

// TestSealOpenSealRoundTrip 信封加密封装回环 + 密文不含明文
func TestSealOpenSealRoundTrip(t *testing.T) {
	pub := &packKey1.PublicKey
	plain := []byte(`{"knowledge":"隐藏卖点库内容","token":"sk_secret"}`)
	packet, aesKey, err := Seal(pub, plain)
	if err != nil {
		t.Fatalf("Seal 失败: %v", err)
	}
	if len(aesKey) != 32 {
		t.Errorf("会话密钥应为 AES-256（32B），实际 %d", len(aesKey))
	}
	if bytes.Contains(packet, []byte("隐藏卖点库内容")) {
		t.Errorf("密文里出现明文，信封加密未生效")
	}
	got, err := OpenSeal(packKey1, packet)
	if err != nil {
		t.Fatalf("OpenSeal 失败: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("回环不一致: got=%q want=%q", got, plain)
	}
	// 空明文也要能回环（合法边界）
	p2, _, err := Seal(pub, nil)
	if err != nil {
		t.Fatalf("Seal(nil) 失败: %v", err)
	}
	if g2, err := OpenSeal(packKey1, p2); err != nil || len(g2) != 0 {
		t.Errorf("空明文回环异常: got=%v err=%v", g2, err)
	}
}

// TestSealOpenSealNegatives 篡改/截尾/错私钥：必须报错且不 panic
func TestSealOpenSealNegatives(t *testing.T) {
	pub := &packKey1.PublicKey
	plain := []byte("pack content for negative cases")
	packet, _, err := Seal(pub, plain)
	if err != nil {
		t.Fatalf("Seal 失败: %v", err)
	}

	shortCases := []struct {
		name  string
		input []byte
	}{
		{name: "packet 不足 4 字节（长度头都读不出）", input: packet[:3]},
		{name: "packet 为空", input: nil},
	}
	for _, tc := range shortCases {
		if _, err := OpenSeal(packKey1, tc.input); err == nil {
			t.Errorf("%s：应报错", tc.name)
		}
	}

	// 篡改长度头：blobLen 撑到超过容器 → 命中长度校验
	bad := make([]byte, len(packet))
	copy(bad, packet)
	bad[0], bad[1], bad[2], bad[3] = 0xFF, 0xFF, 0xFF, 0xFF
	if _, err := OpenSeal(packKey1, bad); err == nil {
		t.Errorf("篡改 blobLen 应报错")
	} else if !strings.Contains(err.Error(), "长度") {
		t.Errorf("篡改长度头应命中长度校验，实际 %v", err)
	}

	// 截尾：GCM tag 校验失败
	if _, err := OpenSeal(packKey1, packet[:len(packet)-5]); err == nil {
		t.Errorf("截尾容器应报错")
	}

	// 翻转末字节（落在密文区）：GCM 认证失败
	tampered := make([]byte, len(packet))
	copy(tampered, packet)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := OpenSeal(packKey1, tampered); err == nil {
		t.Errorf("翻转密文字节应被 GCM 拒绝")
	}

	// 错误私钥：会话密钥解不出来
	if _, err := OpenSeal(packKey2, packet); err == nil {
		t.Errorf("错误私钥应报错")
	} else if !strings.Contains(err.Error(), "会话密钥") {
		t.Errorf("错私钥应命中会话密钥解封分支，实际 %v", err)
	}
}

// TestBuildOpenRoundTrip .aipack 容器完整回环：签名+哈希+解密三关都过
func TestBuildOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePackSrc(t, dir)

	m := Manifest{Code: "auto_rox", Name: "极石汽车", Version: "1.1.0",
		Industry: "auto", Publisher: "lexcross", PackLevel: LevelEnterprise, ParentCode: "auto"}
	container, err := Build(dir, m, keys1())
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if string(container[:4]) != Magic {
		t.Errorf("容器魔数应为 %s，实际 %q", Magic, container[:4])
	}
	pc, err := Open(container, keys1())
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if pc.Manifest.Code != "auto_rox" || pc.Manifest.ContentSHA256 == "" {
		t.Errorf("manifest 回填异常: %+v", pc.Manifest)
	}
	if pc.Manifest.FormatVersion != FormatVersion {
		t.Errorf("format_version 应为 %d，实际 %d", FormatVersion, pc.Manifest.FormatVersion)
	}
	for _, name := range []string{FileScripts, FileProductKB, FileParams} {
		if len(pc.Files[name]) == 0 {
			t.Errorf("开包后缺文件 %s", name)
		}
	}
	// 内容哈希只取决于 tar.gz：同内容两次 Build 的哈希一致（CreatedAt/随机会话密钥不参与）
	c2, err := Build(dir, m, keys1())
	if err != nil {
		t.Fatalf("第二次 Build 失败: %v", err)
	}
	pc2, err := Open(c2, keys1())
	if err != nil {
		t.Fatalf("第二次 Open 失败: %v", err)
	}
	if pc.Manifest.ContentSHA256 != pc2.Manifest.ContentSHA256 {
		t.Errorf("同内容哈希漂移: %s vs %s", pc.Manifest.ContentSHA256, pc2.Manifest.ContentSHA256)
	}
}

// TestOpenRejectsTamperedAndForeign 开包负向：非平台签发/篡改/截尾/密钥不全/缺文件/非法 code
func TestOpenRejectsTamperedAndForeign(t *testing.T) {
	dir := t.TempDir()
	writePackSrc(t, dir)
	m := Manifest{Code: "auto", Name: "汽车行业包", Version: "1.0.0", PackLevel: LevelIndustry}

	container, err := Build(dir, m, keys1())
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	// 1) 换验签公钥（仿冒平台）→ 签名验证失败
	if _, err := Open(container, keys2()); err == nil {
		t.Errorf("他方密钥应验签失败")
	} else if !strings.Contains(err.Error(), "签名") {
		t.Errorf("应命中验签分支，实际 %v", err)
	}
	// 2) 篡改 manifest 区段一个字节 → 验签失败（签名覆盖 manifest）
	tampered := make([]byte, len(container))
	copy(tampered, container)
	tampered[4+4+256] ^= 0x02 // 跳过 magic(4)+sigLen(4)+签名(256B，RSA-2048)，落在 manifest 首字节
	if _, err := Open(tampered, keys1()); err == nil {
		t.Errorf("篡改 manifest 应被拒")
	}
	// 3) 截掉包体尾部 → 解密/GCM 失败，不 panic
	if _, err := Open(container[:len(container)-20], keys1()); err == nil {
		t.Errorf("截尾容器应被拒")
	}
	// 4) 非 .aipack 输入（magic 不符）
	if _, err := Open([]byte("XXXX................"), keys1()); err == nil {
		t.Errorf("错误魔数应被拒")
	}
	// 4.1) 超短容器（<8 字节）：必须报错而非 slice 越界 panic
	// （残项收口 2026-09-19：原头部判断 data[4:8] 对短输入直接崩溃，PackTab 上传入口可被外部触发）
	for _, n := range []int{0, 1, 4, 5, 7} {
		short := make([]byte, n)
		copy(short, Magic)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%d 字节容器 panic 未修复: %v", n, r)
				}
			}()
			if _, err := Open(short, keys1()); err == nil {
				t.Errorf("%d 字节容器应报错", n)
			}
		}()
	}
	// 5) 密钥不全：Build/Open 都要求完整密钥对
	if _, err := Build(dir, m, &Keys{Public: &packKey1.PublicKey}); err == nil {
		t.Errorf("Build 缺私钥应报错")
	}
	if _, err := Open(container, &Keys{Private: packKey1}); err == nil {
		t.Errorf("Open 缺公钥应报错（无法验签）")
	}
	if _, err := LoadKeys(nil, nil); err == nil {
		t.Errorf("LoadKeys 不接受空密钥")
	}
	// 6) 源目录缺必需文件 → Build 直接拒并点名
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, FileScripts), []byte("[]"), 0o600); err != nil {
		t.Fatalf("写测试源文件失败: %v", err)
	}
	if _, err := Build(badDir, m, keys1()); err == nil || !strings.Contains(err.Error(), FileProductKB) {
		t.Errorf("缺 product_kb.json 应拒打包并点名文件，实际 %v", err)
	}
	// 7) 空目录：Build 拒绝（无内容可打包）
	if _, err := Build(t.TempDir(), m, keys1()); err == nil {
		t.Errorf("空源目录应报错")
	}
	// 8) manifest.code 非法 → Open 侧白名单拦截
	//    （Build 不校验 code 是既有行为，钉住"消费侧必校验"这道闸）
	illegal := Manifest{Code: "bad%code", Name: "x", Version: "1.0.0"}
	built, err := Build(dir, illegal, keys1())
	if err != nil {
		t.Fatalf("Build(非法 code) 意外失败: %v", err)
	}
	if _, err := Open(built, keys1()); err == nil || !strings.Contains(err.Error(), "code") {
		t.Errorf("Open 应拒绝非法 manifest.code，实际 %v", err)
	}
	// 9) manifest 必填项：code 缺失直接拒
	if _, err := Build(dir, Manifest{Name: "无码包"}, keys1()); err == nil {
		t.Errorf("缺 code 应拒打包")
	}
}

// TestUntargzRejectsPathTraversal 包体条目路径穿越必拒（含绝对路径与内嵌 ../）
func TestUntargzRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{
		"../evil",            // 直接向上逃逸
		"a/../../evil",       // 内嵌向上逃逸
		"/etc/passwd",        // 绝对路径
		"./../evil",          // 归一化后逃逸
		"sub/../../out.json", // 深层逃逸
	} {
		data := tarGzWithEntry(t, name, []byte("x"))
		if _, err := untargz(data); err == nil {
			t.Errorf("untargz 应拒绝条目 %q（路径穿越面）", name)
		} else if !strings.Contains(err.Error(), "非法路径") {
			t.Errorf("untargz(%q) 应命中路径校验，实际 %v", name, err)
		}
	}
	// 合法条目正常还原（含子目录）
	ok := tarGzWithEntries(t, [][2]string{
		{"scripts.json", "[]"},
		{"nested/params.json", "{}"},
		{FileProductKB, `{"features":[]}`},
	})
	files, err := untargz(ok)
	if err != nil {
		t.Fatalf("合法 tar.gz 不应被拒: %v", err)
	}
	if len(files) != 3 || string(files["nested/params.json"]) != "{}" {
		t.Errorf("合法条目还原异常: %d 个文件 keys=%v", len(files), mapKeys(files))
	}
	// 非 gzip / 空输入：报错不 panic
	if _, err := untargz([]byte("这不是 gzip")); err == nil {
		t.Errorf("非 gzip 输入应报错")
	}
	if _, err := untargz(nil); err == nil {
		t.Errorf("空输入应报错")
	}
}

// TestValidLevel 层级枚举校验（三级树挂树的入口）
func TestValidLevel(t *testing.T) {
	for _, l := range []string{LevelIndustry, LevelEnterprise, LevelDepartment} {
		if !ValidLevel(l) {
			t.Errorf("合法层级 %q 被拒", l)
		}
	}
	for _, l := range []string{"", "country", "INDUSTRY", "team"} {
		if ValidLevel(l) {
			t.Errorf("非法层级 %q 被放行", l)
		}
	}
}

// ---- 测试辅助 ----

// writePackSrc 写最小可打包源目录（八件套中的必需两件 + 一件可选）
func writePackSrc(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		FileScripts:   `[{"id":"tpl_1","anchor_type":1,"name":"同类锚","prompt_template":"你好","status":1}]`,
		FileProductKB: `{"features":[{"id":"f1","feature_name":"卖点1","category":"性能","status":1}],"notes":"n"}`,
		FileParams:    `{"merge_window_sec":25}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("写源文件 %s 失败: %v", name, err)
		}
	}
}

// tarGzWithEntry 构造只含单个指定名字条目的 tar.gz
func tarGzWithEntry(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	return tarGzWithEntries(t, [][2]string{{name, string(content)}})
}

// tarGzWithEntries 按给定顺序写 tar 条目（切片保序，map 遍历无序不可用）
func tarGzWithEntries(t *testing.T, pairs [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, p := range pairs {
		hdr := &tar.Header{Name: p[0], Size: int64(len(p[1])), Mode: 0o644}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写 tar 头失败: %v", err)
		}
		if _, err := tw.Write([]byte(p[1])); err != nil {
			t.Fatalf("写 tar 内容失败: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gz: %v", err)
	}
	return buf.Bytes()
}

// mapKeys 取文件表的键（失败消息里给排查线索）
func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
