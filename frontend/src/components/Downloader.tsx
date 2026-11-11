import { useEffect, useRef, useState } from "react";
import { formatBytes, onIOS, photoType, type GalleryUrls, type Photo } from "../api";
import { CloseIcon, PhotoIcon } from "../icons";
import { speedText } from "./speed";

// 批量下载原图：一张一张下（同时两张），每张是单独的 JPEG，不再打成 zip。
// 下载列表里能看到每张的进度；网络断了自动等恢复、从断开的地方接着下，关掉页面再打开也能接着下没下完的。
//
// 下好的照片怎么存，看浏览器能做什么：
// - 电脑上的 Chrome、Edge：先选一个文件夹，照片直接写进去，不占内存；接着下时文件夹里已经有的会跳过。
// - iPhone：浏览器不能直接存进相册，每下好 10 张点一次“存到相册”，在系统分享面板里选“存储图像”。
// - 其他浏览器：每张下好后照常存到“下载”里（浏览器可能会问一次是否允许下载多个文件）。

type Status =
  | "waiting" // 排队
  | "downloading"
  | "retrying" // 出错了，过几秒重试
  | "offline" // 网络断了，等恢复
  | "ready" // iPhone 上已经下好，等着存到相册
  | "done"
  | "skipped" // 文件夹里已经有了
  | "failed"
  | "canceled";

export type DownloadItem = {
  id: number;
  name: string;
  size: number;
  status: Status;
  received: number;
  error?: string;
  attempt?: number; // 第几次重试
  delay?: number; // 几秒后重试
};

type Mode = "folder" | "file" | "share";

const parallel = 2;
// 一直没有进展时最多重试几次；有进展就重新计数，所以下得慢、偶尔断一下不会算失败。
const maxTries = 6;
// iPhone 上一次最多攒几张等着存到相册，太多了内存吃不消。
const shareBatch = 10;
const finished: Status[] = ["done", "skipped", "canceled"];

// Fatal 是重试也没用的错误；stopAll 表示后面的也别下了，比如链接失效、磁盘满了。
class Fatal extends Error {
  constructor(
    message: string,
    public stopAll = false,
  ) {
    super(message);
  }
}

// Chrome、Edge 有、类型库里还没有的选文件夹接口。
type DirectoryPicker = (opts: { id?: string; mode?: "readwrite"; startIn?: "downloads" }) => Promise<FileSystemDirectoryHandle>;
const pickDirectory = (window as unknown as { showDirectoryPicker?: DirectoryPicker }).showDirectoryPicker;

function chooseMode(): Mode {
  if (onIOS && navigator.canShare?.({ files: [new File([], "a.jpg", { type: "image/jpeg" })] })) return "share";
  if (pickDirectory && window.isSecureContext) return "folder";
  return "file";
}

// Sink 是一张照片写到哪里：文件夹里的文件，或者内存。
type Sink = {
  write: (chunk: Uint8Array) => Promise<void>;
  restart: () => Promise<void>; // 服务器不支持续传时从头再写
  close: () => Promise<File | null>;
  abort: () => Promise<void>;
};

// 同名照片存成“名字 (1).jpg”。
function withSuffix(name: string, n: number) {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? `${name.slice(0, dot)} (${n})${name.slice(dot)}` : `${name} (${n})`;
}

// folderSink 在文件夹里建文件。同名、同样大小的文件已经在了，说明上次下好了，返回 "exists" 跳过；
// 同名但不一样的文件不覆盖，换个名字存。
async function folderSink(dir: FileSystemDirectoryHandle, name: string, size: number, used: Set<string>): Promise<Sink | "exists"> {
  // 同时下两张，名字要先占上，免得两张同名的照片写进同一个文件。
  let candidate = name;
  for (let n = 1; ; candidate = withSuffix(name, n++)) {
    if (used.has(candidate)) continue;
    used.add(candidate);
    const existing = await dir.getFileHandle(candidate).catch(() => null);
    if (!existing) break;
    const f = await existing.getFile();
    if (f.size === size) return "exists";
    // 大小是 0 的是上次中断时留下的空文件，接着用。
    if (f.size === 0) break;
  }
  const file = await dir.getFileHandle(candidate, { create: true });
  const w = await file.createWritable();
  return {
    write: (c) => w.write(c as Uint8Array<ArrayBuffer>),
    restart: async () => {
      await w.truncate(0);
      await w.seek(0);
    },
    close: async () => {
      await w.close();
      return null;
    },
    // 没写完的内容浏览器会丢掉，只剩一个空文件，删掉它。
    abort: async () => {
      used.delete(candidate);
      await w.abort().catch(() => {});
      await dir.removeEntry(candidate).catch(() => {});
    },
  };
}

