import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { GalleryUrls, Photo } from "../api";
import Star from "./Star";
import { animateSwitch, transitionName } from "./transition";

// 平铺的列数存在浏览器里，下次打开还是一样。间距和圆角固定，列多时圆角跟着变小。
const KEY = "sharephotos:tiles";

function loadColumns(): number | null {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "null");
    return v && typeof v.columns === "number" ? v.columns : null;
  } catch {
    return null;
  }
}

function saveColumns(columns: number) {
  try {
    localStorage.setItem(KEY, JSON.stringify({ columns }));
  } catch {
    // 浏览器不让存时只是下次打开回到默认值。
  }
}

// 没调过时按屏幕宽度定列数：手机三列，电脑四列；最多手机 6 列、电脑 10 列。
const defaultColumns = (width: number) => (width < 600 ? 3 : width < 900 ? 3 : 4);
const maxColumns = (width: number) => (width < 600 ? 6 : 10);

// Tiles 是平铺：照片按原来的比例排成瀑布流，每张放进当前最矮的一列，顺序大致从左到右、从上到下。
// 像 iPhone 的“照片”一样用双指缩放改列数：张开变少、变大，捏合变多、变小；电脑上用触控板缩放或者按住 Ctrl（Mac 上是 ⌘）滚动滚轮。
// 缩放时整面照片先跟着手指放大缩小，松手后换成最接近的列数，手指下面的那张照片留在原地。点照片看大图，点右上角的星星选中。
export default function Tiles({
  photos,
  urls,
  selected,
  coverId,
  onOpen,
  onToggle,
  onDeleteFailed,
}: {
  photos: Photo[];
  urls: GalleryUrls;
  selected: Set<number>;
  coverId?: number;
  onOpen: (p: Photo) => void;
  onToggle: (p: Photo) => void;
  onDeleteFailed?: (p: Photo) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [saved, setSaved] = useState<number | null>(loadColumns);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const count = Math.min(maxColumns(width), Math.max(1, saved ?? defaultColumns(width)));
  const phone = width < 600;
  const gap = phone ? 2 : 4;
  const radius = count >= 5 ? 2 : phone ? 4 : 6;

  // 缩放手势。最新的列数放在 ref 里，事件处理不用每次重新绑定。
  const latest = useRef({ count, width });
  latest.current = { count, width };
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    let scale = 1;
    let center = { x: 0, y: 0 };
    let pinching = false;
    let startDist = 0;
    let wheelTimer: ReturnType<typeof setTimeout>;

    // 缩放过程中整面照片跟着变大变小，以手指（或鼠标）的位置为中心。
    const begin = (x: number, y: number) => {
      const r = el.getBoundingClientRect();
      center = { x, y };
      scale = 1;
      el.style.transition = "none";
      el.style.transformOrigin = `${x - r.left}px ${y - r.top}px`;
      el.classList.add("pinching");
    };
    const zoom = (s: number) => {
      scale = Math.min(3, Math.max(0.3, s));
      el.style.transform = `scale(${scale})`;
    };
    // 松手后换列数：每放大（缩小）到 1.6 倍左右少（多）一列，缩放得不够多时弹回去。
    const end = () => {
      const { count, width } = latest.current;
      let next = count - Math.round(Math.log(scale) / Math.log(1.6));
      if (scale > 1.15) next = Math.min(next, count - 1);
      if (scale < 0.87) next = Math.max(next, count + 1);
      next = Math.min(maxColumns(width), Math.max(1, next));
      el.classList.remove("pinching");
      if (next === count) {
        el.style.transition = "";
        el.style.transform = "";
        return;
      }
      // 记下手指下面那张照片在屏幕上的位置，换列数以后滚动页面让它留在原地。
      const anchor = document.elementFromPoint(center.x, center.y)?.closest<HTMLElement>(".tile");
      const before = anchor?.getBoundingClientRect().top;
      animateSwitch(
        () => {
          el.style.transform = "";
          setSaved(next);
          saveColumns(next);
        },
        () => {
          if (anchor && before !== undefined && anchor.isConnected) window.scrollBy(0, anchor.getBoundingClientRect().top - before);
        },
      );
    };

    const dist = (t: TouchList) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY);
    const touchStart = (e: TouchEvent) => {
      if (e.touches.length !== 2) return;
      pinching = true;
      startDist = dist(e.touches);
      begin((e.touches[0].clientX + e.touches[1].clientX) / 2, (e.touches[0].clientY + e.touches[1].clientY) / 2);
    };
    const touchMove = (e: TouchEvent) => {
      if (!pinching || e.touches.length !== 2) return;
      // 两根手指时不让浏览器滚动或放大整页。
      e.preventDefault();
      zoom(dist(e.touches) / startDist);
    };
    const touchEnd = (e: TouchEvent) => {
      if (!pinching || e.touches.length >= 2) return;
      pinching = false;
      end();
    };
    // 触控板缩放在 Chrome、Edge、Firefox 里是按住 Ctrl 的滚轮。
    const wheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      if (!el.classList.contains("pinching")) begin(e.clientX, e.clientY);
      zoom(scale * Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.02 : 0.004)));
      clearTimeout(wheelTimer);
      wheelTimer = setTimeout(end, 160);
    };
    // Mac 上的 Safari 触控板缩放发的是 gesture 事件；iPhone 上也有，但那边用上面的 touch 事件，这里只拦住整页放大。
    const touchDevice = "ontouchstart" in window;
    const gestureStart = (e: Event) => {
      e.preventDefault();
      if (!touchDevice) begin((e as MouseEvent).clientX, (e as MouseEvent).clientY);
    };
    const gestureChange = (e: Event) => {
      e.preventDefault();
      if (!touchDevice) zoom((e as Event & { scale: number }).scale);
    };
    const gestureEnd = (e: Event) => {
      e.preventDefault();
      if (!touchDevice) end();
    };

    el.addEventListener("touchstart", touchStart, { passive: true });
    el.addEventListener("touchmove", touchMove, { passive: false });
    el.addEventListener("touchend", touchEnd);
    el.addEventListener("touchcancel", touchEnd);
    el.addEventListener("wheel", wheel, { passive: false });
    el.addEventListener("gesturestart", gestureStart);
    el.addEventListener("gesturechange", gestureChange);
    el.addEventListener("gestureend", gestureEnd);
    return () => {
      clearTimeout(wheelTimer);
      el.removeEventListener("touchstart", touchStart);
      el.removeEventListener("touchmove", touchMove);
      el.removeEventListener("touchend", touchEnd);
      el.removeEventListener("touchcancel", touchEnd);
      el.removeEventListener("wheel", wheel);
      el.removeEventListener("gesturestart", gestureStart);
      el.removeEventListener("gesturechange", gestureChange);
      el.removeEventListener("gestureend", gestureEnd);
    };
  }, []);

  // 没处理好的照片不知道比例，先当成正方形。
  const columns: Photo[][] = Array.from({ length: count }, () => []);
  const heights = new Array(count).fill(0);
  for (const p of photos) {
    const i = heights.indexOf(Math.min(...heights));
    columns[i].push(p);
    heights[i] += p.width && p.height ? p.height / p.width : 1;
  }
  // 一列比缩略图（长边 400px）宽时用大图，免得糊。
  const colWidth = width ? (width - gap * (count - 1)) / count : 0;
  const kind = colWidth * (window.devicePixelRatio || 1) > 420 ? "preview" : "thumb";

  return (
    <div className="stack tiles-wrap">
      <div ref={box} className="tiles" style={{ gap }}>
        {width > 0 &&
          columns.map((col, i) => (
            <div key={i} className="tiles-col" style={{ gap }}>
              {col.map((p) => (
                <Tile
                  key={p.id}
                  photo={p}
                  src={p.status === "ready" ? urls.photo(p, kind) : ""}
                  radius={radius}
                  on={selected.has(p.id)}
                  cover={coverId === p.id}
                  onOpen={() => onOpen(p)}
                  onToggle={() => onToggle(p)}
                  onDeleteFailed={onDeleteFailed && (() => onDeleteFailed(p))}
                />
              ))}
            </div>
          ))}
      </div>
    </div>
  );
}

