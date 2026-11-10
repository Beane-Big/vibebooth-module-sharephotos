import { useEffect, useState, type FormEvent } from "react";
import {
  albumPath,
  albumUrls,
  api,
  copyText,
  expiryText,
  formatBytes,
  formatDate,
  shareLink,
  type Album as AlbumData,
  type AlbumDetail,
  type AlbumInput,
  type Photo,
} from "../api";
import Gallery from "../components/Gallery";
import { ImportButton, UploadStatus, useDropPhotos, useUploader } from "../components/Uploader";
import { BackIcon, LinkIcon, SettingsIcon } from "../icons";

// 一个相册的页面。所有能看的人都能看照片、下载原图；上传者本人还能上传照片。
// 分享、续期、改信息和删除放在相册的设置页（settings 为 true，网址 /a/<id>/settings/），从右上角的“设置”进入。
export default function Album({ id, settings = false }: { id: number; settings?: boolean }) {
  const [detail, setDetail] = useState<AlbumDetail | null>(null);
  const [error, setError] = useState("");

  function load() {
    return api
      .album(id)
      .then(setDetail)
      .catch((err) => setError(err.message));
  }

  useEffect(() => {
    load();
  }, [id]);

  // 有照片在生成预览图时每隔几秒刷新一次，生成好的就显示出来。
  const processing = detail?.photos.some((p) => p.status === "processing");
  useEffect(() => {
    if (!processing) return;
    const timer = setInterval(load, 3000);
    return () => clearInterval(timer);
  }, [processing]);

  useEffect(() => {
    if (detail) document.title = `${detail.album.title} · 照片分享`;
  }, [detail?.album.title]);

  // 修改相册，成功返回 true；失败时把错误写到 setErr 里。
  async function save(input: AlbumInput, setErr: (msg: string) => void) {
    setErr("");
    try {
      setDetail(await api.updateAlbum(id, input));
      return true;
    } catch (err) {
      setErr(err instanceof Error ? err.message : "保存失败");
      return false;
    }
  }

  function uploaded(p: Photo) {
    setDetail((d) => d && { ...d, photos: [...d.photos, p], album: { ...d.album, count: d.album.count + 1 } });
  }

  const uploads = useUploader(id, uploaded);
  const dropping = useDropPhotos(uploads, !settings && !!detail?.album.mine);
  const [copied, setCopied] = useState("");

  async function copyShare(a: AlbumData) {
    const ok = await copyText(shareMessage(a));
    setCopied(ok ? "ok" : "failed");
    setTimeout(() => setCopied(""), ok ? 2500 : 6000);
  }

  async function removePhoto(p: Photo) {
    setError("");
    try {
      await api.deletePhoto(id, p.id);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "删除失败");
    }
  }

  const back = settings ? (
    <a className="pill" href={albumPath(id)}>
      <BackIcon />
      返回相册
    </a>
  ) : (
    <a className="pill back" href="/apps/sharephotos/" aria-label="全部相册" title="全部相册">
      <BackIcon />
    </a>
  );

  if (!detail)
    return (
      <main className="content stack">
        <div className="page-head">{back}</div>
        {error ? <p className="error">{error}</p> : <p className="muted">加载中…</p>}
      </main>
    );

  const a = detail.album;
  // 不是上传者本人打开设置页时回到相册（后端也不让别人改）。
  if (settings && !a.mine) {
    location.replace(albumPath(id));
    return null;
  }
  if (settings)
    return (
      <main className="content stack">
        <div className="page-head">
          {back}
          <h1 className="page-title">相册设置</h1>
        </div>
        <section className="card stack">
          <SharePanel album={a} save={save} />
          <InfoPanel album={a} save={save} />
        </section>
      </main>
    );
  return (
    <main className="content wide stack">
      {dropping && <div className="drop-overlay">松开就导入这些照片</div>}
      <Gallery
        photos={detail.photos}
        urls={albumUrls(a.id)}
        rateMbps={detail.rateMbps}
        manage={a.mine ? { coverId: a.cover, onCover: (p) => save({ coverId: p.id }, setError), onDelete: removePhoto } : undefined}
        title={
          <>
            {back}
            <h1 className="page-title">{a.title}</h1>
          </>
        }
        actions={
          a.mine && (
            <>
              <button className="pill" onClick={() => copyShare(a)} aria-label="复制分享链接">
                <LinkIcon />
                <span className="pill-text">{copied === "ok" ? "已复制" : "复制分享链接"}</span>
              </button>
              <ImportButton uploads={uploads} />
              <a className="pill" href={albumPath(id) + "/settings/"} aria-label="设置">
                <SettingsIcon />
                <span className="pill-text">设置</span>
              </a>
            </>
          )
        }
        meta={[!a.mine && a.ownerName && `${a.ownerName} 分享`, a.expired ? "链接已到期" : `${formatDate(a.expiresAt)}前有效`]}
        empty={
          <p className="card muted">{a.mine ? "还没有照片，点右上角“导入”，或者把照片拖到这个页面上。" : "这个相册里还没有照片。"}</p>
        }
      >
        {a.note && <p className="note">{a.note}</p>}
        {copied === "failed" && <p className="error">这个浏览器不让复制，请到“设置”里长按链接手动复制。</p>}
        {copied === "ok" && <p className="success">已复制链接{a.code ? "和提取码" : ""}，可以粘贴到微信里发给家人。</p>}
        {a.mine && a.expired && (
          <p className="hint">链接已经到期，家人打不开了。到右上角“设置”里续期，原来的链接马上恢复。</p>
        )}
        {a.mine && <UploadStatus uploads={uploads} />}
        {error && <p className="error">{error}</p>}
      </Gallery>
    </main>
  );
}

