# 照片分享（sharephotos）模块约定（给 AI 编码工具和自己看）

这是一个 Vibebooth 模块，遵循模块规范 `platform: 1`：https://github.com/Beane-Big/vibebooth/blob/main/docs/module-spec.md 。不符合规范时 `npm run pack` 会直接报错。下面是本模块自己的约定。

- 模块 id 是 `sharephotos`：页面在 `/apps/sharephotos/` 下，接口在 `/api/sharephotos/` 下，容器叫 `sharephotos-frontend` 和 `sharephotos-backend`。
- 不要自己写网关路由标签、端口映射或网络，安装器会补上；`compose.yaml` 允许的字段见规范。
- 图标只有一份：根目录的 `icon.svg`（`module.yaml` 里的 `icon`）。平台主页、浏览器页签和 iOS 添加到主屏幕都用它，构建前端时自动复制一份、再画成 iOS 用的 `apple-touch-icon.png`（React 和 Vue 由 `frontend/vite-icon.ts` 做并写进 `index.html`，纯静态页面由 `frontend/touch-icon.mjs` 做）。不要在 `frontend/` 里另放图标或手写图标链接；图标里不要用 `<text>`，文字先转成路径。
- 不写登录代码。后端用 SDK `github.com/Beane-Big/vibebooth/packages/sdk-go/auth` 拿当前用户。
- 接口权限：要按人控制“能做什么”（例如编辑、清空）时，在 `module.yaml` 的 `permissions` 里声明，后端用 `auth.UserFrom(ctx).Has("<权限 id>")` 检查；没有声明功能权限时用 `HasMenu("<菜单 id>")`。不要用角色判断。前端用 `platform.ts` 的 `can(me, "<权限 id>")` 隐藏没权限的按钮，但真正的检查在后端。
- 前端和平台打交道（取当前用户、权限）只经过 `frontend/src/platform.ts`（纯静态页面是 `public/platform.js`），不要在别处直接请求 `/me`。
- 人员（家庭成员）由平台保存，不要在模块里另建人员表：用平台的对外接口 `http://core:8080/api/platform/ext/v1/` 只读地取人员名单，模块只保存人员 id。显示人员时带上 `?viewer=<令牌里的 sub>`，用返回的 `displayName`（每个人给别人写的备注优先，没写时是姓名）。模块不能改人员资料和头像。
- 后端必须保留 `GET /api/sharephotos/health`，正常时返回 2xx。
- 数据只存在 `compose.yaml` 里声明的卷里，默认用 SQLite。改表结构要兼容老数据。
- 要调用其他模块，只调用对方 `/api/<id>/ext/v1/` 下的对外接口，并在 `module.yaml` 的 `requires` 里写上对方。
- 要换模块 id 或名称，运行 `npm run rename -- <新 id> [新名称]`，不要手动全局替换。装到平台以后就不要再改 id。
- 发布新版本前把 `module.yaml` 的 `version` 改大，再运行 `npm run pack`，或者 `npm run pack:source` 后上传到平台的构建器。
- 后端加了 Go 依赖不用在本机运行 `go mod tidy`，构建镜像时会补齐；Dockerfile 里的 `ARG GOPROXY`、`ARG NPM_REGISTRY` 和缓存挂载不要删，服务器构建靠它们换镜像源、复用下载过的依赖。

## 本模块自己的约定

- 两个功能权限：`upload`（上传照片：建相册、上传、分享、续期、删除自己的相册）和 `view`（预览照片：在门户里看所有还在有效期内的相册）。管理相册只认上传者本人（`canManage`），管理员也不能改别人的相册；链接到期后只有上传者看得到。
- 分享页不需要登录，靠平台的 `public-share` 特权：网关对 `/apps/sharephotos/s/` 和 `/api/sharephotos/s/` 不做登录校验。
  - 后端只在 `backend/share.go` 里给这几个具体路径挂免登录的处理，访问由链接里 128 位的令牌、有效期和可选的提取码控制；不要用“路径以 s/ 开头”之类的判断跳过登录，`%2F` 会被解码成 `/`。
  - 前端的分享页单独构建（`frontend/vite.share.config.ts`，入口 `frontend/s/index.html` 和 `src/share/`），页面、脚本和图标都放在 `dist/s/` 下；管理页的 `assets/` 要登录，分享页不能引用它。分享页只用 `src/share/` 里自己的请求函数，不经过 `platform.ts`，401 表示还没输提取码，不能跳去登录。
  - 新加免登录的接口时，想清楚没有令牌的人能拿到什么。
- 照片只收 JPEG。上传时边写边去掉 EXIF 里的 GPS 和 XMP（`backend/images.go`），再由一个后台任务逐张生成 2048px 预览图和 400px 缩略图，纯 Go，不要引入要 CGO 的图像库。
- 下载原图走总带宽限制（设置页“下载限速”）。批量下载是前端一张一张下原图（`components/Downloader.tsx`），不用 zip：断了用 Range 续传，关页面后能接着下。电脑上的 Chrome、Edge 选文件夹直接写入，iPhone 每 10 张用系统分享面板“存储图像”，其他浏览器逐张存到“下载”。
- 微信内置浏览器不能批量下载：前端在微信里不显示下载全部，单张照片显示原图让人长按保存（`inline=1`）。

## 和平台耦合的等级

当前等级：3（分权）

模块和平台耦合多深分成 5 级，规则见模块规范里的“和平台耦合的等级”。只用到当前等级为止的平台契约，不要越级。要升级时在本目录运行下面的命令，它只加文件；已经改过的文件不覆盖，而是打印要手工加的内容。升级后这一行会跟着改。

| 等级 | 用到的平台契约 | 升级命令 |
| --- | --- | --- |
| 0 纯前端 | 无 | |
| 1 加后端、2 认人 | 网关注入的令牌（SDK 校验），令牌里的账号 id 和名字 | `npx github:Beane-Big/create-vibebooth add backend`，要存数据再 `add data` |
| 3 分权 | 令牌里的 `menus` 和 `perms`，`module.yaml` 的 `menus` 和 `permissions` | `npx github:Beane-Big/create-vibebooth add permissions` |
| 4 用平台数据 | 平台的人员接口 `http://core:8080/api/platform/ext/v1/` | `npx github:Beane-Big/create-vibebooth add persons` |

离开平台也能运行：`npm run standalone`。后端开独立模式（环境变量 `STANDALONE=1`），所有请求都当成一个拥有全部权限的本地用户；不要在 `compose.yaml` 里设这个变量，装到平台里时 SDK 会拒绝请求。第 4 级在独立模式下取不到平台的人员，要自己降级（`backend/persons.go` 里有示例）。