function memorySink(name: string): Sink {
  let parts: Uint8Array<ArrayBuffer>[] = [];
  return {
    write: async (c) => void parts.push(c as Uint8Array<ArrayBuffer>),
    restart: async () => void (parts = []),
    close: async () => new File(parts, name, { type: photoType(name) }),
    abort: async () => void (parts = []),
  };
}

// saveFile 把内存里下好的照片交给浏览器存到“下载”里。
function saveFile(file: File) {
  const url = URL.createObjectURL(file);
  const a = document.createElement("a");
  a.href = url;
  a.download = file.name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => (clearTimeout(t), reject(signal.reason)), { once: true });
  });

const online = (signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    if (navigator.onLine) return resolve();
    window.addEventListener("online", () => resolve(), { once: true });
    signal.addEventListener("abort", () => reject(signal.reason), { once: true });
  });

function diskMessage(err: unknown) {
  if (err instanceof DOMException && err.name === "QuotaExceededError") return "磁盘满了，存不下";
  if (err instanceof DOMException && err.name === "NotAllowedError") return "没有写入这个文件夹的权限，请重新选择文件夹";
  return "写不进文件夹，请检查磁盘空间后重试";
}

// transfer 下载一张原图写进 sink。断了就用 Range 从断开的地方接着下；服务器没按续传返回时从头写。
async function transfer(
  url: string,
  size: number,
  sink: Sink,
  signal: AbortSignal,
  onBytes: (got: number) => void,
  onWait: (patch: Partial<DownloadItem>) => void,
) {
  let got = 0;
  let tries = 0;
  for (;;) {
    try {
      const res = await fetch(url, { signal, cache: "no-store", headers: got ? { Range: `bytes=${got}-` } : undefined });
      if (res.status === 401 || res.status === 403) throw new Fatal("链接失效了，或者提取码换了。刷新页面后再下载", true);
      if (res.status === 404) throw new Fatal("这张照片已经被删掉了");
      if (res.status === 416) {
        await sink.restart();
        got = 0;
        throw new Error("续传的位置不对，从头下");
      }
      if (!res.ok || !res.body) throw new Error(`服务器出错（${res.status}）`);
      if (got > 0 && res.status !== 206) {
        await sink.restart();
        got = 0;
      }
      onWait({ status: "downloading", attempt: undefined, delay: undefined });
      const reader = res.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        try {
          await sink.write(value);
        } catch (err) {
          throw new Fatal(diskMessage(err), true);
        }
        got += value.length;
        tries = 0;
        onBytes(got);
      }
      if (got === size) return;
      if (got > size) {
        await sink.restart();
        got = 0;
      }
      throw new Error("没有下完整");
    } catch (err) {
      if (signal.aborted || err instanceof Fatal) throw err;
      if (++tries >= maxTries) throw new Fatal("网络一直不好，没下完，可以稍后重试");
      if (!navigator.onLine) {
        onWait({ status: "offline" });
        await online(signal);
      }
      const delay = Math.min(30, 2 ** tries);
      onWait({ status: "retrying", attempt: tries, delay });
      await sleep(delay * 1000, signal);
    }
  }
}

