import { useEffect, useRef, useState } from "react";
import { ApiError, formatBytes, uploadPhoto, type Photo } from "../api";
import { CloseIcon, PlusIcon } from "../icons";
import { speedText, useSpeed } from "./speed";

type Item = {
  id: number;
  file: File;
  status: "waiting" | "uploading" | "done" | "failed";
  progress: number; // 0–1
  error?: string;
};

// 同时传两张：一张一张传时每张之间有空档，传太多张又会互相抢带宽，单张都很慢。
const parallel = 2;

let nextId = 1;

// 能上传的格式：手机自己能打开的几种，.hif 是相机存的 HEIF。格式最终由后端按文件内容判断。
const photoExt = /\.(jpe?g|png|webp|heic|heif|hif|gif)$/i;
// 选照片时列出这些格式；写上 image/heic，iPhone 才会传原来的 HEIC，而不是先转成 JPEG。
const photoAccept = ".jpg,.jpeg,.png,.webp,.heic,.heif,.hif,.gif,image/jpeg,image/png,image/webp,image/heic,image/heif,image/gif";

// useUploader 管上传队列：一次可以选一两百张，排队上传，每张传完就交给后端去掉位置信息、生成预览图；
// 没传成的留在列表里，可以重试。相册页右上角的“上传”和拖到页面上的照片都交给它。
export function useUploader(albumId: number, onUploaded: (p: Photo) => void) {
  const [items, setItems] = useState<Item[]>([]);
  const [error, setError] = useState("");
  const queue = useRef<Item[]>([]);
  const active = useRef(0);

  const update = (id: number, patch: Partial<Item>) =>
    setItems((list) => list.map((it) => (it.id === id ? { ...it, ...patch } : it)));

  function pump() {
    while (active.current < parallel && queue.current.length) {
      const it = queue.current.shift()!;
      active.current++;
      update(it.id, { status: "uploading", progress: 0, error: undefined });
      uploadPhoto(albumId, it.file, (progress) => update(it.id, { progress }))
        .then((p) => {
          update(it.id, { status: "done", progress: 1 });
          onUploaded(p);
        })
        .catch((err) => {
          update(it.id, { status: "failed", error: err instanceof Error ? err.message : "上传失败" });
          // 硬盘满了，后面的也传不上去，不用一张张再试。
          if (err instanceof ApiError && err.status === 507) {
            const rest = queue.current.splice(0);
            setItems((list) =>
              list.map((o) => (rest.some((r) => r.id === o.id) ? { ...o, status: "failed", error: err.message } : o)),
            );
          }
        })
        .finally(() => {
          active.current--;
          pump();
        });
    }
  }

  function add(list: FileList | null | undefined) {
    const picked = Array.from(list ?? []);
    const photos = picked.filter((f) => photoExt.test(f.name));
    const skipped = picked.filter((f) => !photos.includes(f)).map((f) => f.name);
    setError(skipped.length ? `只能上传 JPEG、PNG、WebP、HEIC/HEIF、GIF 照片，没有加入：${skipped.join("、")}` : "");
    if (!photos.length) return;
    // 上一批都传完了，开始新的一批时把旧的记录清掉。
    const fresh = photos.map((file): Item => ({ id: nextId++, file, status: "waiting", progress: 0 }));
    setItems((old) => [...(old.every((o) => o.status === "done") ? [] : old), ...fresh]);
    queue.current.push(...fresh);
    pump();
  }

  function retry() {
    const failed = items.filter((it) => it.status === "failed");
    setItems((list) => list.map((it) => (it.status === "failed" ? { ...it, status: "waiting", error: undefined } : it)));
    queue.current.push(...failed);
    pump();
  }

  const working = items.some((it) => it.status === "waiting" || it.status === "uploading");

  // 还在传的时候关页面会中断上传，先提醒一下。
  useEffect(() => {
    if (!working) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [working]);

  return { items, error, working, add, retry, clear: () => {
      setItems([]);
      setError("");
    },
  };
}

export type Uploads = ReturnType<typeof useUploader>;

// UploadButton 是右上角的“上传”：点了打开选照片。
export function UploadButton({ uploads }: { uploads: Uploads }) {
  return (
    <label className="pill">
      <input
        className="sr-only"
        type="file"
        aria-label="上传照片"
        accept={photoAccept}
        multiple
        onChange={(e) => {
          uploads.add(e.target.files);
          e.target.value = "";
        }}
      />
      <PlusIcon />
      <span className="pill-text">上传</span>
    </label>
  );
}

// UploadStatus 在相册页上面显示上传进度和没传成的照片，没在上传时什么都不显示。
export function UploadStatus({ uploads }: { uploads: Uploads }) {
  const { items, error, working, retry } = uploads;
  const done = items.filter((it) => it.status === "done").length;
  const failed = items.filter((it) => it.status === "failed");
  const totalBytes = items.reduce((n, it) => n + it.file.size, 0);
  const sentBytes = items.reduce((n, it) => n + it.file.size * (it.status === "done" ? 1 : it.progress), 0);
  const speed = useSpeed(sentBytes, working);
  if (!items.length && !error) return null;
  return (
    <section className="card stack upload-status">
      {error && <p className="error">{error}</p>}
      {items.length > 0 && (
        <>
          <div className="progress" aria-hidden>
            <span style={{ width: `${totalBytes ? (sentBytes / totalBytes) * 100 : 0}%` }} />
          </div>
          <div className="actions">
            <p className="muted grow">
              {working
                ? `正在上传：已传完 ${done} / ${items.length} 张，${formatBytes(sentBytes)} / ${formatBytes(totalBytes)}${speed > 0 ? `，${speedText(speed, totalBytes - sentBytes)}` : ""}。传完前不要关掉这个页面。`
                : failed.length
                  ? `传完了 ${done} 张，${failed.length} 张没传成。`
                  : `${done} 张都传完了，预览图生成好以后就能看到。`}
            </p>
            {!working && (
              <button className="icon" aria-label="关掉上传进度" onClick={uploads.clear}>
                <CloseIcon />
              </button>
            )}
          </div>
          {failed.length > 0 && (
            <>
              <ul className="failed-list">
                {failed.map((it) => (
                  <li key={it.id}>
                    <span className="drop-name">{it.file.name}</span>
                    <span className="error">{it.error}</span>
                  </li>
                ))}
              </ul>
              {!working && (
                <div>
                  <button className="secondary" onClick={retry}>
                    重试没传成的 {failed.length} 张
                  </button>
                </div>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}

// useDropPhotos 让照片可以直接拖到整个页面上上传，拖着经过时页面显示一个提示框。
export function useDropPhotos(uploads: Uploads, enabled: boolean) {
  const [over, setOver] = useState(false);
  const latest = useRef(uploads);
  latest.current = uploads;
  useEffect(() => {
    if (!enabled) return;
    let depth = 0;
    const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes("Files");
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth++;
      setOver(true);
    };
    const leave = () => {
      depth = Math.max(0, depth - 1);
      if (!depth) setOver(false);
    };
    const over = (e: DragEvent) => hasFiles(e) && e.preventDefault();
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      setOver(false);
      latest.current.add(e.dataTransfer?.files);
    };
    window.addEventListener("dragenter", enter);
    window.addEventListener("dragleave", leave);
    window.addEventListener("dragover", over);
    window.addEventListener("drop", drop);
    return () => {
      window.removeEventListener("dragenter", enter);
      window.removeEventListener("dragleave", leave);
      window.removeEventListener("dragover", over);
      window.removeEventListener("drop", drop);
    };
  }, [enabled]);
  return over;
}
