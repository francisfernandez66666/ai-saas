// §八-7 零测试包最小单测（2026-09-18）：model 是全项目最底层的"不变量来源"，此前零测试。
// 本批只测纯函数/纯方法（无 DB、无网络），钉死四类现状：
//  1. User 角色谓词：RoleSales == RoleUser == "user"，而 IsSales() 判的是字面量 "sales"——
//     存量角色改名后 IsSales 对 "user" 恒 false。**这是隐性不一致，本测试如实固化现状并标注，
//     不改产品代码**（改了要连带核对 middleware/handler 全部调用点，属产品决策）；
//  2. Customer 旅程闸门 HasArrived（价格管控）/CanPromote（促单），以及 T 向量：
//     BuildBaseTVector 必须忽略已存 TVectorJSON（防标签权重反复叠加滚偏），
//     GetTVector 坏 JSON 回落基准值、超长向量截断到 32 维；
//  3. Conversation 会话状态双写（独立列 + StateJSON）与 Attempts=0 的除零保护；
//  4. TagWeightMapping.ApplyToVector 的 0-1 clamp、SystemConfig 各 getter 的垃圾字符串兜底、
//     AnchorTypeName 对 AnchorType* 常量的覆盖、GenerateVisitorKey 的格式与唯一性。
//
// 本包无 DB 用例，但 TestMain 仍走 testutil.RunMain：将来本包新增 DB 用例被跳过时，
// 收尾会在 stderr 打出跳过条数（防"全绿但一个没跑"的静默绿）。
// 注意：这里用**外部测试包 model_test**——testutil 依赖 model，若本文件写 package model 会成环；
// 外部测试包是独立包，允许 import 依赖被测包的第三方/内部包，故不成环。
package model_test

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"testing"

	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口：收尾打印本测试二进制的 DB 跳过计数（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// almostEq 浮点近似比较（T 向量/clamp 结果有除法与加减，不用 ==）
