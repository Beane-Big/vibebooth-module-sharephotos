import { useEffect, useRef, useState, type PointerEvent } from "react";
import { downloadMinutes, formatBytes, inWeChat, onIOS, type GalleryUrls, type Photo } from "../api";
import { CheckIcon, CloseIcon, DownloadIcon, NextIcon, PrevIcon } from "../icons";

// 上传者在门户里管理相册时多出来的操作：设封面、删照片。
export type Manage = {
  coverId: number;
  onCover: (p: Photo) => void;
  onDelete: (p: Photo) => Promise<void>;
};

// Gallery 是门户和分享页共用的照片墙：缩略图网格、点开看大图、选几张或全部下载原图。
// 下载走后端的总带宽限制，所以按限速估计要几分钟，让家人知道大概要等多久。
export default function Gallery({
  photos,
  urls,
  rateMbps,
  manage,
}: {
  photos: Photo[];
  urls: GalleryUrls;
  rateMbps: number;
  manage?: Manage;
}) {
  const ready = photos.filter((p) => p.status === "ready");
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  // 正在看的照片 id；按 id 而不是序号记，上传者删了前面的照片也不会跳到别的照片。
  const [open, setOpen] = useState<number | null>(null);

  // 照片被删掉以后，从已选里去掉。
  useEffect(() => {
    setSelected((s) => new Set([...s].filter((id) => ready.some((p) => p.id === id))));
  }, [photos]);

  const total = ready.reduce((n, p) => n + p.size, 0);
  const picked = ready.filter((p) => selected.has(p.id));
  const pickedBytes = picked.reduce((n, p) => n + p.size, 0);

  function toggle(p: Photo) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(p.id)) next.delete(p.id);
      else next.add(p.id);
      return next;
    });
  }

  function stopSelecting() {
    setSelecting(false);
    setSelected(new Set());
  }

  // 只选了一张时直接下载那张原图，不打包。
  const pickedUrl = picked.length === 1 ? urls.photo(picked[0], "original") : urls.zip(picked.map((p) => p.id));

  return (
    <div className="stack gallery">
      <div className="gallery-bar">
        <span className="muted">
          {ready.length} 张 · {formatBytes(total)}
        </span>
        {ready.length > 0 && (
          <div className="actions">
            <button className="secondary" onClick={() => (selecting ? stopSelecting() : setSelecting(true))}>
              {selecting ? "取消选择" : "选择"}
            </button>
            {!selecting && !inWeChat && (
              <a className="button secondary" href={urls.zip()} download>
                <DownloadIcon />
                下载全部
              </a>
            )}
          </div>
        )}
      </div>
      {ready.length > 0 && !selecting && !inWeChat && (
        <p className="muted">
          下载全部是一个 zip 压缩包，按现在的网速大约要 {downloadMinutes(total, rateMbps)} 分钟；只要几张的话点“选择”。
        </p>
      )}
      {inWeChat && ready.length > 0 && (
        <p className="hint">
          微信里不能下载压缩包：要一次下载多张，请点右上角“···”，选“在浏览器打开”。单张照片可以点开后看原图，长按保存。
        </p>
      )}

      <ul className="grid">
        {photos.map((p) => {
          if (p.status !== "ready") {
            if (!manage) return null;
            return (
              <li key={p.id} className="tile pending">
                <span className="muted">{p.status === "processing" ? "处理中…" : "处理失败"}</span>
                {p.status === "failed" && (
                  <button className="link" onClick={() => manage.onDelete(p)}>
                    删除
                  </button>
                )}
              </li>
            );
          }
          const on = selected.has(p.id);
          return (
            <li key={p.id} className={"tile" + (on ? " on" : "")}>
              <button
                className="tile-button"
                aria-label={selecting ? `选择 ${p.name}` : `查看 ${p.name}`}
                aria-pressed={selecting ? on : undefined}
                onClick={() => (selecting ? toggle(p) : setOpen(p.id))}
              >
                <img src={urls.photo(p, "thumb")} alt="" loading="lazy" decoding="async" />
              </button>
              {selecting && (
                <span className="check" aria-hidden>
                  {on && <CheckIcon />}
                </span>
              )}
              {manage?.coverId === p.id && <span className="badge">封面</span>}
            </li>
          );
        })}
      </ul>

      {selecting && (
        <div className="select-bar">
          <span className="select-count">
            已选 {picked.length} 张
            {picked.length > 0 && (
              <span className="muted">
                {" "}
                · {formatBytes(pickedBytes)} · 约 {downloadMinutes(pickedBytes, rateMbps)} 分钟
              </span>
            )}
          </span>
          <div className="actions">
            <button
              className="secondary"
              onClick={() => setSelected(picked.length === ready.length ? new Set() : new Set(ready.map((p) => p.id)))}
            >
              {picked.length === ready.length ? "全不选" : "全选"}
            </button>
            {inWeChat ? (
              <span className="muted">请在浏览器里下载</span>
            ) : picked.length > 0 ? (
              <a className="button" href={pickedUrl} download onClick={() => setTimeout(stopSelecting, 0)}>
                下载
              </a>
            ) : (
              <button disabled>下载</button>
            )}
          </div>
        </div>
      )}

      {open !== null && (
        <Lightbox
          photos={ready}
          id={open}
          urls={urls}
          manage={manage}
          onMove={setOpen}
          onClose={() => setOpen(null)}
        />
      )}
    </div>
  );
}

