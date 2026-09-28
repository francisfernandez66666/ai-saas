// 通道凭据密文审计单测（FIX-N，2026-09-28 .env 丢失处置批）
//
// 这批用例的价值全在**轮换密钥那一刀**上：pkg/crypto 的 AES 密钥派生自 JWT_SECRET，
// 所以"改 JWT_SECRET"与"库里的凭据还能用吗"之间没有任何中间态——要么全解得开，要么全作废。
// 旧代码没有一处把这件事说出来：管理台对密文列一律显掩码，解不开时也显 ****，
// 于是"没配凭据"和"凭据作废"在界面上长成同一张脸，唯一的差别是要做的动作完全相反。
// 本文件用真库真加密链把这条分界钉住（sqlite/mock 证不了——判据就是 crypto.Decrypt 的真实行为）。
package channel

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
	"ai-scrm/pkg/crypto"
)

// auditTenant 建一个只属于本用例的测试租户与空通道表位（复用存档用例的清理口径）。
func auditTenant(t *testing.T) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	tid := testutil.CreateTenant(t)
	t.Cleanup(func() {
		db.DB.Where("tenant_id = ?", tid).Unscoped().Delete(&model.Channel{})
		testutil.CleanupTenant(t, tid)
	})
	return tid
}

// mustEncrypt 用**当前** JWT_SECRET 加密一个明文（值现场生成，不落任何日志）。
func mustEncrypt(t *testing.T, plain string) string {
	t.Helper()
	c, err := crypto.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密失败（JWT_SECRET 未配或过短？）: %v", err)
	}
	if !strings.HasPrefix(c, "gcm1:") {
		t.Fatalf("密文前缀异常，应为 gcm1: 实得 %s", c[:4])
	}
	return c
}

// TestAuditCredentialCiphersRotation 正向 + 反证同体：
// ① 用当前密钥录好三列凭据 ⇒ 审计必须报"零作废"（正向，防把健康库判成坏了）；
// ② 轮换 JWT_SECRET（t.Setenv，用例结束自动还原）⇒ 同一批密文必须被逐列点名作废，
//
//	且 AffectedChannels 恰等于行数（"几条通道要重录"是运维真正要的数）。
//
// 摘掉审计里的 crypto.Decrypt 判据 ⇒ ②红；摘掉 gcm1 前缀过滤 ⇒ 历史明文行被算动作废（见下条用例）。
func TestAuditCredentialCiphersRotation(t *testing.T) {
	tid := auditTenant(t)
	origSecret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if len(origSecret) < 16 {
		t.Skip("JWT_SECRET 未配置，加密链不可用")
	}
	ch := model.Channel{
		TenantID:     tid,
		Type:         model.ChannelTypeWecomApp,
		Name:         "凭据审计用例",
		Status:       model.ChannelStatusActive,
		SecretCipher: mustEncrypt(t, "secret-"+t.Name()),
		TokenCipher:  mustEncrypt(t, "token-"+t.Name()),
		AesKeyCipher: mustEncrypt(t, "aeskey-"+t.Name()),
	}
	if err := db.DB.Create(&ch).Error; err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Channel{}, ch.ID) })

	// ① 正向：密钥未动，本用例这三列必须被数到且**不新增作废**。
	// 口径必须是**增量**而不是绝对值（2026-09-28 首跑即撞）：审计读的是全表，
	// 真实库里本来就躺着别的行（本机就有一条 2026-09-12 用旧密钥录的测试通道），
	// 断"Undecryptable==0"等于拿别人的状态判我的用例，与 §四c 那条
	// "数量等式必须先证明这些行是本段链路产生的"是同一个坑。
	base, err := AuditCredentialCiphers(nil)
	if err != nil {
		t.Fatalf("审计取数失败: %v", err)
	}
	if base.WithCipher < 3 {
		t.Fatalf("前置不成立：本用例三列密文没被数到（WithCipher=%d），列清单或前缀判据已漂移", base.WithCipher)
	}
	baseline := base.WithCipher

	// ② 反证：换密钥（≥16 字符且与原名不同），同一批密文必须全部点名
	t.Setenv("JWT_SECRET", origSecret+"_rotated_for_audit")
	sum2, err := AuditCredentialCiphers(nil)
	if err != nil {
		t.Fatalf("轮换后审计取数失败: %v", err)
	}
	if sum2.WithCipher != baseline {
		t.Fatalf("密文列总数漂移：轮换前后应同为 %d，实得 %d", baseline, sum2.WithCipher)
	}
	if gained := sum2.Undecryptable - base.Undecryptable; gained < 3 {
		t.Fatalf("反证失败：轮换 JWT_SECRET 后本用例三列必须新增作废，实得增量 %d（前 %d 后 %d）",
			gained, base.Undecryptable, sum2.Undecryptable)
	}
	if sum2.AffectedChannels <= 0 {
		t.Fatalf("AffectedChannels 必须 ≥1（本用例那条通道），实得 %d", sum2.AffectedChannels)
	}
}