func almostEq(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

// ============================================================
// 1. User 角色谓词
// ============================================================

// TestUserRolePredicatesPinCurrentBehavior 角色谓词现状固化（含 IsSales 的隐性不一致）
func TestUserRolePredicatesPinCurrentBehavior(t *testing.T) {
	// 先钉住角色常量本身的现状
	if model.RoleSales != model.RoleUser {
		t.Errorf("RoleSales 应为 RoleUser 的兼容别名，实际 %q vs %q", model.RoleSales, model.RoleUser)
	}
	if model.RoleSales == "sales" {
		t.Errorf("【现状漂移】RoleSales 别名已随 P1-32 收敛为 %q（不再是字面量 \"sales\"），"+
			"若又变回 \"sales\" 说明角色体系回退，本文件下方谓词表需一并复核", model.RoleUser)
	}
	// 【隐性不一致·现状固化】RoleSales==RoleUser=="user"，但 IsSales() 判的是字面量 "sales"，
	// 因此对别名角色 IsSales() 恒为 false。产品代码未改，这里把现状钉死：
	// 将来若把 IsSales 改成判 RoleSales，本断言会失败，提醒调用点（middleware/handler）必须同步核对。
	if (&model.User{Role: model.RoleSales}).IsSales() {
		t.Errorf("【现状】Role=RoleSales(%q) 时 IsSales() 应为 false，实际为 true：说明 IsSales 已改为判常量，需复核全部调用点", model.RoleSales)
	}

	cases := []struct {
		role        string
		sales       bool
		admin       bool
		superAdmin  bool
		tenantAdmin bool
		readOnly    bool
	}{
		{role: model.RoleSuperAdmin, admin: true, superAdmin: true},
		{role: model.RoleTenantAdmin, admin: true, tenantAdmin: true},
		{role: model.RoleUser}, // 【隐性不一致】新角色名 "user"：IsSales() 返回 false
		{role: "sales", sales: true},
		{role: model.RoleDeptAdmin}, // 部门管理员：IsAdmin() 不含 dept_admin，三个谓词全 false
		{role: model.RoleReadOnly, readOnly: true},
		{role: "admin", admin: true}, // 旧字面量仍被 IsAdmin 兼容
		{role: ""},                   // 空角色：一律 false（fail-closed）
		{role: "SUPER_ADMIN"},        // 大小写敏感：不认
		{role: "root"},               // 未定义角色：不认
	}
	for _, tc := range cases {
		u := &model.User{Role: tc.role}
		if got := u.IsSales(); got != tc.sales {
			t.Errorf("Role=%q IsSales()=%v want %v", tc.role, got, tc.sales)
		}
		if got := u.IsAdmin(); got != tc.admin {
			t.Errorf("Role=%q IsAdmin()=%v want %v", tc.role, got, tc.admin)
		}
		if got := u.IsSuperAdmin(); got != tc.superAdmin {
			t.Errorf("Role=%q IsSuperAdmin()=%v want %v", tc.role, got, tc.superAdmin)
		}
		if got := u.IsTenantAdmin(); got != tc.tenantAdmin {
			t.Errorf("Role=%q IsTenantAdmin()=%v want %v", tc.role, got, tc.tenantAdmin)
		}
		if got := u.IsReadOnly(); got != tc.readOnly {
			t.Errorf("Role=%q IsReadOnly()=%v want %v", tc.role, got, tc.readOnly)
		}
	}
}

// ============================================================
// 2. Customer 旅程闸门 + T 向量
// ============================================================

// TestCustomerHasArrived 价格管控闸门：arrived 及之后阶段可见具体价格
func TestCustomerHasArrived(t *testing.T) {
	cases := []struct {
		stage string
		want  bool
	}{
		{stage: "", want: false}, // 空阶段按起点 ai_connected 处理
		{stage: model.JourneyAIConnected, want: false},
		{stage: model.JourneyHumanConnected, want: false},
		{stage: model.JourneyLeadCaptured, want: false},
		{stage: model.JourneyArrived, want: true},
		{stage: model.JourneyOrdered, want: true},
		{stage: model.JourneyDelivered, want: true},
		{stage: model.JourneyLost, want: false}, // 独立分支 order=-1，不参与正向比较
		{stage: "not_a_stage", want: false},     // 未知阶段：拒绝放行（fail-closed）
		{stage: "ARRIVED", want: false},         // 大小写敏感
	}
	for _, tc := range cases {
		c := &model.Customer{JourneyStage: tc.stage}
		if got := c.HasArrived(); got != tc.want {
			t.Errorf("HasArrived()(stage=%q)=%v want %v", tc.stage, got, tc.want)
		}
	}
}

// TestCustomerCanPromote 促单闸门：仅"已到店 + 已报价"
func TestCustomerCanPromote(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		sub   string
		want  bool
	}{
		{name: "到店+已报价", stage: model.JourneyArrived, sub: model.SubStageQuoted, want: true},
		{name: "到店+刚到店", stage: model.JourneyArrived, sub: model.SubStageEmpty, want: false},
		{name: "到店+已试驾未报价", stage: model.JourneyArrived, sub: model.SubStageTestDrive, want: false},
		{name: "未到店但子状态误留 quoted", stage: model.JourneyLeadCaptured, sub: model.SubStageQuoted, want: false},
		{name: "已下单无需促单", stage: model.JourneyOrdered, sub: model.SubStageQuoted, want: false},
		{name: "已交付", stage: model.JourneyDelivered, sub: model.SubStageQuoted, want: false},
		{name: "已战败", stage: model.JourneyLost, sub: model.SubStageQuoted, want: false},
		{name: "零值客户", stage: "", sub: "", want: false},
	}
	for _, tc := range cases {
		c := &model.Customer{JourneyStage: tc.stage, JourneySubStage: tc.sub}
		if got := c.CanPromote(); got != tc.want {
			t.Errorf("CanPromote()(%s)=%v want %v", tc.name, got, tc.want)
		}
	}
	// 未知子状态不得被当成 quoted（防前端乱传绕过闸门）
	if (&model.Customer{JourneyStage: model.JourneyArrived, JourneySubStage: "quoted "}).CanPromote() {
		t.Errorf("子状态带空格的伪 quoted 不应放行促单")
	}
}