// Lightbox 全屏看大图：左右键或左右滑动换一张，Esc 或右上角关闭。看的是 2048px 的预览图，下载拿原图。
function Lightbox({
  photos,
  id,
  urls,
  manage,
  onMove,
  onClose,
}: {
  photos: Photo[];
  id: number;
  urls: GalleryUrls;
  manage?: Manage;
  onMove: (id: number) => void;
  onClose: () => void;
}) {
  const index = photos.findIndex((p) => p.id === id);
  const photo = photos[index];
  // 微信里点了“看原图”的照片：直接显示原图，长按就能保存。
  const [original, setOriginal] = useState<number | null>(null);
  const [deleting, setDeleting] = useState(false);
  const start = useRef<{ x: number; y: number } | null>(null);

  // 看的那张不在了（比如在别的页签里删了）就关掉。
  useEffect(() => {
    if (!photo) onClose();
  }, [photo]);

  const go = (step: number) => {
    const next = photos[index + step];
    if (next) {
      onMove(next.id);
      setDeleting(false);
    }
  };

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") go(-1);
      if (e.key === "ArrowRight") go(1);
    };
    window.addEventListener("keydown", key);
    // 看大图时后面的页面不跟着滚。
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", key);
      document.body.style.overflow = overflow;
    };
  }, [index, photos]);

  // 提前取好前后两张，翻页时不用等。
  useEffect(() => {
    for (const p of [photos[index - 1], photos[index + 1]]) if (p) new Image().src = urls.photo(p, "preview");
  }, [index, photos]);

  if (!photo) return null;

  function down(e: PointerEvent) {
    start.current = { x: e.clientX, y: e.clientY };
  }

  function up(e: PointerEvent) {
    if (!start.current) return;
    const dx = e.clientX - start.current.x;
    const dy = e.clientY - start.current.y;
    start.current = null;
    if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy)) go(dx < 0 ? 1 : -1);
  }

  const showOriginal = original === photo.id;

  return (
    <div className="lightbox" role="dialog" aria-modal="true" aria-label={photo.name}>
      <div className="lightbox-top">
        <span>
          {index + 1} / {photos.length}
        </span>
        <span className="lightbox-name">{photo.name}</span>
        <button className="icon" aria-label="关闭" onClick={onClose}>
          <CloseIcon />
        </button>
      </div>
      <div className="lightbox-stage" onPointerDown={down} onPointerUp={up} onPointerCancel={() => (start.current = null)}>
        <img
          key={photo.id + (showOriginal ? "-original" : "")}
          src={showOriginal ? urls.photo(photo, "original", true) : urls.photo(photo, "preview")}
          alt={photo.name}
          draggable={false}
        />
        {index > 0 && (
          <button className="lightbox-nav prev" aria-label="上一张" onClick={() => go(-1)}>
            <PrevIcon />
          </button>
        )}
        {index < photos.length - 1 && (
          <button className="lightbox-nav next" aria-label="下一张" onClick={() => go(1)}>
            <NextIcon />
          </button>
        )}
      </div>
      <div className="lightbox-bottom">
        {deleting && manage ? (
          <>
            <span>删除这张照片？分享出去的链接里也会消失。</span>
            <button
              className="danger"
              onClick={() => {
                // 删掉以后接着看后面一张，删的是最后一张就看前面一张。
                const next = photos[index + 1] ?? photos[index - 1];
                if (next) onMove(next.id);
                setDeleting(false);
                manage.onDelete(photo);
              }}
            >
              确认删除
            </button>
            <button className="secondary" onClick={() => setDeleting(false)}>
              不删除
            </button>
          </>
        ) : (
          <>
            <span className="lightbox-size">原图 {formatBytes(photo.size)}</span>
            {inWeChat ? (
              showOriginal ? (
                <span>长按图片保存到手机</span>
              ) : (
                <button onClick={() => setOriginal(photo.id)}>看原图</button>
              )
            ) : onIOS ? (
              <SaveToPhotos key={photo.id} photo={photo} urls={urls} />
            ) : (
              <a className="button" href={urls.photo(photo, "original")} download>
                <DownloadIcon />
                下载原图
              </a>
            )}
            {manage && (
              <>
                <button className="secondary" disabled={manage.coverId === photo.id} onClick={() => manage.onCover(photo)}>
                  {manage.coverId === photo.id ? "已是封面" : "设为封面"}
                </button>
                <button className="secondary" onClick={() => setDeleting(true)}>
                  删除
                </button>
              </>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// SaveToPhotos 是 iPhone 上的“存到相册”：先把原图取下来，再打开系统分享面板选“存储图像”。
// 系统只在点按后的几秒内允许打开分享面板，原图慢的时候取完已经过了，这时让用户再点一次。
function SaveToPhotos({ photo, urls }: { photo: Photo; urls: GalleryUrls }) {
  const [file, setFile] = useState<File | null>(null);
  const [state, setState] = useState<"idle" | "loading" | "again" | "error">("idle");

  async function share(f: File) {
    try {
      await navigator.share({ files: [f] });
      setState("idle");
    } catch (err) {
      // 用户自己关掉分享面板是 AbortError，不用提示。
      setState(err instanceof DOMException && err.name === "NotAllowedError" ? "again" : "idle");
    }
  }

  async function save() {
    if (file) return share(file);
    setState("loading");
    try {
      const res = await fetch(urls.photo(photo, "original"));
      if (!res.ok) throw new Error();
      const f = new File([await res.blob()], photo.name, { type: "image/jpeg" });
      setFile(f);
      await share(f);
    } catch {
      setState("error");
    }
  }

  // 系统不支持分享文件（比如很旧的 iOS）时退回普通下载。
  if (!navigator.canShare?.({ files: [new File([], photo.name, { type: "image/jpeg" })] }))
    return (
      <a className="button" href={urls.photo(photo, "original")} download>
        <DownloadIcon />
        下载原图
      </a>
    );

  return (
    <>
      <button onClick={save} disabled={state === "loading"}>
        {state === "loading" ? "正在取原图…" : "存到相册"}
      </button>
      {state === "again" && <span>原图准备好了，再点一次“存到相册”</span>}
      {state === "error" && <span>没取到原图，请重试</span>}
    </>
  );
}
