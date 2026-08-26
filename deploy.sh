#!/bin/bash
# ============================================================
# AI-SCRM 本地化部署脚本（P4，2026-08-26）
#
# 用法：
#   ./deploy.sh init     首次部署检查（密钥/包/配置自检）
#   ./deploy.sh build    构建引擎镜像并启动
#   ./deploy.sh pack     打包行业包源目录为 .aipack（等价 cmd/pack build）
#
# 前置：
#   - Docker & docker compose
#   - keys/pack_priv.pem + pack_pub.pem（packtool keygen 生成，随部署物分发）
#   - packs/*.aipack 行业包文件
#   - .env 已按生产环境配置（DB 密码/JWT_SECRET 必须）
# ============================================================
set -e
cd "$(dirname "$0")"

case "$1" in
  init)
    echo "==== 部署前自检 ===="
    [ -f .env ] || { echo "✗ 缺少 .env（cp .env.example .env 并按生产配置）"; exit 1; }
    grep -q "^JWT_SECRET=change-me" .env && { echo "✗ JWT_SECRET 仍为占位符——必须替换为随机32位串"; exit 1; }
    [ -f keys/pack_priv.pem ] || echo "⚠ 缺少 keys/pack_priv.pem（行业包解封需要）"
    [ -f keys/pack_pub.pem ] || echo "⚠ 缺少 keys/pack_pub.pem（验签需要）"
    ls packs/*.aipack >/dev/null 2>&1 && echo "✓ 行业包: $(ls packs/*.aipack | wc -l | tr -d ' ') 个" || echo "⚠ packs/ 下无 .aipack（可启动后经超管后台上传）"
    grep -q "^AI_MOCK_MODE=false" .env && echo "ℹ AI_MOCK_MODE=false：请确认已配置 LLM 网关凭证或厂商Key"
    echo "✓ 自检完成"
    ;;
  build)
    ./deploy.sh init
    docker compose -f docker-compose.prod.yml up -d --build
    echo "✓ 已启动。健康检查: curl http://localhost:${SERVER_PORT:-8080}/health"
    ;;
  pack)
    shift
    go run ./cmd/pack build -src "${1:?用法: pack <源目录> <输出.aipack> <code> <名称> <版本> <level> [parent]}" \
      "${@:2}"
    ;;
  *)
    sed -n '2,20p' "$0"
    ;;
esac