// TestJourneyStageOrderContract 旅程阶段顺序：到店之后严格有序、留资与人工建联同级
func TestJourneyStageOrderContract(t *testing.T) {
	if model.JourneyStageOrder[model.JourneyHumanConnected] != model.JourneyStageOrder[model.JourneyLeadCaptured] {
		t.Errorf("人工建联与已留资应同级（无必然先后）: %d vs %d",
			model.JourneyStageOrder[model.JourneyHumanConnected], model.JourneyStageOrder[model.JourneyLeadCaptured])
	}
	prev := math.MinInt32
	for _, s := range []string{model.JourneyArrived, model.JourneyOrdered, model.JourneyDelivered} {
		o := model.JourneyStageOrder[s]
		if o <= prev {
			t.Errorf("到店后阶段顺序必须递增，%s=%d 未大于 %d", s, o, prev)
		}
		prev = o
	}
	if model.JourneyStageOrder[model.JourneyLost] >= 0 {
		t.Errorf("已战败应为独立分支（order<0），实际 %d", model.JourneyStageOrder[model.JourneyLost])
	}
	for stage, name := range model.JourneyStageNames {
		if name == "" {
			t.Errorf("旅程阶段 %q 缺中文名", stage)
		}
	}
	if got := (&model.Customer{JourneyStage: model.JourneyArrived}).GetJourneyStageName(); got != "已到店" {
		t.Errorf("GetJourneyStageName()=%q want 已到店", got)
	}
	// 未知阶段回落原值（不编造中文名）
	if got := (&model.Customer{JourneyStage: "weird"}).GetJourneyStageName(); got != "weird" {
		t.Errorf("未知阶段名应原样返回，实际 %q", got)
	}
	if !(&model.Customer{JourneyStage: model.JourneyLost}).IsLost() {
		t.Errorf("IsLost() 应识别 lost 阶段")
	}
}

// TestBuildBaseTVectorIgnoresStoredJSON 基准向量只由结构化字段算出，绝不读 TVectorJSON
func TestBuildBaseTVectorIgnoresStoredJSON(t *testing.T) {
	c := &model.Customer{
		IntentScore:      0.8,
		PriceSensitivity: 0.6,
		BrandAwareness:   0.4,
		InterestProduct:  "", // 兴趣产品编码走注入 resolver，单测二进制未注册→0（这里显式留空避免依赖注入顺序）
		OfflineTouch:     2,
		DecisionCycle:    14,
		TrustLevel:       0.7,
		Source:           "微信",
		Gender:           1,
		Age:              30,
		Region:           "华东",
		Career:           "工程师",
		ProductAge:       3.5,
		ResistanceType:   "price",
		CustomerType:     "owner",
	}
	// 故意塞一份"已叠加过权重"的脏 JSON：BuildBaseTVector 必须完全无视它
	c.TVectorJSON = "[0.05,0.05,0.05,9,9,9,9,9,9,9,9,9,9,9,9,9,9,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]"

	base := c.BuildBaseTVector()
	want := map[int]float64{
		0:  0.8, // 意向分
		1:  0.6, // 价格敏感度
		2:  0.4, // 品牌认知度
		3:  0,   // 兴趣产品编码（resolver 未注入 → 未知中性值）
		4:  2,   // 线下接触状态
		5:  14,  // 决策周期
		6:  0.7, // 信任度
		7:  3,   // 来源编码：微信
		8:  1,   // 性别
		9:  2,   // 年龄段：30 岁 → <35 桶
		10: 1,   // 地域编码：华东
		11: 6,   // 职业编码：工程师
		12: 0,   // 用车场景（简化）
		13: 3.5, // 现用产品年限
		14: 1,   // 抗性编码：price
		15: 1,   // 客户身份：owner
		16: 0,   // 地域商圈（未启用）
	}
	for idx, w := range want {
		if !almostEq(base[idx], w) {
			t.Errorf("T[%d]=%v want %v（基准向量维度含义漂移，策略引擎输入会整体错位）", idx, base[idx], w)
		}
	}
	for idx := 17; idx < 32; idx++ {
		if base[idx] != 0 {
			t.Errorf("T[%d] 为预留维，应为 0，实际 %v", idx, base[idx])
		}
	}

	// 编码函数的兜底方向：未知值不得被静默映射成 0（0 是"未知"合法值，未知文本用 99）
	unk := &model.Customer{Source: "地推", Region: "外太空", Career: "网红", ResistanceType: "unknown", Age: -1}
	uv := unk.BuildBaseTVector()
	for idx, name := range map[int]string{7: "来源编码", 10: "地域编码", 11: "职业编码"} {
		if uv[idx] != 99 {
			t.Errorf("%s 对未知文本应为 99，实际 %v", name, uv[idx])
		}
	}
	if uv[14] != 0 || uv[15] != 0 || uv[9] != 0 {
		t.Errorf("抗性/身份/年龄未知应落到合法 0，实际 %v/%v/%v", uv[14], uv[15], uv[9])
	}
}

