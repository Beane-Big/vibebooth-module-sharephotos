import { flushSync } from "react-dom";

// 卡片和平铺切换时的动画，用浏览器的视图过渡（View Transitions）：同一张照片在两种看法里用同一个名字，
// 浏览器把它从旧位置、旧大小平滑地移到新位置。不支持的浏览器（较旧的 iOS 和 Firefox）退回淡入，见 styles.css 的 .switching。

export const transitionName = (p: { id: number }) => `sp-photo-${p.id}`;

export function animateSwitch(change: () => void) {
  const supported = "startViewTransition" in document;
  if (!supported || matchMedia("(prefers-reduced-motion: reduce)").matches) {
    change();
    if (supported) return;
    // 退回方案：给照片墙加一个淡入，动画结束后去掉。
    const el = document.querySelector(".gallery");
    el?.classList.remove("switching");
    void (el as HTMLElement | null)?.offsetWidth;
    el?.classList.add("switching");
    return;
  }
  // React 要在回调里同步改完界面，浏览器才能拍到新的样子。
  document.startViewTransition(() => flushSync(change));
}
