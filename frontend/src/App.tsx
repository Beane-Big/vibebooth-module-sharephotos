import Album from "./pages/Album";
import Home from "./pages/Home";
import Settings from "./pages/Settings";

// 只有相册列表、相册、相册设置和应用设置几个页面，用路径区分即可，不引入路由库。
// 分享页（/apps/sharephotos/s/）是另外一套构建，见 vite.share.config.ts。
export default function App() {
  const path = location.pathname;
  if (path.startsWith("/apps/sharephotos/settings")) return <Settings />;
  const album = path.match(/^\/apps\/sharephotos\/a\/(\d+)/);
  if (album) return <Album id={Number(album[1])} settings={/^\/apps\/sharephotos\/a\/\d+\/settings/.test(path)} />;
  return <Home />;
}
