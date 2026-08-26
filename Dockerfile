# ============================================================
# AI-SCRM 引擎镜像（P4 本地交付）
# 多阶段构建：golang 编译 → alpine 运行
# ============================================================
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ai-scrm ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/ai-scrm ./ai-scrm
COPY frontend ./frontend
EXPOSE 8080
ENTRYPOINT ["./ai-scrm"]
