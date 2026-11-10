import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import moduleIcon from "./vite-icon";

// 门户里的管理页挂在 /apps/sharephotos/ 下；本地开发时把接口请求转给本机平台的网关（8080 端口），
// npm run standalone 时由 VB_API 指向独立运行的本机后端。
// 分享页 s/index.html 另外构建（见 vite.share.config.ts），开发时由同一个开发服务器提供。
export default defineConfig({
  base: "/apps/sharephotos/",
  plugins: [react(), moduleIcon(), sharePage()],
  server: {
    proxy: {
      "/api": process.env.VB_API ?? "http://localhost:8080",
    },
  },
});

// 开发时 /apps/sharephotos/s/<令牌> 返回分享页，和 nginx.conf 里的规则一样。
function sharePage(): Plugin {
  return {
    name: "sharephotos-share-page",
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        if (/^\/apps\/sharephotos\/s\/[\w-]+\/?(\?.*)?$/.test(req.url ?? "")) req.url = "/apps/sharephotos/s/index.html";
        next();
      });
    },
  };
}
