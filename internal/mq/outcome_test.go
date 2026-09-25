// G-15 观测面真实性单测（2026-09-24）：消息中心台账的**消费终态回写**规则。
//
// 这块逻辑修的是"永远全绿的假账"：以前 message_event_records 只在发布阶段落一行 sent，
// 消费者无论成功还是重试耗尽被丢弃都不改这行。于是运维面看到的 sent 永远等于"已妥投"，
// 而默认形态（MQ_TYPE=log，进程内总线）丢事件比 Kafka 更静默——连 offset 都没地方回滚。
//
// 用例钉的四条判据，每条都是"缺它就会重新变回假账"的形态：
//  1. dead_letter 不被 consumed 洗白（扇出场景甲成功乙失败时，台账必须留住更差的事实）；
//  2. 反向要能覆盖（consumed 之后另一消费者判死，终态转 dead_letter）；
//  3. 行已被 dead_letter 占用时**不得补建**第二条（撞唯一索引 + 把已认账的死信重复计一次）；
//  4. 补建行必须显式带 tenant_id（消费协程没有请求 ctx，盖章回调拿不到租户 → C7 红线）。
//
// 以及两处纯函数：错误原因汇总、按字符而非字节的截断（按字节切中文会让 PG 直接拒写）。
package mq

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-scrm/internal/db"
	"ai-scrm/internal/logx"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestMain 统一出口见 envelope_test.go（同包共用一个，勿重复声明）

// setupOutcomeDB 接库 + 造一个真实租户（死信/终态行要落 tenant_id，租户不存在会撞外键）
func setupOutcomeDB(t *testing.T) uint {
	t.Helper()
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	t.Cleanup(func() {
		db.DB.Where("event_id LIKE ?", "ut-outcome-%").Delete(&model.MessageEventRecord{})
	})
	return tid
}

// seedRecord 直插一条发布阶段台账（status 通常是 sent）
func seedRecord(t *testing.T, tid uint, eventID, status, payload string) {
	t.Helper()
	rec := model.MessageEventRecord{
		TenantID: tid, OneID: "one_ut", EventID: eventID, EventType: "lead_captured",
		Topic: TopicUserEvent, Key: "one_ut", Payload: payload, Status: status, TraceID: "trace-pub",
	}
	if err := db.DB.Create(&rec).Error; err != nil {
		t.Fatalf("预置台账失败: %v", err)
	}
}

// loadRecord 按 event_id 读回台账（断言用）
func loadRecord(t *testing.T, eventID string) model.MessageEventRecord {
	t.Helper()
	var got model.MessageEventRecord
	if err := db.DB.Where("event_id = ?", eventID).First(&got).Error; err != nil {
		t.Fatalf("读回台账失败 event=%s: %v", eventID, err)
	}
	return got
}

// countRecords 数某 event_id 的行数（判"有没有被补建出第二条"）
func countRecords(t *testing.T, eventID string) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&model.MessageEventRecord{}).Where("event_id = ?", eventID).Count(&n).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	return n
}

// newEnv 造一个只填必要字段的信封（终态回写只读 Header + Topic/Payload）
func newEnv(eventID string, tid uint, trace string) Envelope {
	env := Envelope{Topic: TopicUserEvent, Key: "one_ut", Payload: []byte(`{"event_type":"lead_captured","data":{}}`)}
	env.Header.TenantID = tid
	env.Header.OneID = "one_ut"
	env.Header.EventID = eventID
	env.Header.TraceID = trace
	return env
}