type Save = (input: AlbumInput, setErr: (msg: string) => void) => Promise<boolean>;

// shareMessage 是发到微信里的一段话：名称、链接、提取码和有效期都写上，家人不用再问。
function shareMessage(a: AlbumData) {
  return [
    `「${a.title}」的照片，点开就能看大图、下载原图：`,
    a.token ? shareLink(a.token) : "",
    a.code && `提取码：${a.code}`,
    `${formatDate(a.expiresAt)}前有效`,
  ]
    .filter(Boolean)
    .join("\n");
}

// SharePanel 管分享：复制链接、提取码、有效期、换链接。只有上传者本人看得到。
function SharePanel({ album: a, save }: { album: AlbumData; save: Save }) {
  const [copied, setCopied] = useState("");
  const [error, setError] = useState("");
  const [days, setDays] = useState(7);
  const [confirmReset, setConfirmReset] = useState(false);
  const link = a.token ? shareLink(a.token) : "";

  const message = shareMessage(a);

  async function copy(text: string, what: string) {
    const ok = await copyText(text);
    setCopied(ok ? what : "failed");
    if (ok) setTimeout(() => setCopied(""), 3000);
  }

  return (
    <div className="stack">
      {a.expired && (
        <p className="hint">
          链接已经到期，家人打不开了。照片会在{formatDate(a.deleteAt)}删除；续期后原来的链接马上恢复。
        </p>
      )}
      <div className="actions">
        <input readOnly value={link} aria-label="分享链接" onFocus={(e) => e.target.select()} />
        <button onClick={() => copy(message, "message")}>{copied === "message" ? "已复制" : "复制分享文字"}</button>
        <button className="secondary" onClick={() => copy(link, "link")}>
          {copied === "link" ? "已复制" : "只复制链接"}
        </button>
      </div>
      {copied === "failed" && <p className="error">这个浏览器不让复制，请长按上面的链接手动复制。</p>}
      <p className="muted">
        拿到链接的人不用注册就能看。{a.views > 0 ? `已经打开过 ${a.views} 次。` : "还没有人打开过。"}
      </p>

      <div className="share-rows">
        <div className="share-row">
          <span className="share-label">提取码</span>
          <span className="share-value">{a.code ? <strong className="code">{a.code}</strong> : <span className="muted">不要提取码</span>}</span>
          <div className="actions">
            {a.code ? (
              <>
                <button className="secondary" onClick={() => save({ newCode: true }, setError)}>
                  换一个
                </button>
                <button className="link" onClick={() => save({ withCode: false }, setError)}>
                  去掉
                </button>
              </>
            ) : (
              <button className="secondary" onClick={() => save({ withCode: true }, setError)}>
                加提取码
              </button>
            )}
          </div>
        </div>
        <div className="share-row">
          <span className="share-label">有效期</span>
          <span className={"share-value" + (a.expired ? " error" : "")}>{expiryText(a)}</span>
          <div className="actions">
            <label className="inline-field">
              从今天起再
              <input
                type="number"
                min={1}
                max={365}
                value={Number.isNaN(days) ? "" : days}
                onChange={(e) => setDays(e.target.valueAsNumber)}
                aria-label="续期天数"
              />
              天
            </label>
            <button className="secondary" disabled={!(days >= 1 && days <= 365)} onClick={() => save({ expireDays: days }, setError)}>
              续期
            </button>
          </div>
        </div>
        <div className="share-row">
          <span className="share-label">链接</span>
          <span className="share-value muted">链接发错了人，可以换一个新链接，旧链接马上打不开。</span>
          <div className="actions">
            {confirmReset ? (
              <>
                <button
                  className="danger"
                  onClick={async () => {
                    setConfirmReset(false);
                    await save({ resetLink: true }, setError);
                  }}
                >
                  确认换链接
                </button>
                <button className="secondary" onClick={() => setConfirmReset(false)}>
                  不换
                </button>
              </>
            ) : (
              <button className="secondary" onClick={() => setConfirmReset(true)}>
                换链接
              </button>
            )}
          </div>
        </div>
      </div>
      {error && <p className="error">{error}</p>}
    </div>
  );
}

