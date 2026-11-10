import { useEffect, useState, type FormEvent } from "react";
import { ApiError, formatDate, shareUrls, type Photo } from "../api";
import Gallery from "../components/Gallery";

// 分享页看到的相册：没输提取码时没有 photos。
type ShareInfo = {
  title: string;
  note: string;
  ownerName: string;
  expiresAt: string;
  count: number;
  totalBytes: number;
  needCode: boolean;
  cover: number;
  rateMbps: number;
  photos?: Photo[];
};

// 网址是 /apps/sharephotos/s/<令牌>。
const token = location.pathname.match(/^\/apps\/sharephotos\/s\/([\w-]+)/)?.[1] ?? "";
const base = `/api/sharephotos/s/${token}`;

// 分享页的接口不需要登录，401 表示还没输提取码，不能像门户里那样跳去登录。
async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(base + path, { ...init, headers: { "Content-Type": "application/json" } });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body.error ?? "打开失败，请稍后再试");
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

// ShareApp 是家人打开分享链接看到的页面：相册名称和说明，需要时先输提取码，然后是照片墙。
export default function ShareApp() {
  const [info, setInfo] = useState<ShareInfo | null>(null);
  const [error, setError] = useState("");

  function load() {
    call<ShareInfo>("")
      .then((i) => {
        setInfo(i);
        document.title = i.title;
      })
      .catch((err) => setError(err.message));
  }

  useEffect(() => {
    if (token) load();
    else setError("链接不完整，请把分享的人发来的整段链接复制到浏览器里打开");
  }, []);

  if (!info)
    return (
      <main className="content share stack">
        {error ? <p className="card error">{error}</p> : <p className="muted">加载中…</p>}
      </main>
    );

  const urls = shareUrls(token);
  const locked = !info.photos;
  return (
    <main className="content wide share stack">
      {!locked && info.cover > 0 && (
        <div className="hero">
          {/* 手机上封面是隐藏的，lazy 让隐藏时不去下载这张图。 */}
          <img src={urls.photo({ id: info.cover }, "preview")} alt="" loading="lazy" />
        </div>
      )}
      {locked ? (
        <>
          <section className="album-head">
            <h1 className="page-title">{info.title}</h1>
            <span className="muted meta">
              {[info.ownerName && `${info.ownerName} 分享`, `${info.count} 张`, `${formatDate(info.expiresAt)}前有效`]
                .filter(Boolean)
                .map((text, i) => (
                  <span key={i}>{text}</span>
                ))}
            </span>
            {info.note && <p className="note">{info.note}</p>}
          </section>
          <Unlock onDone={load} />
        </>
      ) : (
        <Gallery
          photos={info.photos!}
          urls={urls}
          rateMbps={info.rateMbps}
          title={<h1 className="page-title">{info.title}</h1>}
          meta={[info.ownerName && `${info.ownerName} 分享`, `${formatDate(info.expiresAt)}前有效`]}
          empty={<p className="card muted">相册里还没有照片。</p>}
        >
          {info.note && <p className="note">{info.note}</p>}
        </Gallery>
      )}
      {!locked && info.photos!.length > 0 && (
        <p className="muted footnote">
          {formatDate(info.expiresAt, true)}以后链接失效，喜欢的照片请及时保存。
        </p>
      )}
    </main>
  );
}

// Unlock 是输提取码的地方，输对以后后端发一个只对这个链接有效的 Cookie。
function Unlock({ onDone }: { onDone: () => void }) {
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await call("/unlock", { method: "POST", body: JSON.stringify({ code: code.trim() }) });
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : "打开失败");
      setBusy(false);
    }
  }

  return (
    <form className="card stack unlock" onSubmit={submit}>
      <h1>请输入提取码</h1>
      <p className="muted">提取码是 4 位数字，和链接一起发给你的。</p>
      <div className="actions">
        <input
          value={code}
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={4}
          autoFocus
          placeholder="4 位数字"
          aria-label="提取码"
          onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
        />
        <button type="submit" disabled={busy || code.length !== 4}>
          {busy ? "打开中…" : "打开相册"}
        </button>
      </div>
      {error && <p className="error">{error}</p>}
    </form>
  );
}
