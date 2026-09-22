// Package config 全局配置中心：集中管理服务/数据库/Redis/MQ/AI/策略超参数等。
// 本文件承载 P1-4 生产姿态启动断言（prod 必须 release 模式）。
package config

import (
	"fmt"
	"os"

	"github.com/gin-gonic/gin"
)

// AssertProdGinMode P1-4(2026-09-22)：生产姿态启动断言。
// 当 APP_ENV=prod 且 gin 运行模式非 release 时返回错误（调用方应 log.Fatalf 拒启），
// 把"prod+debug"这一合规红线从软告警升级为硬拒绝；其余情形（dev/test、prod+release）放行。
// 须在 gin.SetMode 之后、业务初始化之前调用，使 gin.Mode() 已反映 GIN_MODE 取值。
func AssertProdGinMode() error {
	return assertProdMode(os.Getenv("APP_ENV"), gin.Mode())
}

// assertProdMode 断言核心逻辑（可单测纯函数）：
//   - appEnv=="prod" 且 ginMode!=gin.ReleaseMode → 报错（拒绝启动）；
//   - 其余（dev / 非 prod / prod+release）一律放行。
func assertProdMode(appEnv, ginMode string) error {
	if appEnv == "prod" && ginMode != gin.ReleaseMode {
		return fmt.Errorf("APP_ENV=prod 要求 GIN_MODE=release（当前 %s）", ginMode)
	}
	return nil
}
