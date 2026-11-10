import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { GalleryUrls, Photo } from "../api";
import Star from "./Star";
import { transitionName } from "./transition";

// 平铺的三个可调参数，存在浏览器里，下次打开还是一样。
type Layout = { columns: number; gap: number; radius: number };

const KEY = "sharephotos:tiles";

function loadLayout(): Layout | null {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "null");
    return v && typeof v.columns === "number" ? v : null;
  } catch {
    return null;
  }
}

function saveLayout(l: Layout) {
  try {
    localStorage.setItem(KEY, JSON.stringify(l));
  } catch {
    // 浏览器不让存时只是下次打开回到默认值。
  }
}

// 没调过时按屏幕宽度定列数：手机两列，电脑四列。
const defaultColumns = (width: number) => (width < 600 ? 2 : width < 900 ? 3 : 4);

// Tiles 是平铺：照片按原来的比例排成瀑布流，每张放进当前最矮的一列，顺序大致从左到右、从上到下。
// 照片墙上的“调整平铺”打开 tuning 时，上面显示改列数、间距和圆角的滑块。点照片看大图，点右上角的星星选中。
export default function Tiles({
  photos,
  urls,
  tuning,
  selected,
  coverId,
  onOpen,
  onToggle,
  onDeleteFailed,
}: {
  photos: Photo[];
  urls: GalleryUrls;
  tuning: boolean;
  selected: Set<number>;
  coverId?: number;
  onOpen: (p: Photo) => void;
  onToggle: (p: Photo) => void;
  onDeleteFailed?: (p: Photo) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [layout, setLayout] = useState<Layout | null>(loadLayout);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const current: Layout = layout ?? { columns: defaultColumns(width), gap: width < 600 ? 4 : 8, radius: 8 };
  const set = (patch: Partial<Layout>) => {
    const next = { ...current, ...patch };
    setLayout(next);
    saveLayout(next);
  };

  // 没处理好的照片不知道比例，先当成正方形。
  const columns: Photo[][] = Array.from({ length: current.columns }, () => []);
  const heights = new Array(current.columns).fill(0);
  for (const p of photos) {
    const i = heights.indexOf(Math.min(...heights));
    columns[i].push(p);
    heights[i] += p.width && p.height ? p.height / p.width : 1;
  }
  // 一列比缩略图（长边 400px）宽时用大图，免得糊。
  const colWidth = width ? (width - current.gap * (current.columns - 1)) / current.columns : 0;
  const kind = colWidth * (window.devicePixelRatio || 1) > 420 ? "preview" : "thumb";

  return (
    <div className="stack tiles-wrap">
      {tuning && (
        <div className="tuner">
          <Slider label="列数" value={current.columns} min={1} max={8} onChange={(columns) => set({ columns })} />
          <Slider label="间距" value={current.gap} min={0} max={32} onChange={(gap) => set({ gap })} />
          <Slider label="圆角" value={current.radius} min={0} max={32} onChange={(radius) => set({ radius })} />
        </div>
      )}
      <div ref={box} className="tiles" style={{ gap: current.gap }}>
        {width > 0 &&
          columns.map((col, i) => (
            <div key={i} className="tiles-col" style={{ gap: current.gap }}>
              {col.map((p) => (
                <Tile
                  key={p.id}
                  photo={p}
                  src={p.status === "ready" ? urls.photo(p, kind) : ""}
                  radius={current.radius}
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

function Slider({
  label,
  value,
  min,
  max,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (v: number) => void;
}) {
  return (
    <label className="slider">
      <span>
        {label}
        <span className="muted"> {value}</span>
      </span>
      <input type="range" min={min} max={max} value={value} onChange={(e) => onChange(e.target.valueAsNumber)} />
    </label>
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