// useDownloader 管下载列表。scope 区分不同的相册，用来记住关页面时还没下完的照片。
export function useDownloader(urls: GalleryUrls, scope: string, photos: Photo[]) {
  const key = "sharephotos:downloads:" + scope;
  const [items, setItems] = useState<DownloadItem[]>([]);
  const [mode, setMode] = useState<Mode | null>(null);
  const [folder, setFolder] = useState("");
  const [paused, setPaused] = useState(false);
  const [stopped, setStopped] = useState(""); // 让整个队列停下来的错误
  const [unsaved, setUnsaved] = useState(0); // iPhone 上下好了还没存到相册的张数
  const [shareHint, setShareHint] = useState("");
  const [sharing, setSharing] = useState(false); // 分享面板正在打开或开着
  // 上次关页面时没下完的照片 id。
  const [leftover, setLeftover] = useState<number[]>(() => {
    try {
      const v = JSON.parse(localStorage.getItem(key) ?? "[]");
      return Array.isArray(v) ? v : [];
    } catch {
      return [];
    }
  });

  // 异步下载过程中要用到的最新值放在 ref 里。
  const queue = useRef<number[]>([]);
  const active = useRef(new Map<number, AbortController>());
  const info = useRef(new Map<number, Photo>());
  const modeRef = useRef<Mode | null>(null);
  const dir = useRef<FileSystemDirectoryHandle | null>(null);
  const used = useRef(new Set<string>());
  const pausedRef = useRef(false);
  const stoppedRef = useRef("");
  const ready = useRef<{ id: number; file: File }[]>([]);
  // 进度：每张收到多少字节，界面每 0.3 秒刷新一次，不必每收到一块就重画。
  const received = useRef(new Map<number, number>());
  // 真正从网上收到的字节数，算速度用；文件夹里已有而跳过的不算。
  const net = useRef(0);
  const samples = useRef<[number, number][]>([]);
  const [, setTick] = useState(0);

  const update = (id: number, patch: Partial<DownloadItem>) =>
    setItems((list) => list.map((it) => (it.id === id ? { ...it, ...patch } : it)));

  const working = items.some((it) => ["waiting", "downloading", "retrying", "offline"].includes(it.status)) && !paused && !stopped;

  useEffect(() => {
    if (!working) return;
    const t = setInterval(() => {
      const now = Date.now();
      samples.current = [...samples.current.filter(([t]) => now - t < 8000), [now, net.current]];
      setItems((list) =>
        list.map((it) => (received.current.has(it.id) && it.received !== received.current.get(it.id) ? { ...it, received: received.current.get(it.id)! } : it)),
      );
      setTick((n) => n + 1);
    }, 300);
    return () => clearInterval(t);
  }, [working]);

  // 还没下完的照片记在浏览器里，关掉页面再打开时可以接着下。
  useEffect(() => {
    if (!items.length) return;
    const left = items.filter((it) => !finished.includes(it.status)).map((it) => it.id);
    try {
      if (left.length) localStorage.setItem(key, JSON.stringify(left));
      else localStorage.removeItem(key);
    } catch {
      // 存不了只是关页面后不能接着下。
    }
  }, [items]);

  // 正在下的时候关页面会中断，先提醒一下。
  useEffect(() => {
    if (!working && !unsaved) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [working, unsaved]);

  useEffect(() => () => active.current.forEach((c) => c.abort("cancel")), []);

  function pump() {
    while (
      !pausedRef.current &&
      !stoppedRef.current &&
      active.current.size < parallel &&
      queue.current.length &&
      (modeRef.current !== "share" || ready.current.length + active.current.size < shareBatch)
    ) {
      run(queue.current.shift()!);
    }
  }

  async function run(id: number) {
    const p = info.current.get(id)!;
    const ctrl = new AbortController();
    active.current.set(id, ctrl);
    received.current.set(id, 0);
    update(id, { status: "downloading", received: 0, error: undefined, attempt: undefined, delay: undefined });
    let sink: Sink | null = null;
    try {
      if (modeRef.current === "folder") {
        let s: Sink | "exists";
        try {
          s = await folderSink(dir.current!, p.name, p.size, used.current);
        } catch (err) {
          throw new Fatal(diskMessage(err), true);
        }
        if (s === "exists") {
          received.current.set(id, p.size);
          update(id, { status: "skipped", received: p.size });
          return;
        }
        sink = s;
      } else sink = memorySink(p.name);
      await transfer(
        urls.photo(p, "original"),
        p.size,
        sink,
        ctrl.signal,
        (got) => {
          net.current += Math.max(0, got - (received.current.get(id) ?? 0));
          received.current.set(id, got);
        },
        (patch) => update(id, patch),
      );
      const file = await sink.close();
      sink = null;
      received.current.set(id, p.size);
      if (modeRef.current === "share" && file) {
        ready.current.push({ id, file });
        setUnsaved(ready.current.length);
        update(id, { status: "ready", received: p.size });
      } else {
        if (file) saveFile(file);
        update(id, { status: "done", received: p.size });
      }
    } catch (err) {
      await sink?.abort().catch(() => {});
      received.current.set(id, 0);
      if (ctrl.signal.aborted) {
        // 暂停的放回队列最前面，取消的不管了。
        if (ctrl.signal.reason === "pause") {
          queue.current.unshift(id);
          update(id, { status: "waiting", received: 0, attempt: undefined, delay: undefined });
        } else update(id, { status: "canceled", received: 0 });
      } else {
        const message = err instanceof Error ? err.message : "没下成";
        update(id, { status: "failed", received: 0, error: message, attempt: undefined, delay: undefined });
        if (err instanceof Fatal && err.stopAll) {
          stoppedRef.current = message;
          setStopped(message);
          active.current.forEach((c) => c.abort("pause"));
        }
      }
    } finally {
      active.current.delete(id);
      pump();
    }
  }

  // add 把照片加进下载列表。第一次下载时决定怎么存；电脑上要先选文件夹，必须在点按里调用。
  async function add(list: Photo[]) {
    if (!list.length) return;
    if (!modeRef.current) {
      let m = chooseMode();
      if (m === "folder") {
        try {
          dir.current = await pickDirectory!({ id: "sharephotos", mode: "readwrite", startIn: "downloads" });
          setFolder(dir.current.name);
        } catch (err) {
          // 用户关掉了选文件夹的窗口就不下载；别的原因（比如浏览器不让）改成普通下载。
          if (err instanceof DOMException && err.name === "AbortError") return;
          m = "file";
        }
      }
      modeRef.current = m;
      setMode(m);
    }
    setLeftover([]);
    const busy = new Set(items.filter((it) => !finished.includes(it.status) && it.status !== "failed").map((it) => it.id));
    const fresh = list.filter((p) => !busy.has(p.id));
    for (const p of fresh) info.current.set(p.id, p);
    const ids = new Set(fresh.map((p) => p.id));
    // 上一批都完了，开始新的一批时把旧的记录清掉。
    setItems((old) => [
      ...(old.every((o) => finished.includes(o.status)) ? [] : old.filter((o) => !ids.has(o.id))),
      ...fresh.map((p): DownloadItem => ({ id: p.id, name: p.name, size: p.size, status: "waiting", received: 0 })),
    ]);
    queue.current = [...queue.current.filter((id) => !ids.has(id)), ...fresh.map((p) => p.id)];
    if (items.every((o) => finished.includes(o.status))) {
      received.current.clear();
      samples.current = [];
    }
    for (const p of fresh) received.current.set(p.id, 0);
    pump();
  }

  function pause() {
    pausedRef.current = true;
    setPaused(true);
    active.current.forEach((c) => c.abort("pause"));
  }

  function resume() {
    pausedRef.current = false;
    setPaused(false);
    stoppedRef.current = "";
    setStopped("");
    samples.current = [];
    pump();
  }

  function cancel(id: number) {
    const c = active.current.get(id);
    if (c) return c.abort("cancel");
    queue.current = queue.current.filter((q) => q !== id);
    update(id, { status: "canceled" });
  }

  function cancelAll() {
    queue.current = [];
    active.current.forEach((c) => c.abort("cancel"));
    setItems((list) => list.map((it) => (["waiting", "failed", "retrying", "offline"].includes(it.status) ? { ...it, status: "canceled" } : it)));
  }

  function retry() {
    const failed = items.filter((it) => it.status === "failed" || it.status === "canceled").map((it) => it.id);
    setItems((list) => list.map((it) => (failed.includes(it.id) ? { ...it, status: "waiting", error: undefined } : it)));
    queue.current.push(...failed);
    resume();
  }

  // iPhone：把攒好的几张交给系统分享面板，用户选“存储图像”。
  // 照片多时系统要准备一两秒才弹出面板，这期间按钮显示“正在打开”并且不能再点；再点一次会被系统当成重复分享而失败。
  async function saveToPhotos() {
    if (sharing) return;
    const batch = ready.current;
    setShareHint("");
    setSharing(true);
    try {
      await navigator.share({ files: batch.map((b) => b.file) });
      ready.current = ready.current.filter((r) => !batch.includes(r));
      setUnsaved(ready.current.length);
      const saved = new Set(batch.map((b) => b.id));
      setItems((list) => list.map((it) => (saved.has(it.id) ? { ...it, status: "done" } : it)));
      pump();
    } catch (err) {
      // 自己关掉分享面板不用提示；照片留着，可以再点。
      if (err instanceof DOMException && err.name === "NotAllowedError") setShareHint("没打开分享面板，请再点一次");
    } finally {
      setSharing(false);
    }
  }

  function clear() {
    if (working) return;
    setItems([]);
    ready.current = [];
    setUnsaved(0);
  }

  // 速度按最近几秒算，剩余时间按速度估计。
  const s = samples.current;
  const speed = s.length > 1 ? ((s[s.length - 1][1] - s[0][1]) * 1000) / Math.max(1, s[s.length - 1][0] - s[0][0]) : 0;
  const left = leftover.map((id) => photos.find((p) => p.id === id && p.status === "ready")).filter((p): p is Photo => !!p);

  return {
    items,
    mode,
    folder,
    paused,
    stopped,
    working,
    speed,
    unsaved,
    shareHint,
    sharing,
    leftover: left,
    add,
    pause,
    resume,
    cancel,
    cancelAll,
    retry,
    saveToPhotos,
    clear,
    forget: () => {
      setLeftover([]);
      try {
        localStorage.removeItem(key);
      } catch {
        // 没关系
      }
    },
  };
}

export type Downloads = ReturnType<typeof useDownloader>;

const statusText = (it: DownloadItem) => {
  switch (it.status) {
    case "waiting":
      return "等待";
    case "downloading":
      return it.size ? `${Math.floor((it.received / it.size) * 100)}%` : "下载中";
    case "retrying":
      return `断了，${it.delay} 秒后接着下（第 ${it.attempt} 次）`;
    case "offline":
      return "网络断了，恢复后接着下";
    case "ready":
      return "下好了，等存到相册";
    case "done":
      return "已保存";
    case "skipped":
      return "文件夹里已经有了";
    case "failed":
      return it.error ?? "没下成";
    case "canceled":
      return "已取消";
  }
};

// DownloadPanel 是贴在屏幕底部的下载列表：上面是总进度、速度和还要多久，展开后是每张照片的状态。
// 没有下载时，如果上次有没下完的，提示接着下。
export function DownloadPanel({ downloads: d }: { downloads: Downloads }) {
  // 手机上默认只显示总进度，免得列表挡住照片。
  const [open, setOpen] = useState(() => !matchMedia("(max-width: 600px)").matches);
  const { items } = d;

  if (!items.length) {
    if (!d.leftover.length) return null;
    return (
      <section className="download-panel">
        <div className="download-head">
          <span className="grow">上次还有 {d.leftover.length} 张没下完</span>
          <button onClick={() => d.add(d.leftover)}>接着下载</button>
          <button className="secondary" onClick={d.forget}>
            不用了
          </button>
        </div>
      </section>
    );
  }

  const count = (...s: Status[]) => items.filter((it) => s.includes(it.status)).length;
  const counted = items.filter((it) => it.status !== "canceled");
  const total = counted.reduce((n, it) => n + it.size, 0);
  const got = counted.reduce((n, it) => n + it.received, 0);
  const finishedCount = count("done", "skipped", "ready");
  const failed = count("failed");
  const canceled = count("canceled");
  const waiting = d.working || d.paused || d.stopped;

  const title = d.stopped
    ? "下载停了"
    : d.paused
      ? "已暂停"
      : d.unsaved >= shareBatch
        ? `下好 ${d.unsaved} 张，存到相册后接着下`
        : d.working
        ? `正在下载 ${finishedCount} / ${counted.length} 张`
        : failed
          ? `下完 ${finishedCount} 张，${failed} 张没下成`
          : `${finishedCount} 张都下完了`;

  return (
    <section className="download-panel" aria-label="下载列表">
      <div className="download-head">
        <div className="grow download-summary">
          <strong>{title}</strong>
          <span className="muted">
            {formatBytes(got)} / {formatBytes(total)}
            {d.working && d.unsaved < shareBatch && d.speed > 0 && ` · ${speedText(d.speed, total - got)}`}
          </span>
        </div>
        {d.working && (
          <button className="secondary" onClick={d.pause}>
            暂停
          </button>
        )}
        {(d.paused || d.stopped) && <button onClick={d.resume}>继续</button>}
        <button className="secondary" onClick={() => setOpen(!open)} aria-expanded={open}>
          {open ? "收起" : "查看"}
        </button>
        {!waiting && !d.unsaved && (
          <button className="icon" aria-label="关掉下载列表" onClick={d.clear}>
            <CloseIcon />
          </button>
        )}
      </div>
      <div className="progress" aria-hidden>
        <span style={{ width: `${total ? (got / total) * 100 : 0}%` }} />
      </div>
      {d.stopped && <p className="error">{d.stopped}</p>}
      {open && (
        <>
          <p className="muted">
            {d.mode === "folder"
              ? `存到文件夹「${d.folder}」。中途关掉页面也没关系，再下载时选同一个文件夹，已经下好的会跳过。`
              : d.mode === "share"
                ? "下载中不要锁屏或切到别的应用，不然会暂停。"
                : "照片存到浏览器的“下载”里。浏览器问是否允许下载多个文件时，请选允许。"}
          </p>
          <ul className="download-list">
            {items.map((it) => (
              <li key={it.id} className={"download-row " + it.status}>
                <span className="drop-name">{it.name}</span>
                <span className="muted download-size">{formatBytes(it.size)}</span>
                <span className={"download-status" + (it.status === "failed" ? " error" : "")}>{statusText(it)}</span>
                {["waiting", "downloading", "retrying", "offline"].includes(it.status) && (
                  <button className="link" onClick={() => d.cancel(it.id)}>
                    取消
                  </button>
                )}
                {it.status === "downloading" && (
                  <span className="progress download-row-progress" aria-hidden>
                    <span style={{ width: `${it.size ? (it.received / it.size) * 100 : 0}%` }} />
                  </span>
                )}
              </li>
            ))}
          </ul>
          <div className="actions">
            {(failed > 0 || canceled > 0) && !d.working && (
              <button className="secondary" onClick={d.retry}>
                重新下载没下成的 {failed + canceled} 张
              </button>
            )}
            {waiting && (
              <button className="link" onClick={d.cancelAll}>
                全部取消
              </button>
            )}
          </div>
        </>
      )}
      {/* 放在面板最下面：面板贴着屏幕底部，最下面这一行不会因为上面的文字变化而跳动，点的时候不会点空。 */}
      {d.mode === "share" && d.unsaved > 0 && (
        <div className="save-photos">
          <button className="save-photos-button" onClick={d.saveToPhotos} disabled={d.sharing}>
            <PhotoIcon />
            {d.sharing ? "正在打开…" : `存到相册（${d.unsaved} 张）`}
          </button>
          {d.shareHint && <span className="muted">{d.shareHint}</span>}
        </div>
      )}
    </section>
  );
}
