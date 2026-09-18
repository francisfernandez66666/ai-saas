// §八-7 零测试包最小单测（2026-09-18）：mq 信封与 Header 的编排层契约——
//  1. buildEnvelope：oneID 缺失必须回落 `sys:t{tid}`（否则分区键为空 → 同租户事件乱序）、
//     Key 恒等于分区键 one_id（铁律 4）、payload 双层结构 {event_type,data}、trace_id 沿用请求链路；
//  2. kafkaHeaders ↔ fromKafkaMessage 往返一致，且消费侧对铁律字段（event_id/tenant_id）缺失必拒
//     ——一旦放宽，Inbox 幂等键会退化成空串，事件被静默吞或跨租户串事件；
//  3. Init 的降级选择：声明 kafka 但无 brokers 时回落 LogCenter（"配置缺了就整个 MQ 不可用"
//     会把启动炸掉，历史上 P2-78 附近踩过）。
//
// 不测端到端异步投递（那是 smoke/UAT 层职责，本包 recordAudit 依赖 DB 且异步不可控）。
package mq

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"ai-scrm/config"
	"ai-scrm/internal/testutil"

	"github.com/segmentio/kafka-go"
)

// TestMain 统一出口：本包若有 DB 用例被 testutil 跳过，收尾把跳过条数打到 stderr（防静默绿）
func TestMain(m *testing.M) {
	os.Exit(testutil.RunMain(m))
}

// traceKey 与生产侧一致：mq 用字符串键读 gin ctx 透传的 trace_id
const traceKey = "trace_id"

// TestBuildEnvelopeOneIDFallbackAndPartitionKey 空 oneID 回落与分区键一致性
func TestBuildEnvelopeOneIDFallbackAndPartitionKey(t *testing.T) {
	env := buildEnvelope(context.Background(), TopicUserEvent, 42, "", "lead_captured",
		map[string]any{"phone": "13800001111"})

	if env.Header.OneID != "sys:t42" {
		t.Errorf("空 oneID 应回落 sys:t{tenant}，实际 %q", env.Header.OneID)
	}
	if env.Key != env.Header.OneID {
		t.Errorf("分区键 Key 应恒等于 one_id，实际 Key=%q oneID=%q", env.Key, env.Header.OneID)
	}
	if env.Topic != TopicUserEvent {
		t.Errorf("Topic=%q want %q", env.Topic, TopicUserEvent)
	}
	if env.Header.TenantID != 42 {
		t.Errorf("TenantID=%d want 42", env.Header.TenantID)
	}
	if env.Header.EventID == "" {
		t.Errorf("EventID 不能为空（Inbox 幂等键）")
	}
	if env.Header.EventTime.IsZero() {
		t.Errorf("EventTime 不能为零值")
	}
	// trace_id 缺省走随机（16 位 hex，8 字节）
	if len(env.Header.TraceID) != 16 {
		t.Errorf("缺 trace 时应生成 16 位随机 hex，实际 %q", env.Header.TraceID)
	}

	// 传了 oneID 就原样保留，且仍是分区键
	env2 := buildEnvelope(context.Background(), TopicFlowDrive, 7, "one_abc", "flow_requeue", nil)
	if env2.Header.OneID != "one_abc" || env2.Key != "one_abc" {
		t.Errorf("显式 oneID 应透传并作为分区键，实际 one=%q key=%q", env2.Header.OneID, env2.Key)
	}
}

