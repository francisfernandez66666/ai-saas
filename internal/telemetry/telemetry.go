// Package telemetry OpenTelemetry 最小装配（P2-7）。
//
// 背景：原可观测性仅"响应头回显 TraceID"，无 span/依赖拓扑。本包引入 otel，
// 提供全局 TracerProvider（stdout exporter，零外部端点依赖，本地/网关即可看 span 时序）。
// 生产可换 OTLP exporter（设 OTEL_EXPORTER_OTLP_ENDPOINT 后改 Init 即可，接口不变）。
//
// 范围说明（诚实）：本包 + 网关 AI 调用路径的 span 是最小落地；全链路 span 拓扑、
// 跨服务 context 传播是持续工作（见 FIXPLAN P2-7）。
package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer

// Init 初始化全局 TracerProvider 并设为 otel 全局默认。
// 返回 shutdown（进程退出时 flush + 关闭 exporter）。serviceName 写入 resource service.name。
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	exp, err := stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(attribute.String("service.name", serviceName)),
	)
	if err != nil {
		res = resource.NewSchemaless(attribute.String("service.name", serviceName))
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	tracer = tp.Tracer("ai-scrm")
	return tp.Shutdown, nil
}

// Tracer 返回全局 tracer；Init 未调用时返回 noop tracer（安全，不 nil 解引用）。
func Tracer() trace.Tracer {
	if tracer != nil {
		return tracer
	}
	return trace.NewNoopTracerProvider().Tracer("ai-scrm/noop")
}
