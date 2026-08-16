# syntax=docker/dockerfile:1

# ---- 构建阶段：纯 Go 静态编译 ----
# Go 版本固定到 go.mod 声明的 toolchain 1.25.13（与 CI setup-go 的 go-version-file 一致）。
# TARGETOS/TARGETARCH 由 Buildx 多架构构建自动注入，支持 linux/amd64 与 linux/arm64。
FROM golang:1.25.13-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# 先复制依赖清单并下载，利用 BuildKit 层缓存。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 正式产物与发布约束保持一致：CGO_ENABLED=0、trimpath、剥离符号表。
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/werewolf ./cmd/werewolf

# ---- 运行时阶段：非 root、只读文件系统 ----
# distroless static 包含 CA 证书（Telegram API TLS 所需）且无 shell；
# nonroot 用户 UID 65532。
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/werewolf /usr/local/bin/werewolf

# SQLite 数据、配置与日志全部使用外部挂载 /data，不写入镜像层。
# 容器部署示例（对应 deploy/systemd/ 之外的容器方式）：
#   chown 65532:65532 /var/lib/telegram-werewolf
#   docker run -v /var/lib/telegram-werewolf:/data \
#     ghcr.io/v2up-32mb/telegram-werewolf:v0.1.0
# 注意：容器以 nonroot（UID 65532）运行，挂载目录必须允许该 UID 写入。
VOLUME ["/data"]

USER nonroot:nonroot

# 不固定 EXPOSE：应用监听地址由 config.yaml 的 health_address 决定（默认禁用），
# 不对外暴露固定端口。
ENTRYPOINT ["/usr/local/bin/werewolf"]
CMD ["serve", "--config", "/data/config.yaml"]