// TestBuildEnvelopePayloadShapeAndTraceReuse payload 双层结构与 trace 沿用
func TestBuildEnvelopePayloadShapeAndTraceReuse(t *testing.T) {
	ctx := context.WithValue(context.Background(), traceKey, "trace-from-gin") //nolint:staticcheck
	env := buildEnvelope(ctx, TopicUserEvent, 3, "one_x", "conversation_msg",
		map[string]any{"k": "v"})

	if env.Header.TraceID != "trace-from-gin" {
		t.Errorf("应沿用请求链路 trace_id，实际 %q", env.Header.TraceID)
	}
	var parsed struct {
		EventType string         `json:"event_type"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &parsed); err != nil {
		t.Fatalf("payload 非合法 JSON: %v raw=%s", err, env.Payload)
	}
	if parsed.EventType != "conversation_msg" {
		t.Errorf("payload.event_type=%q want conversation_msg", parsed.EventType)
	}
	if parsed.Data["k"] != "v" {
		t.Errorf("payload.data 原样丢失: %s", env.Payload)
	}
	// 空 oneID + ctx=nil 也不能 panic（后台任务传 nil 是真实调用形态）
	if got := buildEnvelope(nil, TopicFlowResult, 0, "", "x", nil); got.Header.OneID != "sys:t0" {
		t.Errorf("nil ctx 场景应回落 sys:t0，实际 %q", got.Header.OneID)
	}
	// payload 序列化兜底：不可 marshal 的类型不得 panic，要落 marshal_error 标记
	bad := buildEnvelope(context.Background(), TopicUserEvent, 1, "one_y", "bad", func() {})
	if !strings.Contains(string(bad.Payload), "marshal_error") {
		t.Errorf("不可序列化 payload 应写 marshal_error，实际 %s", bad.Payload)
	}
}

// TestKafkaHeadersRoundTrip 信封 → Header → 信封：铁律字段与 Key/Payload 全量一致
func TestKafkaHeadersRoundTrip(t *testing.T) {
	env := buildEnvelope(context.Background(), TopicUserEvent, 9, "one_rt", "order_created",
		map[string]any{"amount_cents": 12345})
	env.Header.TraceID = "trace-rt"

	msg := kafka.Message{
		Topic:   env.Topic,
		Key:     []byte(env.Key),
		Value:   env.Payload,
		Headers: kafkaHeaders(env),
	}
	got, err := fromKafkaMessage(env.Topic, msg)
	if err != nil {
		t.Fatalf("合法消息不应被拒: %v", err)
	}
	if got.Header.TenantID != env.Header.TenantID {
		t.Errorf("tenant_id 往返不一致: %d vs %d", got.Header.TenantID, env.Header.TenantID)
	}
	if got.Header.OneID != env.Header.OneID || got.Key != env.Key {
		t.Errorf("one_id/key 往返不一致: one=%q key=%q", got.Header.OneID, got.Key)
	}
	if got.Header.EventID != env.Header.EventID {
		t.Errorf("event_id 往返不一致: %q vs %q", got.Header.EventID, env.Header.EventID)
	}
	if got.Header.TraceID != "trace-rt" {
		t.Errorf("trace_id 往返不一致: %q", got.Header.TraceID)
	}
	if !got.Header.EventTime.Equal(env.Header.EventTime) {
		t.Errorf("event_time 往返不一致: %v vs %v", got.Header.EventTime, env.Header.EventTime)
	}
	if string(got.Payload) != string(env.Payload) {
		t.Errorf("payload 往返不一致")
	}
	// Header 铁律四件必须齐（下游只读 Header，不解析 payload）
	want := map[string]bool{"tenant_id": false, "one_id": false, "event_id": false, "event_time": false}
	for _, h := range kafkaHeaders(env) {
		if _, ok := want[h.Key]; ok {
			want[h.Key] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("kafkaHeaders 缺铁律头 %s", k)
		}
	}
}

// TestFromKafkaMessageRejectsMissingMandatoryHeaders 铁律字段缺失必拒（方向不可反）
func TestFromKafkaMessageRejectsMissingMandatoryHeaders(t *testing.T) {
	full := []kafka.Header{
		{Key: "tenant_id", Value: []byte("5")},
		{Key: "one_id", Value: []byte("one_z")},
		{Key: "event_id", Value: []byte("evt-1")},
		{Key: "event_time", Value: []byte(time.Now().Format(time.RFC3339Nano))},
		{Key: "trace_id", Value: []byte("tr-1")},
	}
	cases := []struct {
		name    string
		dropKey string // 删掉某个必需键
		set     map[string]string
	}{
		{name: "缺 event_id", dropKey: "event_id"},
		{name: "缺 tenant_id", dropKey: "tenant_id"},
		{name: "tenant_id=0 视为非法", dropKey: "", set: map[string]string{"tenant_id": "0"}},
		{name: "tenant_id 垃圾值按 0 处理", dropKey: "", set: map[string]string{"tenant_id": "abc"}},
		{name: "event_id 空串视为非法", dropKey: "", set: map[string]string{"event_id": ""}},
	}
	for _, tc := range cases {
		hdrs := make([]kafka.Header, 0, len(full))
		for _, h := range full {
			if h.Key == tc.dropKey {
				continue
			}
			if v, ok := tc.set[h.Key]; ok {
				h = kafka.Header{Key: h.Key, Value: []byte(v)}
			}
			hdrs = append(hdrs, h)
		}
		_, err := fromKafkaMessage(TopicUserEvent, kafka.Message{Topic: TopicUserEvent, Headers: hdrs})
		if err == nil {
			t.Errorf("%s：应拒收，实际放行", tc.name)
		}
	}
}

// TestInitCenterSelection Init 的降级选择（测完恢复 DefaultCenter）
func TestInitCenterSelection(t *testing.T) {
	old := DefaultCenter
	defer func() { DefaultCenter = old }()

	// 1) 声明 kafka 但 brokers 为空 → 降级 LogCenter（不 panic、不置 nil）
	Init(config.MQConfig{Type: "kafka", TopicPrefix: "test."})
	if _, ok := DefaultCenter.(*LogCenter); !ok {
		t.Fatalf("kafka 无 brokers 应降级 *LogCenter，实际 %T", DefaultCenter)
	}

	// 2) Type 空（默认）→ LogCenter
	Init(config.MQConfig{Type: "", TopicPrefix: "test."})
	if _, ok := DefaultCenter.(*LogCenter); !ok {
		t.Fatalf("MQ_TYPE 未设置应使用 *LogCenter，实际 %T", DefaultCenter)
	}

	// 3) Type=log → LogCenter；kafka 带 brokers → KafkaCenter（仅构造生产者，不连 broker）
	Init(config.MQConfig{Type: "log"})
	if _, ok := DefaultCenter.(*LogCenter); !ok {
		t.Fatalf("MQ_TYPE=log 应为 *LogCenter，实际 %T", DefaultCenter)
	}
	Init(config.MQConfig{Type: "kafka", Brokers: []string{"127.0.0.1:9092"}, TopicPrefix: "test."})
	kc, ok := DefaultCenter.(*KafkaCenter)
	if !ok {
		t.Fatalf("MQ_TYPE=kafka 且有 brokers 应为 *KafkaCenter，实际 %T", DefaultCenter)
	}
	if kc.cfg.TopicPrefix != "test." {
		t.Errorf("KafkaCenter 未带上配置前缀: %q", kc.cfg.TopicPrefix)
	}
	_ = kc.Close()

	// 4) 未初始化时 Publish 明确报错而不是 nil panic
	DefaultCenter = nil
	if err := Publish(context.Background(), TopicUserEvent, 1, "one", "x", nil); err == nil {
		t.Errorf("DefaultCenter=nil 时 Publish 应报错")
	}
	// Subscribe/StartConsumers/Close 同样要能扛住未初始化（启动顺序竞态）
	DefaultCenter = nil
	Subscribe(TopicUserEvent, func(context.Context, Envelope) error { return nil })
	StartConsumers(context.Background())
	Close()
}

// TestExtractEventType 审计落库的事件名提取：合法/非法/缺字段三口径
func TestExtractEventType(t *testing.T) {
	if got := extractEventType([]byte(`{"event_type":"lead_captured","data":{}}`)); got != "lead_captured" {
		t.Errorf("extractEventType=%q want lead_captured", got)
	}
	for _, bad := range []string{"", "not json", `{"data":{}}`, `{"event_type":123}`} {
		if got := extractEventType([]byte(bad)); got != "unknown" {
			t.Errorf("extractEventType(%q)=%q want unknown", bad, got)
		}
	}
}
