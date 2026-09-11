package configcenter

import (
	"testing"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestIsConfigStorageValid P1-38：升级值存储形态判定
// 合法：JSON(对象/数组/字符串字面量/数字/布尔)与裸字符串（"on"/"web"/"10,30"）
// 非法：空、截断 JSON、花括号包非 JSON
func TestIsConfigStorageValid(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"json-object", `{"a":1}`, true},
		{"json-array", `[1,2]`, true},
		{"json-number", `10`, true},
		{"json-bool", `true`, true},
		{"json-string-literal", `"ab"`, true},
		{"plain-on", "on", true},
		{"plain-web", "web", true},
		{"plain-interval", "[10,30]", true}, // json.Valid 也接受
		{"empty", "", false},
		{"truncated-json", `{"a":`, false},
		{"brace-raw", `{on}`, false},
		{"dangling-bracket", `[on`, false},
	}
	for _, c := range cases {
		if got := isConfigStorageValid(c.val); got != c.want {
			t.Errorf("%s: isConfigStorageValid(%q)=%v want %v", c.name, c.val, got, c.want)
		}
	}
}

// TestUpgradePlainString P1-38：Upgrade 不再因 json.Valid 丢弃裸字符串配置
func TestUpgradePlainString(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenantCode(t, "cfgup")
	defer testutil.CleanupTenant(t, tid)

	// 系统默认层预置一把 string 类型键
	def := model.SystemConfig{
		TenantID: 0, Category: "reply_speed", Key: "cfg_test_plain_string",
		Value: "10", ValueType: "string", DefaultValue: "10",
	}
	if err := db.DB.Create(&def).Error; err != nil {
		t.Fatalf("seed 系统默认失败: %v", err)
	}
	defer db.DB.Unscoped().Delete(&model.SystemConfig{}, def.ID)

	n, err := Upgrade(tid, map[string]string{"cfg_test_plain_string": "web"})
	if err != nil {
		t.Fatalf("Upgrade 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("Upgrade 应生效 1 项，实得 %d", n)
	}
	var row model.SystemConfig
	if err := db.DB.Where("tenant_id = ? AND \"key\" = ?", tid, "cfg_test_plain_string").First(&row).Error; err != nil {
		t.Fatalf("租户覆盖行未写入: %v", err)
	}
	if row.Value != "web" || row.ValueType != "string" {
		t.Fatalf("覆盖值不符: value=%s type=%s", row.Value, row.ValueType)
	}
}