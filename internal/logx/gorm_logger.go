// 脱敏版 GORM logger（C3，2026-09-12）
// GORM 默认 logger 在 Info 态会把 fc() 产出的"插值后 SQL"（含手机号/邮箱等参数明文）整条打屏。
// logger.Config 没有改写文案的钩子，故包一层 Trace：拿 fc() 的 sql 经 logx.Mask 后再交给内层。
// 级别策略不变：release=Warn（连参数都不打），debug=Info（打但已掩码）。
package logx

import (
	"context"
	"time"

	"gorm.io/gorm/logger"
)

type redactLogger struct {
	inner logger.Interface
}

// NewGormLogger 包一层 SQL 参数脱敏；inner 通常传 logger.Default.LogMode(level)
func NewGormLogger(inner logger.Interface) logger.Interface {
	return &redactLogger{inner: inner}
}

func (l *redactLogger) LogMode(level logger.LogLevel) logger.Interface {
	return &redactLogger{inner: l.inner.LogMode(level)}
}

func (l *redactLogger) Info(ctx context.Context, format string, args ...interface{}) {
	l.inner.Info(ctx, Mask(format), args...)
}

func (l *redactLogger) Warn(ctx context.Context, format string, args ...interface{}) {
	l.inner.Warn(ctx, Mask(format), args...)
}

func (l *redactLogger) Error(ctx context.Context, format string, args ...interface{}) {
	l.inner.Error(ctx, Mask(format), args...)
}

// Trace 关键收口：把 fc() 返回的插值 SQL 掩码后再委托内层格式化输出
func (l *redactLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, rows := fc()
	sql = Mask(sql)
	l.inner.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
}