// InfoPanel 接在分享设置下面：改名称、说明、排序和到期后保留几天，最下面是删除相册。
function InfoPanel({ album: a, save }: { album: AlbumData; save: Save }) {
  const [form, setForm] = useState({ title: a.title, note: a.note, sort: a.sort, retainDays: a.retainDays });
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [deleting, setDeleting] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMessage("");
    if (await save(form, setError)) setMessage("已保存");
    setBusy(false);
  }

  async function remove() {
    setError("");
    try {
      await api.deleteAlbum(a.id);
      location.href = "/apps/sharephotos/";
    } catch (err) {
      setError(err instanceof Error ? err.message : "删除失败");
    }
  }

  const set = (patch: Partial<typeof form>) => setForm({ ...form, ...patch });

  return (
    <form className="stack settings-more" onSubmit={submit}>
      <label className="field">
        名称
        <input value={form.title} maxLength={60} onChange={(e) => set({ title: e.target.value })} />
      </label>
      <label className="field">
        说明（家人打开时看到，可以不填）
        <textarea rows={3} maxLength={300} value={form.note} onChange={(e) => set({ note: e.target.value })} />
      </label>
      <label className="field">
        照片顺序
        <select value={form.sort} onChange={(e) => set({ sort: e.target.value as AlbumData["sort"] })}>
          <option value="time">按拍摄时间</option>
          <option value="name">按文件名</option>
        </select>
      </label>
      <label className="field">
        链接到期后照片再保留几天
        <span className="muted">保留期间只有你能看到这个相册，可以续期；过了就连同照片一起删除。填 0 表示到期就删。</span>
        <input
          type="number"
          min={0}
          max={365}
          value={Number.isNaN(form.retainDays) ? "" : form.retainDays}
          onChange={(e) => set({ retainDays: e.target.valueAsNumber })}
        />
      </label>
      <div className="actions">
        <button type="submit" disabled={busy || !form.title.trim()}>
          {busy ? "保存中…" : "保存"}
        </button>
        {message && <span className="success">{message}</span>}
        {error && <span className="error">{error}</span>}
      </div>
      <div className="danger-zone actions">
        {deleting ? (
          <>
            <span>
              删除「{a.title}」和里面的 {a.count} 张照片（{formatBytes(a.totalBytes)}）？分享链接马上失效，删除后找不回来。
            </span>
            <button type="button" className="danger" onClick={remove}>
              确认删除
            </button>
            <button type="button" className="secondary" onClick={() => setDeleting(false)}>
              不删除
            </button>
          </>
        ) : (
          <button type="button" className="link danger-link" onClick={() => setDeleting(true)}>
            删除相册
          </button>
        )}
      </div>
    </form>
  );
}
