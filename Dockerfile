# ============================================================
# AI-SCRM 引擎镜像（P4 本地交付）
# 多阶段构建：前端 React 构建 → golang 编译 → alpine 运行
# ============================================================
FROM node:20-alpine AS fe
WORKDIR /fe
COPY frontend-react/package*.json ./
RUN npm install
COPY frontend-react/ ./
RUN npm run build

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
# React SPA 产物（main.go 由 frontend-react/dist 托管 / 与 /app）
COPY --from=fe /fe/dist ./frontend-react/dist
EXPOSE 8080
ENTRYPOINT ["./ai-scrm"]