function Tile({
  photo: p,
  src,
  radius,
  on,
  cover,
  onOpen,
  onToggle,
  onDeleteFailed,
}: {
  photo: Photo;
  src: string;
  radius: number;
  on: boolean;
  cover: boolean;
  onOpen: () => void;
  onToggle: () => void;
  onDeleteFailed?: () => void;
}) {
  const ratio = p.width && p.height ? `${p.width} / ${p.height}` : "1";
  if (p.status !== "ready")
    return (
      <div className="tile pending" style={{ aspectRatio: ratio, borderRadius: radius }}>
        <span className="muted">{p.status === "processing" ? "处理中…" : "处理失败"}</span>
        {p.status === "failed" && onDeleteFailed && (
          <button className="link" onClick={onDeleteFailed}>
            删除
          </button>
        )}
      </div>
    );
  return (
    <div className="tile" style={{ aspectRatio: ratio, borderRadius: radius, viewTransitionName: transitionName(p) }}>
      <button className="tile-button" aria-label={`查看 ${p.name}`} onClick={onOpen}>
        <img src={src} alt="" loading="lazy" decoding="async" />
      </button>
      <Star on={on} name={p.name} onToggle={onToggle} />
      {cover && <span className="badge">封面</span>}
    </div>
  );
}

// useStoredView 记住上次用的是卡片还是平铺。
export function useStoredView(): ["cards" | "tiles", (v: "cards" | "tiles") => void] {
  const [view, setView] = useState<"cards" | "tiles">(() => {
    try {
      return localStorage.getItem("sharephotos:view") === "tiles" ? "tiles" : "cards";
    } catch {
      return "cards";
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem("sharephotos:view", view);
    } catch {
      // 存不了也不影响这次使用。
    }
  }, [view]);
  return [view, setView];
}