// TestAuditCredentialCiphersIgnoresLegacyPlaintext 历史明文列（无 gcm1: 前缀）不得计为作废：
// pkg/crypto 对非 gcm1 前缀原样返回（兼容轮换前的存量明文），审计若把它算成"坏"，
// 观测位会在从未录入过凭据的老库上长红，运维收到的信号是"重录凭据"而实际没有凭据可录。
func TestAuditCredentialCiphersIgnoresLegacyPlaintext(t *testing.T) {
	tid := auditTenant(t)
	if len(strings.TrimSpace(os.Getenv("JWT_SECRET"))) < 16 {
		t.Skip("JWT_SECRET 未配置，加密链不可用")
	}
	// 先取基线再插数据：审计读的是全表，本机库里躺着历史行（含一条旧密钥录的测试通道），
	// 断绝对值等于拿别人的状态判本用例——与 §四c「等式必须先证明这些行是本段链路产生的」同形态。
	base, err := AuditCredentialCiphers(nil)
	if err != nil {
		t.Fatalf("审计取数失败: %v", err)
	}
	if base.WithCipher == 0 {
		t.Fatalf("前置不成立：审计一列密文都没看到，说明列清单或前缀判据已漂移")
	}
	extra := model.Channel{
		TenantID:     tid,
		Type:         model.ChannelTypeWechatMP,
		Name:         "历史明文用例",
		Status:       model.ChannelStatusActive,
		SecretCipher: "plain-secret-legacy", // 无 gcm1 前缀＝轮换前的存量明文形态
		TokenCipher:  mustEncrypt(t, "token-legacy"),
	}
	// 本用例插入「一列历史明文 + 一列真密文」：审计必须只把真密文计进 WithCipher，
	// 且作废数一动不动。摘掉 gcm1 前缀判据 ⇒ 明文被算成坏，作废 +1，本条红。
	if err := db.DB.Create(&extra).Error; err != nil {
		t.Fatalf("建第二条通道失败: %v", err)
	}
	t.Cleanup(func() { db.DB.Unscoped().Delete(&model.Channel{}, extra.ID) })
	after, err := AuditCredentialCiphers(nil)
	if err != nil {
		t.Fatalf("二次审计取数失败: %v", err)
	}
	if after.WithCipher != base.WithCipher+1 {
		t.Fatalf("历史明文被计进了密文计数：前 %d 后 %d（期望只 +1，即只数那条真密文）",
			base.WithCipher, after.WithCipher)
	}
	if after.Undecryptable != base.Undecryptable {
		t.Fatalf("历史明文被判作废：前 %d 后 %d", base.Undecryptable, after.Undecryptable)
	}
}

// TestCipherColumnsCoverModel 反向锁：model.Channel 里每一个 *_cipher 列都必须在审计清单里。
// 这是本文件最容易漏的一条——新增密文列（比如下一批的支付商户密钥）而没加进 cipherColumns，
// 审计会永远数不到那一列，观测位继续报 ok(全部可解)，而这种"绿"恰恰是作废时没人知道的那种。
func TestCipherColumnsCoverModel(t *testing.T) {
	inList := []string{}
	for _, c := range cipherColumns {
		inList = append(inList, c.Column)
	}
	sort.Strings(inList)
	modelCols := []string{}
	rt := reflect.TypeOf(model.Channel{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		col := f.Name
		if tag := f.Tag.Get("gorm"); tag != "" {
			for _, part := range strings.Split(tag, ";") {
				if strings.HasPrefix(part, "column:") {
					col = strings.TrimPrefix(part, "column:")
				}
			}
		}
		if strings.HasSuffix(col, "_cipher") || strings.HasSuffix(f.Name, "Cipher") {
			modelCols = append(modelCols, col)
		}
	}
	sort.Strings(modelCols)
	if strings.Join(modelCols, ",") != strings.Join(inList, ",") {
		t.Fatalf("model.Channel 的密文列与审计清单不一致：模型=%v 审计=%v（新增列请同步 cipherColumns，否则作废时无人报警）",
			modelCols, inList)
	}
	// 逐列取数器也必须成对在场（漏了 Get 会让清单里有列名而实际不扫那一列）
	for _, c := range cipherColumns {
		if c.Get == nil {
			t.Fatalf("密文列 %s 没有取数器", c.Column)
		}
	}
}

// getenvJWTSecretForAudit 读 JWT_SECRET 原值（判长度用，绝不打印）。