// TestCustomerGetTVector T 向量读取：有效 JSON 按位映射、坏 JSON 回落基准、超长截断
func TestCustomerGetTVector(t *testing.T) {
	base := &model.Customer{IntentScore: 0.5, TrustLevel: 0.25}

	// 1) 坏 JSON：必须回落 BuildBaseTVector，而不是返回全零向量（全零会让策略引擎以为"全新冷客户"）
	bad := *base
	bad.TVectorJSON = "{不是数组]"
	got := bad.GetTVector()
	want := base.BuildBaseTVector()
	for i := range want {
		if !almostEq(got[i], want[i]) {
			t.Fatalf("坏 JSON 应回落基准向量，T[%d] got %v want %v", i, got[i], want[i])
		}
	}

	// 2) 空串：等同坏 JSON 分支（走基准值）
	empty := *base
	empty.TVectorJSON = ""
	if g := empty.GetTVector(); !almostEq(g[0], want[0]) {
		t.Errorf("空 TVectorJSON 应走基准向量，T[0]=%v want %v", g[0], want[0])
	}

	// 3) 有效 JSON：按位置覆盖，短向量剩余维度补 0
	short := *base
	short.TVectorJSON = "[0.9,0.1,0.2]"
	sv := short.GetTVector()
	if !almostEq(sv[0], 0.9) || !almostEq(sv[1], 0.1) || !almostEq(sv[2], 0.2) {
		t.Errorf("有效 JSON 未按位置映射: %v", sv[:3])
	}
	for i := 3; i < 32; i++ {
		if sv[i] != 0 {
			t.Errorf("短向量越界维应补 0，T[%d]=%v", i, sv[i])
			break
		}
	}

	// 4) 超长 JSON：截断到 32 维（数组长度固定，越界必须丢弃）
	long := *base
	long.TVectorJSON = "[" + repeat("1,", 40) + "1]"
	lv := long.GetTVector()
	for i := 0; i < 32; i++ {
		if lv[i] != 1 {
			t.Errorf("超长向量前 32 维应全为 1，T[%d]=%v", i, lv[i])
		}
	}

	// 5) SaveTVector↔GetTVector 回环（写路径与读路径编码一致）
	rt := *base
	rt.SaveTVector([32]float64{7: 0.42, 31: 0.99})
	rv := rt.GetTVector()
	if !almostEq(rv[7], 0.42) || !almostEq(rv[31], 0.99) {
		t.Errorf("SaveTVector↔GetTVector 回环不一致: T[7]=%v T[31]=%v", rv[7], rv[31])
	}
	if !regexp.MustCompile(`^\[\d`).MatchString(rt.TVectorJSON) {
		t.Errorf("SaveTVector 应写入 JSON 数组，实际 %q", rt.TVectorJSON)
	}
}

// repeat 生成 n 份 s 的拼接（构造超长向量 JSON 用）
func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// ============================================================
// 3. Conversation 状态双写
// ============================================================

