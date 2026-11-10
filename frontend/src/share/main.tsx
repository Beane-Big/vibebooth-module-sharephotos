import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import ShareApp from "./ShareApp";
import "../styles.css";

// 分享页的入口：家人从微信里点开的就是它，不需要登录，也不在门户里打开。
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ShareApp />
  </StrictMode>,
);
