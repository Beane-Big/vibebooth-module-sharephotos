import { useEffect, useRef, useState, type DragEvent } from "react";
import { ApiError, formatBytes, uploadPhoto, type Photo } from "../api";
import { UploadIcon } from "../icons";

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

// Uploader 是上传照片的区域：点击选择或把照片拖进来，一次可以选一两百张，排队上传。
// 每张传完就交给后端去掉位置信息、生成预览图；没传成的列出来，可以重试。
export default function Uploader({ albumId, onUploaded }: { albumId: number; onUploaded: (p: Photo) => void }) {
  const [items, setItems] = useState<Item[]>([]);
  const [over, setOver] = useState(false);
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
    const jpegs = picked.filter((f) => /\.jpe?g$/i.test(f.name));
    const skipped = picked.filter((f) => !jpegs.includes(f)).map((f) => f.name);
    setError(skipped.length ? `只能上传 JPEG（.jpg）照片，没有加入：${skipped.join("、")}` : "");
    if (!jpegs.length) return;
    // 上一批都传完了，开始新的一批时把旧的记录清掉。
    const fresh = jpegs.map((file): Item => ({ id: nextId++, file, status: "waiting", progress: 0 }));
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

  const done = items.filter((it) => it.status === "done").length;
  const failed = items.filter((it) => it.status === "failed");
  const working = items.some((it) => it.status === "waiting" || it.status === "uploading");
  const totalBytes = items.reduce((n, it) => n + it.file.size, 0);
  const sentBytes = items.reduce((n, it) => n + it.file.size * (it.status === "done" ? 1 : it.progress), 0);

  // 还在传的时候关页面会中断上传，先提醒一下。
  useEffect(() => {
    if (!working) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [working]);

  function drop(e: DragEvent) {
    e.preventDefault();
    setOver(false);
    add(e.dataTransfer.files);
  }

  return (
    <div className="stack files">
      <label
        className={"drop" + (over ? " over" : "")}
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={drop}
      >
        <input
          className="sr-only"
          type="file"
          accept=".jpg,.jpeg,image/jpeg"
          multiple
          onChange={(e) => {
            add(e.target.files);
            e.target.value = "";
          }}
        />
        <span className="drop-icon">
          <UploadIcon />
        </span>
        <span className="drop-text">
          <span className="drop-name">点击选择照片，或拖到这里</span>
          <span className="muted">Lightroom 导出的 JPEG，一次可以选很多张；上传时会去掉照片里的位置信息</span>
        </span>
      </label>
      {error && <p className="error">{error}</p>}
      {items.length > 0 && (
        <div className="stack upload-status">
          <div className="progress" aria-hidden>
            <span style={{ width: `${totalBytes ? (sentBytes / totalBytes) * 100 : 0}%` }} />
          </div>
          <p className="muted">
            {working
              ? `正在上传：已传完 ${done} / ${items.length} 张，${formatBytes(sentBytes)} / ${formatBytes(totalBytes)}。传完前不要关掉这个页面。`
              : failed.length
                ? `传完了 ${done} 张，${failed.length} 张没传成。`
                : `${done} 张都传完了，预览图生成好以后就能看到。`}
          </p>
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
        </div>
      )}
    </div>
  );
}