// TestConversationStateDoubleWrite 会话状态：独立列与 StateJSON 双写必须一致，且除零安全
func TestConversationStateDoubleWrite(t *testing.T) {
	conv := &model.Conversation{TenantID: 1, CustomerID: 2}

	// 1) Attempts=0：HookRate 必须为 0（无除零 panic）
	st := conv.GetState()
	if st.Attempts != 0 || st.HookRate != 0 {
		t.Errorf("初始状态应 Attempts=0 HookRate=0，实际 %d/%v", st.Attempts, st.HookRate)
	}

	// 2) SaveState 双写：列与 JSON 同时更新
	saved := model.SessionState{
		Attempts: 4, HookCount: 3, HookRate: 0.75, LastTid: "tpl_scarcity_001",
		LastAnchorType: model.AnchorTypeScarcity, Emotion: "positive",
		HighIntentRounds: 2, SilentDuration: 30, CurrentStage: 3,
	}
	conv.SaveState(saved)
	if conv.Attempts != 4 || conv.HookCount != 3 || conv.LastTid != "tpl_scarcity_001" ||
		conv.LastAnchorType != model.AnchorTypeScarcity || conv.Emotion != "positive" ||
		conv.HighIntentRounds != 2 || conv.SilentDuration != 30 || conv.CurrentStage != 3 {
		t.Errorf("SaveState 未写回独立列: %+v", conv)
	}
	var jsonState model.SessionState
	if err := json.Unmarshal([]byte(conv.StateJSON), &jsonState); err != nil {
		t.Fatalf("SaveState 应写入合法 StateJSON，实际 %q err=%v", conv.StateJSON, err)
	}
	if jsonState.Attempts != saved.Attempts || jsonState.HookCount != saved.HookCount ||
		jsonState.LastTid != saved.LastTid || jsonState.CurrentStage != saved.CurrentStage {
		t.Errorf("StateJSON 与入参不一致: %+v vs %+v", jsonState, saved)
	}
	// 3) GetState 读回：JSON 仅覆盖 HookRate，其余以列为准
	got := conv.GetState()
	if got.Attempts != 4 || got.HookCount != 3 || !almostEq(got.HookRate, 0.75) {
		t.Errorf("GetState 读回异常: %+v", got)
	}

	// 4) IncrementAttempts：列自增 + JSON 同步（不除零）
	inc := &model.Conversation{}
	for i := 1; i <= 3; i++ {
		inc.IncrementAttempts()
		if inc.Attempts != i {
			t.Fatalf("第 %d 次 IncrementAttempts 后 Attempts=%d", i, inc.Attempts)
		}
		var js model.SessionState
		if err := json.Unmarshal([]byte(inc.StateJSON), &js); err != nil {
			t.Fatalf("IncrementAttempts 后 StateJSON 非法: %q err=%v", inc.StateJSON, err)
		}
		if js.Attempts != i {
			t.Errorf("IncrementAttempts 双写不一致: 列=%d JSON=%d", i, js.Attempts)
		}
		if r := inc.GetState(); r.HookRate != 0 {
			t.Errorf("零接钩时 HookRate 应为 0，实际 %v", r.HookRate)
		}
	}
	// 5) RecordHook 后接钩率的【现状·缺陷】：StateJSON 的 hook_rate 一旦 >0，
	//    GetState 就用它覆盖按列重算的值（合并方向是"JSON 覆盖列"），而 RecordHook 自己
	//    又会把这次算出的值写回 JSON——于是接钩率被永久锁在第一次 RecordHook 算出的 1/3，
	//    即使已接钩 2 次也不回升。本测试如实固化该现状（不改产品代码），
	//    真实值应为 2/3：修复时需把 GetState 的 JSON 覆盖分支改为"仅补空不覆盖"。
	inc.RecordHook()
	inc.RecordHook()
	got2 := inc.GetState()
	if got2.HookCount != 2 || got2.Attempts != 3 {
		t.Errorf("RecordHook 后列值异常: %+v", got2)
	}
	if !almostEq(got2.HookRate, 1.0/3.0) {
		t.Errorf("【现状】接钩率被旧 StateJSON 锁死，应读到首次 RecordHook 落盘的 1/3，实际 %v", got2.HookRate)
	}

	// 6) 脏 JSON：非法 StateJSON 不得让 GetState panic，且列值仍生效
	dirty := &model.Conversation{Attempts: 5, HookCount: 1, StateJSON: "{坏 JSON"}
	d := dirty.GetState()
	if d.Attempts != 5 || d.HookCount != 1 || !almostEq(d.HookRate, 0.2) {
		t.Errorf("坏 StateJSON 下应按列计算，实际 %+v", d)
	}
	// 7) JSON 里的 HookRate>0 会覆盖列算出的值（现状：允许人工回填历史接钩率）
	override := &model.Conversation{Attempts: 1, HookCount: 0, StateJSON: `{"attempts":1,"hook_count":0,"hook_rate":0.9}`}
	if r := override.GetState(); !almostEq(r.HookRate, 0.9) {
		t.Errorf("【现状】StateJSON 的 hook_rate>0 应覆盖列算值，实际 %v", r.HookRate)
	}
	// 8) UpdateSilentDuration：无最后消息时间时不得改动已存时长
	ts := &model.Conversation{}
	ts.UpdateSilentDuration()
	if ts.SilentDuration != 0 {
		t.Errorf("LastMessageAt 为空时静默时长应保持 0，实际 %d", ts.SilentDuration)
	}
}

