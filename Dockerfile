# syntax=docker/dockerfile:1

# 构建阶段：静态编译服务端，前端文件已经通过 go:embed 打包进二进制
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/topogen-server ./cmd/server

# 运行阶段
FROM alpine:3.20
RUN addgroup -S topogen && adduser -S -G topogen topogen \
    && mkdir -p /data && chown topogen:topogen /data
COPY --from=build /out/topogen-server /usr/local/bin/topogen-server
USER topogen
ENV TOPOGEN_ADDR=:8080 TOPOGEN_DATA=/data
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["topogen-server"]
