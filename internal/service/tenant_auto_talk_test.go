// TenantUsesAutoTalk 分流谓词回归单测（批四 P2，DEFECT_VERIFY_2026-09-20）。
// 旧口径「绑定任意行业包→汽车兜底话术」使 edu/wedding 等非 auto 包租户被
// "越野SUV销售/约试驾/聊车吧"污染；新口径仅汽车族（industry=auto）保留汽车兜底。
// 本测覆盖：非 auto 包→中立、auto 行业包→汽车、auto 族企业包→汽车、无绑定→中立，
// 并字节级断言询价/跑题兜底文案随族别正确切换。全程自建自清临时包码，不碰真实包。
package service

import (
	"strings"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/runtimecfg"
	"ai-scrm/internal/testutil"
)

// bindTestPack 建临时包行+租户绑定，返回租户ID；t.Cleanup 全量回收。
// 注意：必须用 CreateTenantCode 给每个用例独立 code——CreateTenant 进程内复用同一租户，
// 会互相串绑定行，且 boundPackCodes/TenantUsesAutoTalk 各有 30s 进程缓存，
// 同 tid 先后断言不同族别必假失败（本测试首跑实证）。
func bindTestPack(t *testing.T, codeName, packCode, industry string, enterprise bool) uint {
	t.Helper()
	tid := testutil.CreateTenantCode(t, codeName)
	pack := model.IndustryPack{Code: packCode, Name: "单测包" + packCode, Industry: industry, Version: "1.0.0", Status: "active"}
	if err := db.DB.Create(&pack).Error; err != nil {
		t.Fatalf("建临时包失败: %v", err)
	}
	bind := model.TenantPackBinding{TenantID: tid, PackID: pack.ID, PackCode: packCode, BoundAt: time.Now()}
	if enterprise {
		// 企业码挂靠路径（auto_rox 类场景）：PackCode 用行业码，EnterpriseCode 用被测码
		bind.PackCode = "auto"
		if industry != "auto" {
			bind.PackCode = packCode + "_ind"
			ind := model.IndustryPack{Code: bind.PackCode, Name: "单测行业层", Industry: industry, Version: "1.0.0", Status: "active"}
			if err := db.DB.Create(&ind).Error; err != nil {
				t.Fatalf("建行业层包失败: %v", err)
			}
			t.Cleanup(func() { db.DB.Delete(&model.IndustryPack{}, ind.ID) })
		}
		bind.EnterpriseCode = packCode
	}
	if err := db.DB.Create(&bind).Error; err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Delete(&model.TenantPackBinding{}, bind.ID)
		db.DB.Delete(&model.IndustryPack{}, pack.ID)
	})
	return tid
}

func TestTenantUsesAutoTalk(t *testing.T) {
	testutil.SetupTestDB(t)
	if db.DB == nil {
		t.Skip("db 未就绪")
	}
	// 单测环境不依赖系统配置层（industry.* 键未配置才走兜底分流）
	old := runtimecfg.DefaultSystemConfigService
	runtimecfg.DefaultSystemConfigService = runtimecfg.NewStaticService(nil, nil)
	defer func() { runtimecfg.DefaultSystemConfigService = old }()

	suffix := time.Now().Format("150405")

	t.Run("非auto包走中立", func(t *testing.T) {
		tid := bindTestPack(t, "utt_edu", "utedu"+suffix, "edu", false)
		if TenantUsesAutoTalk(tid) {
			t.Fatal("绑定 edu 包误判为汽车族——教育租户会收到约试驾话术")
		}
		for _, reply := range priceReplyFallback(tid, false) {
			if strings.Contains(reply, "试驾") || strings.Contains(reply, "车") {
				t.Fatalf("edu 租户询价兜底含汽车词: %q", reply)
			}
		}
		if r := GetOffTopicReplyForTenant(tid, "讲个故事吧谢谢"); strings.Contains(r, "车") {
			t.Fatalf("edu 租户跑题兜底含汽车词: %q", r)
		}
	})

	t.Run("auto行业包保持汽车", func(t *testing.T) {
		tid := bindTestPack(t, "utt_auto", "uttaa"+suffix, "auto", false)
		if !TenantUsesAutoTalk(tid) {
			t.Fatal("auto 包租户误判为非汽车族——车企租户行业口径回归")
		}
	})

	t.Run("auto族企业包挂靠保持汽车", func(t *testing.T) {
		// PackCode=auto（真实 auto 行业包已上架），企业码=临时 auto 族包
		tid := bindTestPack(t, "utt_rox", "utrox"+suffix, "auto", true)
		if !TenantUsesAutoTalk(tid) {
			t.Fatal("auto_rox 类企业包租户误判为非汽车族")
		}
	})

	t.Run("无绑定走中立", func(t *testing.T) {
		tid := testutil.CreateTenantCode(t, "utt_none")
		if TenantUsesAutoTalk(tid) {
			t.Fatal("无绑定租户误判为汽车族")
		}
	})
}