// ============================================================
// 4. 标签权重 / 配置值 / 锚类型 / 访客密钥
// ============================================================

// TestTagWeightMappingClamp 权重叠加必须 clamp 在 0-1（否则多轮对话后维度爆炸）
func TestTagWeightMappingClamp(t *testing.T) {
	cases := []struct {
		name      string
		direction string
		delta     float64
		current   float64
		want      float64
	}{
		{name: "常规上调", direction: "up", delta: 0.1, current: 0.5, want: 0.6},
		{name: "常规下调", direction: "down", delta: 0.1, current: 0.5, want: 0.4},
		{name: "上调越界钳到 1", direction: "up", delta: 0.9, current: 0.5, want: 1},
		{name: "下调越界钳到 0", direction: "down", delta: 0.9, current: 0.5, want: 0},
		{name: "边界：正好到 1", direction: "up", delta: 0.5, current: 0.5, want: 1},
		{name: "边界：正好到 0", direction: "down", delta: 0.5, current: 0.5, want: 0},
		{name: "方向缺省按上调", direction: "", delta: 0.2, current: 0.1, want: 0.3},
		{name: "非法方向按上调（只有 down 取负）", direction: "sideways", delta: 0.2, current: 0.1, want: 0.3},
		{name: "负 delta 反向", direction: "up", delta: -0.2, current: 0.1, want: 0},
		{name: "零值不改", direction: "down", delta: 0, current: 0.77, want: 0.77},
		{name: "起点已越界仍钳回", direction: "up", delta: 0, current: 3.5, want: 1},
	}
	for _, tc := range cases {
		m := &model.TagWeightMapping{TVectorIndex: 0, WeightDelta: tc.delta, Direction: tc.direction}
		got := m.ApplyToVector(tc.current)
		if !almostEq(got, tc.want) {
			t.Errorf("ApplyToVector(%s: current=%v delta=%v dir=%q)=%v want %v",
				tc.name, tc.current, tc.delta, tc.direction, got, tc.want)
		}
		if got < 0 || got > 1 {
			t.Errorf("ApplyToVector(%s) 结果越界 [0,1]: %v", tc.name, got)
		}
	}
}

