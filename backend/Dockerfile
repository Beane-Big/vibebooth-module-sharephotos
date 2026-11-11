# 构建上下文是 backend/。依赖在构建时下载，本机打包和服务器上的构建器都一样；下载过的依赖留在 Docker 的构建缓存里。
FROM golang:1.26-alpine AS build
# 服务器上的构建器可以传入国内的 Go 代理，不传时用官方代理。
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY . .
# 先 go mod tidy 补齐 go.mod 和 go.sum：加了新依赖、本机又没有 Go 时也能构建。
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/app .

FROM alpine:3.22
# 服务器上的构建器可以传入国内的 Alpine 镜像（例如 https://mirrors.tencent.com/alpine），不传时用官方源。
ARG APK_MIRROR=
# libheif-tools 带的 heif-dec 用来把 HEIC/HEIF 照片转成预览图；Go 没有现成的 HEVC 解码器。
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${APK_MIRROR%/}#" /etc/apk/repositories; fi && \
    apk add --no-cache libheif-tools
RUN adduser -D -u 10001 app
# 数据目录挂载命名卷；先建好并交给 app 用户，卷第一次创建时会沿用这个属主。
RUN mkdir /data && chown app /data
USER app
COPY --from=build /out/app /usr/local/bin/app
EXPOSE 8000
ENTRYPOINT ["app"]
