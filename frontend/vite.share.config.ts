import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import moduleIcon from "./vite-icon";

// 分享页单独构建：网关只对 /apps/sharephotos/s/ 不要求登录，所以分享页的页面、脚本和图标都要放在 s/ 下面，
// 不能和管理页共用 assets/。在管理页之后构建，不清空 dist。
export default defineConfig({
  base: "/apps/sharephotos/",
  plugins: [react(), moduleIcon("s/")],
  build: {
    emptyOutDir: false,
    assetsDir: "s/assets",
    rollupOptions: { input: "s/index.html" },
  },
});