// TestSystemConfigValueGetters 配置值解析容错：垃圾字符串必须回落零值而非 panic
func TestSystemConfigValueGetters(t *testing.T) {
	intCases := []struct {
		value string
		want  int
	}{
		{value: "42", want: 42},
		{value: "-3", want: -3},
		{value: "0", want: 0},
		{value: "2.9", want: 2},    // 整数解析失败 → float 截断
		{value: "1e3", want: 1000}, // 科学计数法走 float 分支
		{value: `"7"`, want: 0},    // JSON 字符串不是数字 → 零值
		{value: "abc", want: 0},
		{value: "", want: 0},
		{value: "[]", want: 0},
		{value: "9223372036854775807", want: math.MaxInt64}, // 边界：正好 int64 上限，ParseInt 直接成功
		// 注：超出 int64 的字面量（如 1e22）不钉死——整数解析失败后走 float 分支，
		// float64→int 溢出在 arm64 饱和成 MaxInt64、amd64 成 MinInt64，平台相关，断言会变成跨机器假失败。
	}
	for _, tc := range intCases {
		c := &model.SystemConfig{Value: tc.value}
		if got := c.GetIntValue(); got != tc.want {
			t.Errorf("GetIntValue(%q)=%d want %d", tc.value, got, tc.want)
		}
	}

	boolCases := []struct {
		value string
		want  bool
	}{
		{value: "true", want: true},
		{value: "false", want: false},
		{value: "TRUE", want: false},   // 大小写敏感
		{value: `"true"`, want: false}, // 字符串不是布尔
		{value: "1", want: false},      // 数字不是布尔（不猜 truthy）
		{value: "yes", want: false},
		{value: "", want: false},
	}
	for _, tc := range boolCases {
		c := &model.SystemConfig{Value: tc.value}
		if got := c.GetBoolValue(); got != tc.want {
			t.Errorf("GetBoolValue(%q)=%v want %v", tc.value, got, tc.want)
		}
	}

	sliceCases := []struct {
		name  string
		value string
		want  []int
	}{
		{name: "整数数组", value: "[3,8]", want: []int{3, 8}},
		{name: "小数数组截断", value: "[3.7,8.2]", want: []int{3, 8}},
		{name: "空数组", value: "[]", want: []int{}},
		{name: "字符串数组不兼容", value: `["a"]`, want: []int{}},
		{name: "对象", value: `{"a":1}`, want: []int{}},
		{name: "垃圾", value: "not json", want: []int{}},
		{name: "空串", value: "", want: []int{}},
		{name: "混合数组整体失败", value: `[3,"a"]`, want: []int{}},
		{name: "嵌套数组", value: "[[1]]", want: []int{}},
	}
	for _, tc := range sliceCases {
		c := &model.SystemConfig{Value: tc.value}
		got := c.GetIntSliceValue()
		if len(got) != len(tc.want) {
			t.Errorf("GetIntSliceValue(%s: %q)=%v len want %d", tc.name, tc.value, got, len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("GetIntSliceValue(%s: %q)[%d]=%d want %d", tc.name, tc.value, i, got[i], tc.want[i])
			}
		}
		if got == nil {
			t.Errorf("GetIntSliceValue(%q) 返回 nil 切片（调用方 append/range 语义不同，现状应为空切片）", tc.value)
		}
	}

	strCases := []struct {
		value string
		want  string
	}{
		{value: `"hello"`, want: "hello"},
		{value: `""`, want: ""},
		{value: "raw-text", want: "raw-text"}, // 非法 JSON 原样返回（老配置项直接存裸串）
		{value: "42", want: "42"},             // 数字裸串同样原样返回
		{value: `["a","b"]`, want: `["a","b"]`},
	}
	for _, tc := range strCases {
		c := &model.SystemConfig{Value: tc.value}
		if got := c.GetStringValue(); got != tc.want {
			t.Errorf("GetStringValue(%q)=%q want %q", tc.value, got, tc.want)
		}
		// GetJSONValue 无论合法与否都返回原值（不做二次编码）
		if got := c.GetJSONValue(); got != tc.value {
			t.Errorf("GetJSONValue(%q)=%q 应原样返回", tc.value, got)
		}
	}

	// GetStringSliceValue：非法输入返回空切片而非 nil
	for _, v := range []string{"[]", `[`, "abc", ``} {
		c := &model.SystemConfig{Value: v}
		if got := c.GetStringSliceValue(); got == nil || len(got) != 0 {
			t.Errorf("GetStringSliceValue(%q)=%v 应为非 nil 空切片", v, got)
		}
	}
	if got := (&model.SystemConfig{Value: `["wechat","web"]`}).GetStringSliceValue(); len(got) != 2 || got[0] != "wechat" {
		t.Errorf("GetStringSliceValue 正常解析异常: %v", got)
	}
}