// TestMarkEventOutcomeConsumed 成功终态：改写同一行、清掉旧 err_msg、trace 落到消费侧
func TestMarkEventOutcomeConsumed(t *testing.T) {
	tid := setupOutcomeDB(t)
	ev := "ut-outcome-ok"
	seedRecord(t, tid, ev, "sent", `{"event_type":"lead_captured","data":{}}`)
	// 预置一条脏 err_msg，验证 consumed 会把它清干净（否则前端/运维看到的是"成功但带原因"）
	if err := db.DB.Model(&model.MessageEventRecord{}).Where("event_id = ?", ev).
		Update("err_msg", "历史残留").Error; err != nil {
		t.Fatalf("预置脏原因失败: %v", err)
	}

	if err := markEventOutcome(newEnv(ev, tid, "trace-consume"), StatusConsumed, ""); err != nil {
		t.Fatalf("consumed 回写失败: %v", err)
	}
	got := loadRecord(t, ev)
	if got.Status != StatusConsumed {
		t.Errorf("status=%q want %q", got.Status, StatusConsumed)
	}
	if got.ErrMsg != "" {
		t.Errorf("consumed 应清空 err_msg，实际 %q", got.ErrMsg)
	}
	if got.TraceID != "trace-consume" {
		t.Errorf("trace_id=%q want trace-consume（消费侧要能按同一条 trace 追）", got.TraceID)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("updated_at 未落：分不出「发布即失败」与「发了三小时才消费掉」")
	}
	if n := countRecords(t, ev); n != 1 {
		t.Errorf("终态必须改写同一行，现在台账有 %d 行", n)
	}
}

// TestDeadLetterNotWashedByConsumed 核心反洗白用例：甲成功、乙失败时留住 dead_letter
func TestDeadLetterNotWashedByConsumed(t *testing.T) {
	tid := setupOutcomeDB(t)
	ev := "ut-outcome-dl-first"
	seedRecord(t, tid, ev, "sent", `{}`)

	if err := markEventOutcome(newEnv(ev, tid, "tr"), StatusDeadLetter, "consumer#1: 库超时"); err != nil {
		t.Fatalf("dead_letter 回写失败: %v", err)
	}
	// 另一个消费者随后成功——这是扇出场景的真实时序，绝不能把死信改回 consumed
	if err := markEventOutcome(newEnv(ev, tid, "tr"), StatusConsumed, ""); err != nil {
		t.Fatalf("后续 consumed 不应报错: %v", err)
	}
	got := loadRecord(t, ev)
	if got.Status != StatusDeadLetter {
		t.Fatalf("死信被 consumed 洗白（status=%q）——观测面会重新变成假账", got.Status)
	}
	if got.ErrMsg != "consumer#1: 库超时" {
		t.Errorf("死信原因被抹掉，实际 %q", got.ErrMsg)
	}
	// 不许补建第二条：event_id 唯一，且"重复记一条死信"会把运维数字放大
	if n := countRecords(t, ev); n != 1 {
		t.Errorf("死信行被重复补建，现在 %d 行", n)
	}
}

// TestConsumedThenDeadLetterTurnsRed 反向时序：先 consumed 后判死，终态必须转 dead_letter
// （缺这条就等于只封了"洗白"一个方向，另一个方向照样丢事件不留痕）
func TestConsumedThenDeadLetterTurnsRed(t *testing.T) {
	tid := setupOutcomeDB(t)
	ev := "ut-outcome-turn-red"
	seedRecord(t, tid, ev, "sent", `{}`)
	if err := markEventOutcome(newEnv(ev, tid, "tr"), StatusConsumed, ""); err != nil {
		t.Fatalf("consumed 回写失败: %v", err)
	}
	if err := markEventOutcome(newEnv(ev, tid, "tr"), StatusDeadLetter, "consumer#0: 反序列化失败"); err != nil {
		t.Fatalf("dead_letter 回写失败: %v", err)
	}
	got := loadRecord(t, ev)
	if got.Status != StatusDeadLetter {
		t.Errorf("status=%q want dead_letter（更差的事实必须覆盖更好的）", got.Status)
	}
	if !strings.Contains(got.ErrMsg, "反序列化失败") {
		t.Errorf("err_msg=%q 未带失败原因", got.ErrMsg)
	}
}

// TestMarkEventOutcomeCreatesMissingRow 发布阶段没落库时补建，且必须显式带 tenant_id
// （消费协程里没有 gin ctx，写入自动盖章拿不到租户——漏设就是 tenant_id=0 的跨租户可见行）
func TestMarkEventOutcomeCreatesMissingRow(t *testing.T) {
	tid := setupOutcomeDB(t)
	ev := "ut-outcome-create"
	if err := markEventOutcome(newEnv(ev, tid, "tr-new"), StatusDeadLetter, "no audit row"); err != nil {
		t.Fatalf("缺行时死信回写失败: %v", err)
	}
	got := loadRecord(t, ev)
	if got.TenantID != tid {
		t.Errorf("补建行 tenant_id=%d want %d（0=平台视图可见，等于跨租户泄露）", got.TenantID, tid)
	}
	if got.EventType != "lead_captured" {
		t.Errorf("event_type=%q 应从 payload 提出（补建行也要能被按类型统计）", got.EventType)
	}
	if got.Payload == "" {
		t.Errorf("补建行必须带 payload：这是这条事件唯一的副本，没 payload 就重放不了")
	}
}

