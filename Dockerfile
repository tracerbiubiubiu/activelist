# activelist apiserver 多阶段构建。迁移已 embed 进二进制（internal/repository/migrate.go
# iofs），镜像内无需 migrations 目录；config 经镜像内置默认 + 环境变量覆盖（§6）。
# 构建期走国内镜像源（对齐 zhuzhao deployments/Dockerfile 惯例）。
FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
# 可在受限构建机传入 -p / GOMAXPROCS；不带入运行镜像。
ARG GOFLAGS
ARG GOMAXPROCS
RUN sed -i 's#https://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories && \
    go env -w GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/apiserver ./cmd/apiserver

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -g 10001 app \
 && adduser -D -u 10001 -G app app
WORKDIR /app
COPY --from=build /out/apiserver /app/apiserver
COPY configs/config.yaml /app/config/config.yaml
# 日志目录（utils logger 惰性建目录——uid 10001 在 root 属主的 /app 下无权创建，
# 且 MultiWriter 文件先行会短路 stdout：缺此行 = 部署态全部日志静默丢失）
RUN mkdir -p /app/logs && chown -R app:app /app
USER app
EXPOSE 18081
ENTRYPOINT ["/app/apiserver", "serve", "/app/config/config.yaml"]
