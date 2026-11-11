# 照片分享

把成片发给家人：建一个相册，上传照片（JPEG、PNG、WebP、HEIC/HEIF、GIF），生成一个链接发到微信里。家人不用注册，点开就能看大图、挑几张或全部下载原图。

- 链接默认 7 天有效，可以加 4 位提取码、续期或换链接；到期后照片再保留 7 天（都可以在设置里改），过了自动删除，也可以随时手动删除。
- 上传时自动去掉照片里的位置信息，原图其他内容不变。
- 下载原图有总带宽上限，免得家人一起下载时门户和其他应用打不开。
- 门户里有“预览照片”权限的人能看所有还在有效期内的相册；只有“上传照片”权限的人能建相册，而且只能管理自己的。
- 需要平台支持 `public-share` 特权（平台要更新到包含这个特权的版本，构建器要 0.5.1 及以上），安装时管理员要确认这个特权。

这是一个 [Vibebooth](https://github.com/Beane-Big/vibebooth) 模块，由 `create-vibebooth` 生成。

## 常用命令

都在本目录运行，需要 Node.js。

### 发布：推荐交给平台的构建器打包

| 命令 | 需要 | 做什么 |
| --- | --- | --- |
| `npm run pack:source` | 只要 Node.js | 把源码打成 `dist/sharephotos-<版本>-source.zip`（不含 `node_modules`、`dist`），上传到平台的“构建器”页面，由服务器构建出软件包，构建完在构建记录上点“安装” |

推荐用这种方式：本机不用装 Docker，镜像在服务器上构建，用的是构建器设置里的镜像源。前提是平台上已经装好构建器。GitHub 上的 “Download ZIP” 也可以直接上传给构建器。

### 本机打包和开发（可选）

| 命令 | 需要 | 做什么 |
| --- | --- | --- |
| `npm run pack` | Docker | 在本机构建镜像，生成 `dist/sharephotos-<版本>.zip`，上传到平台的“应用管理”页。平台还没装构建器时用 |
| `npm run install:local` | Docker 和本机平台 | 打包并装到本机平台，打开 http://localhost:8080 就能看到。改完代码再运行一次即可 |
| `npm run dev` | Docker 和本机平台 | 只启动前端开发服务器，接口转给本机平台，改界面时不用每次打包。先在 http://localhost:8080 登录一次，再打开终端里显示的地址 |

- 运行前先启动 Docker（Windows 和 Mac 上是 Docker Desktop）。
- “本机平台”指在平台仓库里用 `docker compose up -d` 跑起来的 Vibebooth。`pack` 不需要本机平台。
- `pack` 用安装器镜像检查并打包：本机有平台时用本机平台的，没有时下载发布版 `ghcr.io/beane-big/vibebooth-installer`，第一次要能连上 ghcr.io。镜像还是私有的话，先用有 `read:packages` 权限的令牌运行一次 `docker login ghcr.io`。
- 本机平台比模块旧时，`pack` 会用旧的安装器而报错（比如不认识新字段），先在平台仓库里 `git pull` 再 `docker compose up -d --build`。

### 离开平台单独运行

| 命令 | 需要 | 做什么 |
| --- | --- | --- |
| `npm run standalone` | Node.js；有后端时还要 Go | 后端开独立模式（不校验令牌，所有请求都当成拥有全部权限的本地用户，数据放在 `.data/`），前端开发服务器把接口转给它。打开终端里显示的地址，按 Ctrl+C 一起退出 |

分享页的网址是 `/apps/sharephotos/s/<令牌>`，在相册页“分享给家人”里复制，开发服务器同样能打开。

独立模式只用来在本机开发和演示，不要在 `compose.yaml` 里设 `STANDALONE`：装到平台里时 SDK 会拒绝所有请求。

### 升级到下一级

在本目录运行 `npx github:Beane-Big/create-vibebooth add <backend | data | permissions | persons>`，说明见 `AGENTS.md` 的“和平台耦合的等级”。

### 其他

| 命令 | 做什么 |
| --- | --- |
| `npm run rename -- <新 id> [新名称]` | 改模块 id，也可以同时改名称，代码、配置和文档里用到的地方一起改。只改名称就把 id 写成原来的。id 要在第一次装到平台前定好：装过的模块改 id 会变成另一个应用，旧数据不会跟过去 |

依赖在构建镜像时下载（Go 用 `GOPROXY`，npm 用 `NPM_REGISTRY`），本机不用装 Go。

发布新版本前，把 `module.yaml` 的 `version` 改大。