// TestConsumedDoesNotCreateWhenRowExistsAsDeadLetter 已存在死信行时 consumed 静默放弃、不补建
func TestConsumedDoesNotCreateWhenRowExistsAsDeadLetter(t *testing.T) {
	tid := setupOutcomeDB(t)
	ev := "ut-outcome-no-create"
	seedRecord(t, tid, ev, StatusDeadLetter, `{}`)
	if err := markEventOutcome(newEnv(ev, tid, "tr"), StatusConsumed, ""); err != nil {
		t.Fatalf("应静默放弃而非报错: %v", err)
	}
	if n := countRecords(t, ev); n != 1 {
		t.Errorf("行数=%d want 1（撞唯一索引的补建会把整轮回写成 error 日志刷屏）", n)
	}
	if got := loadRecord(t, ev); got.Status != StatusDeadLetter {
		t.Errorf("status=%q want dead_letter", got.Status)
	}
}

// TestMarkEventOutcomeNilDBSafe 未接库时不得 panic、不得报错：
// 消费循环里一次 DB 抖动绝不能变成消费者返回值异常，否则事件会被无限重投
func TestMarkEventOutcomeNilDBSafe(t *testing.T) {
	old := db.DB
	defer func() { db.DB = old }()
	db.DB = nil
	if err := markEventOutcome(newEnv("ut-nil", 1, "tr"), StatusDeadLetter, "x"); err != nil {
		t.Errorf("未接库应返回 nil（降级为无操作），实际 %v", err)
	}
}

// TestOutcomeErrText 多消费者原因汇总：空切片必须是空串（consumed 终态不留脏原因）
func TestOutcomeErrText(t *testing.T) {
	if got := outcomeErrText(nil); got != "" {
		t.Errorf("outcomeErrText(nil)=%q want 空串", got)
	}
	if got := outcomeErrText([]string{"a", "b"}); got != "a; b" {
		t.Errorf("outcomeErrText=%q want %q", got, "a; b")
	}
}

// TestTruncateRunesKeepsUTF8 按字符截断：中/英混排被按字节切会留下半个 UTF-8 序列，PG 直接拒写
func TestTruncateRunesKeepsUTF8(t *testing.T) {
	long := strings.Repeat("消费失败：下游超时", 100) // 中文为主，字节数远超字符数
	got := truncateRunes(long, 50)
	if utf8.RuneCountInString(got) != 50 {
		t.Errorf("截断后字符数=%d want 50", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8，落库会被 22023 拒")
	}
	short := "abc"
	if truncateRunes(short, 50) != short {
		t.Errorf("未超长不得改动，实际 %q", truncateRunes(short, 50))
	}
}

// TestBuildEnvelopeTraceUsesLogx 发布端 trace 取值走 logx 单点（与消费侧注入同一套键）：
// ctx 有 trace 就沿用，没有就自造 16 位 hex，两条腿都不能空
func TestBuildEnvelopeTraceUsesLogx(t *testing.T) {
	ctx := logx.ContextWithTrace(context.Background(), "tr-unified")
	if got := buildEnvelope(ctx, TopicUserEvent, 1, "one_t", "x", nil).Header.TraceID; got != "tr-unified" {
		t.Errorf("应沿用 ctx trace，实际 %q", got)
	}
	if got := buildEnvelope(context.Background(), TopicUserEvent, 1, "one_t", "x", nil).Header.TraceID; len(got) != 16 {
		t.Errorf("无 trace 时应自造 16 位 hex，实际 %q", got)
	}
	// 空 trace 的 ctx 不得把键塞进 ctx 后再读出空串（ContextWithTrace 自身短路）
	if got := logx.TraceFrom(logx.ContextWithTrace(context.Background(), "")); got != "" {
		t.Errorf("空 trace 不应污染 ctx，实际读出 %q", got)
	}
}