// TestAnchorTypeNameCoversConstants 锚类型名称映射必须覆盖全部 AnchorType* 常量
func TestAnchorTypeNameCoversConstants(t *testing.T) {
	// 注意：同类锚与场景锚刻意同值（见下方断言），所以这里用切片而不是 map（map 字面量会重复键报错）
	for _, typ := range []int{
		model.AnchorTypeNoThrow,
		model.AnchorTypeSameKind,
		model.AnchorTypeDisassemble,
		model.AnchorTypeCompare,
		model.AnchorTypeLoss,
		model.AnchorTypeScarcity,
		model.AnchorTypeSelfPay,
	} {
		name, ok := model.AnchorTypeName[typ]
		if !ok {
			t.Errorf("AnchorTypeName 缺锚类型 %d 的名称（前端会显示空/未知）", typ)
			continue
		}
		if name == "" {
			t.Errorf("AnchorTypeName[%d] 为空串", typ)
		}
	}
	if model.AnchorTypeName[model.AnchorTypeNoThrow] != "不抛" {
		t.Errorf("AnchorTypeName[0] 应为「不抛」，实际 %q", model.AnchorTypeName[model.AnchorTypeNoThrow])
	}
	// 【现状固化】同类锚与场景锚刻意同值（aggressiveness 相同），字典里也刻意合并成一条
	if model.AnchorTypeSameKind != model.AnchorTypeScene {
		t.Errorf("同类锚与场景锚应同为 %d，实际 %d/%d", model.AnchorTypeSameKind, model.AnchorTypeSameKind, model.AnchorTypeScene)
	}
	if len(model.AnchorTypeName) != 7 {
		t.Errorf("锚类型名称表应为 7 条（0-6），实际 %d", len(model.AnchorTypeName))
	}
	// 稀缺/代价自担锚是促单话术，编码必须落在 5/6（策略引擎按数值判定）
	if model.AnchorTypeScarcity != 5 || model.AnchorTypeSelfPay != 6 {
		t.Errorf("促单类锚编码漂移: 稀缺=%d 代价自担=%d", model.AnchorTypeScarcity, model.AnchorTypeSelfPay)
	}
}

// TestGenerateVisitorKey 访客密钥：64 位小写十六进制且互不相同（C3 横向越权防线）
func TestGenerateVisitorKey(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		k := model.GenerateVisitorKey()
		if !re.MatchString(k) {
			t.Fatalf("访客密钥格式异常（应为 32 字节随机数的十六进制）: %q", k)
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("访客密钥重复: %s", k)
		}
		seen[k] = struct{}{}
	}
	// 表字段容量约束：size:64 恰好等于密钥长度，多一位会被截断/报错
	if len(model.GenerateVisitorKey()) != 64 {
		t.Errorf("访客密钥长度应为 64")
	}
}

// TestCustomerTagHelpers 标签 JSON 帮助函数：空值/去重/解析失败都不 panic
func TestCustomerTagHelpers(t *testing.T) {
	c := &model.Customer{}
	if got := c.GetTags(); got == nil || len(got) != 0 {
		t.Errorf("空标签应返回非 nil 空切片，实际 %#v", got)
	}
	c.AddTag("high_intent")
	c.AddTag("high_intent")
	c.AddTag("price_sensitive")
	if got := c.GetTags(); len(got) != 2 || got[0] != "high_intent" || got[1] != "price_sensitive" {
		t.Errorf("AddTag 去重/追加异常: %#v", got)
	}
	c.SetTags([]string{"a"})
	if got := c.GetTags(); len(got) != 1 || got[0] != "a" {
		t.Errorf("SetTags↔GetTags 回环异常: %#v", got)
	}
	// 脏数据（老行可能是裸文本）：不 panic，长度为 0
	// 【现状】空 Tags 走早返回给非 nil 空切片，坏 JSON 走 Unmarshal 失败给 nil 切片；
	// 两者都只能靠 len() 判空，调用方用 `got != nil` 判分支会走错路。
	dirty := &model.Customer{Tags: "不是JSON"}
	if got := dirty.GetTags(); len(got) != 0 {
		t.Errorf("坏 Tags 应返回长度 0，实际 %#v", got)
	}
}

// TestTableNameContract 表名固定：改表名等于改迁移，测试钉住以免误动
func TestTableNameContract(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "User", got: model.User{}.TableName(), want: "tenant_users"},
		{name: "Customer", got: model.Customer{}.TableName(), want: "customers"},
		{name: "Conversation", got: model.Conversation{}.TableName(), want: "conversations"},
		{name: "Template", got: model.Template{}.TableName(), want: "templates"},
		{name: "TagWeightMapping", got: model.TagWeightMapping{}.TableName(), want: "tag_weight_mappings"},
		{name: "SystemConfig", got: model.SystemConfig{}.TableName(), want: "system_configs"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s.TableName()=%q want %q", tc.name, tc.got, tc.want)
		}
	}
}
