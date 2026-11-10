import { useEffect, useLayoutEffect, useRef, useState, type PointerEvent } from "react";
import type { GalleryUrls, Photo } from "../api";
import Star from "./Star";
import { transitionName } from "./transition";

// 前后各画多少张；再远的看不清，不画能省下很多图片请求。
const visible = 14;

// Cards 是卡片：照片像一叠卡片水平排成一行，都正对着人，当前这张在正中最大，两边的往后退、越远越小。
// 打开时先叠成一摞再展开。左右滑动、滚轮或方向键换一张，点中间这张看大图，点旁边的跳过去，下面的滑块可以快速拖到任意一张。
// 每张右上角有星星，点了选中。
export default function Cards({
  photos,
  urls,
  paused,
  intro,
  start,
  selected,
  onIndex,
  onOpen,
  onToggle,
}: {
  photos: Photo[];
  urls: GalleryUrls;
  paused: boolean; // 看大图时不响应方向键
  intro: boolean; // 第一次打开时播放从一摞展开的动画；从平铺切过来时由切换动画负责，不再播放
  start: number; // 从第几张开始：切回卡片、看完大图时停在上次看的那张
  selected: Set<number>;
  onIndex: (i: number) => void;
  onOpen: (p: Photo) => void;
  onToggle: (p: Photo) => void;
}) {
  const stage = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  // pos 是正在显示的位置（可以是小数，动画中间），target 是要停到的那张。
  const first = Math.min(Math.max(0, start), Math.max(0, photos.length - 1));
  const [pos, setPos] = useState(first);
  const target = useRef(first);
  // 展开动画的进度，0 是叠成一摞，1 是排开。
  const [spread, setSpread] = useState(intro ? 0 : 1);
  const drag = useRef<{ x: number; start: number; moved: boolean } | null>(null);
  const last = photos.length - 1;

  useLayoutEffect(() => {
    const el = stage.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // 打开时从一摞展开，减少动态效果的设置下直接排开。
  useEffect(() => {
    if (!intro || matchMedia("(prefers-reduced-motion: reduce)").matches) return setSpread(1);
    let raf = 0;
    const begin = performance.now() + 200;
    const step = (now: number) => {
      const t = Math.min(1, Math.max(0, (now - begin) / 900));
      setSpread(1 - Math.pow(1 - t, 3));
      if (t < 1) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, []);

  // 每一帧往 target 靠近一点，停下后不再占用 CPU。
  const raf = useRef(0);
  const shown = useRef(first);
  function animate() {
    cancelAnimationFrame(raf.current);
    const tick = () => {
      const d = target.current - shown.current;
      shown.current = Math.abs(d) < 0.001 ? target.current : shown.current + d * 0.16;
      setPos(shown.current);
      if (shown.current !== target.current) raf.current = requestAnimationFrame(tick);
    };
    raf.current = requestAnimationFrame(tick);
  }
  useEffect(() => () => cancelAnimationFrame(raf.current), []);

  function go(to: number, snap = true) {
    const clamped = Math.min(last, Math.max(0, to));
    target.current = snap ? Math.round(clamped) : clamped;
    animate();
  }

  // 照片变少（被删）时别停在空的位置上。
  useEffect(() => {
    if (target.current > last) go(last);
  }, [last]);

  useEffect(() => {
    if (paused) return;
    const key = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
      if (e.key === "ArrowLeft") go(target.current - 1);
      if (e.key === "ArrowRight") go(target.current + 1);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [paused, last]);

  // 滚轮在卡片上时用来翻照片；翻到头以后不再拦着，页面照常滚动。滚完停一会儿就对齐到最近的一张。
  useEffect(() => {
    const el = stage.current;
    if (!el) return;
    let timer: ReturnType<typeof setTimeout>;
    const wheel = (e: WheelEvent) => {
      const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
      if ((delta < 0 && target.current <= 0) || (delta > 0 && target.current >= last)) return;
      e.preventDefault();
      go(target.current + delta / 160, false);
      clearTimeout(timer);
      timer = setTimeout(() => go(target.current), 140);
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => {
      el.removeEventListener("wheel", wheel);
      clearTimeout(timer);
    };
  }, [last]);

  // 卡片的大小：手机上占一半多屏幕宽，电脑上最大 300px，高宽比 5:4。
  const phone = width < 600;
  const cw = Math.min(300, Math.max(150, width * (phone ? 0.56 : 0.22)));
  const ch = cw * 1.25;
  const stepPx = cw * (phone ? 0.26 : 0.4);

  function down(e: PointerEvent) {
    drag.current = { x: e.clientX, start: target.current, moved: false };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent) {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    if (Math.abs(dx) > 6) d.moved = true;
    if (d.moved) go(d.start - dx / (stepPx * 1.6), false);
  }
  function up(e: PointerEvent) {
    const d = drag.current;
    drag.current = null;
    if (!d) return;
    if (d.moved) return go(target.current);
    // 没拖动就是点了一下：点中间那张看大图，点别的跳过去。
    const hit = document.elementsFromPoint(e.clientX, e.clientY).find((el) => el instanceof HTMLElement && el.dataset.index);
    const i = hit ? Number((hit as HTMLElement).dataset.index) : -1;
    if (i < 0) return;
    if (i === Math.round(target.current)) onOpen(photos[i]);
    else go(i);
  }

  const center = Math.round(pos);
  useEffect(() => onIndex(Math.round(target.current)), [center]);
  // 外面改了从第几张看（比如看完大图回来）时转到那张。
  useEffect(() => {
    if (start !== Math.round(target.current)) go(start);
  }, [start]);
  const current = photos[Math.min(last, Math.max(0, center))];

  return (
    <div className="cards-wrap stack">
      <div
        ref={stage}
        className="cards-stage"
        style={{ height: ch * 1.25 }}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={() => (drag.current = null)}
      >
        {width > 0 &&
          photos.map((p, i) => {
            const d = i - pos;
            const a = Math.abs(d);
            if (a > visible) return null;
            const s = Math.sign(d);
            const near = Math.min(a, 1);
            // 排开以后的位置：当前这张在正中，前面的往左、后面的往右，越远越往后退，整排保持水平、不倾斜。
            const x = s * (near * cw * (phone ? 0.62 : 0.78) + Math.max(0, a - 1) * stepPx);
            const z = -near * cw * 0.5 - Math.max(0, a - 1) * cw * 0.16;
            // 叠成一摞时每张只往后错开一点点。
            const t = spread;
            const sx = d * 3;
            const tx = sx + (x - sx) * t;
            const ty = -a * 3 * (1 - t);
            const tz = -a * 8 + (z + a * 8) * t;
            return (
              <div
                key={p.id}
                data-index={i}
                className={"card3d" + (a < 0.5 ? " focus" : "")}
                style={{
                  width: cw,
                  height: ch,
                  marginLeft: -cw / 2,
                  marginTop: -ch / 2,
                  transform: `translate3d(${tx}px, ${ty}px, ${tz}px)`,
                  zIndex: 1000 - Math.round(a * 10),
                  viewTransitionName: transitionName(p),
                  opacity: a > visible - 3 ? Math.max(0, (visible - a) / 3) : 1,
                }}
              >
                <img src={urls.photo(p, a < 1.5 ? "preview" : "thumb")} alt="" draggable={false} data-index={i} />
                <Star on={selected.has(p.id)} name={p.name} onToggle={() => onToggle(p)} />
              </div>
            );
          })}
      </div>
      {current && (
        <div className="cards-caption">
          <span>
            {center + 1} / {photos.length}
          </span>
          {photos.length > 1 && (
            <input
              type="range"
              min={0}
              max={last}
              value={center}
              aria-label="拖到第几张"
              onChange={(e) => go(e.target.valueAsNumber)}
            />
          )}
          <span className="muted">点中间的照片看大图</span>
        </div>
      )}
    </div>
  );
}
