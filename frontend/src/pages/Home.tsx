import { useEffect, useState, type FormEvent } from "react";
import { albumPath, albumUrls, api, expiryText, formatBytes, shotDateText, type Album } from "../api";
import { PhotoIcon, PlusIcon, SettingsIcon } from "../icons";
import { can, getMe, type Me } from "../platform";

// 首页：所有能看的相册，有“上传照片”权限的人可以新建相册。点一个相册进入它的页面。
export default function Home() {
  const [me, setMe] = useState<Me | null>(null);
  const [albums, setAlbums] = useState<Album[] | null>(null);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [shotDate, setShotDate] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    getMe().then(setMe);
    api
      .albums()
      .then(setAlbums)
      .catch((err) => setError(err.message));
  }, []);

  const uploader = me !== null && can(me, "upload");

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const a = await api.createAlbum({ title, shotDate });
      location.href = albumPath(a.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "新建失败");
      setBusy(false);
    }
  }

  return (
    <main className="content wide stack">
      <div className="page-head">
        {uploader && !creating && (
          <button className="pill" onClick={() => setCreating(true)}>
            <PlusIcon />
            新建相册
          </button>
        )}
        {uploader && (
          <a className="pill end" href="/apps/sharephotos/settings/">
            <SettingsIcon />
            设置
          </a>
        )}
      </div>

      {creating && (
        <form className="card stack confirm" onSubmit={create}>
          <h1>新建相册</h1>
          <label className="field">
            名称
            <input
              value={title}
              autoFocus
              maxLength={60}
              placeholder="例如：外婆八十大寿"
              onChange={(e) => setTitle(e.target.value)}
            />
          </label>
          <label className="field">
            拍摄日期（可以不填）
            <input type="date" value={shotDate} onChange={(e) => setShotDate(e.target.value)} />
          </label>
          <div className="actions">
            <button type="submit" disabled={busy || !title.trim()}>
              {busy ? "新建中…" : "新建并上传照片"}
            </button>
            <button type="button" className="secondary" onClick={() => setCreating(false)}>
              取消
            </button>
          </div>
        </form>
      )}

      {error && <p className="error">{error}</p>}
      {albums === null && !error && <p className="muted">加载中…</p>}
      {albums?.length === 0 && (
        <p className="card muted">{uploader ? "还没有相册，点“新建相册”开始分享。" : "现在没有分享中的相册。"}</p>
      )}

      <ul className="albums">
        {albums?.map((a) => (
          <li key={a.id}>
            <a className={"card album" + (a.expired ? " expired" : "")} href={albumPath(a.id)}>
              <span className="album-cover">
                {a.cover ? <img src={albumUrls(a.id).photo({ id: a.cover }, "thumb")} alt="" loading="lazy" /> : <PhotoIcon />}
              </span>
              <span className="album-text">
                <span className="item-name">{a.title}</span>
                <span className="muted meta">
                  {[
                    shotDateText(a.shotDate),
                    `${a.ready} 张`,
                    a.ready > 0 && formatBytes(a.totalBytes),
                    !a.mine && a.ownerName && `${a.ownerName} 分享`,
                  ]
                    .filter(Boolean)
                    .map((text, i) => (
                      <span key={i}>{text}</span>
                    ))}
                </span>
                <span className={a.expired ? "error" : "muted"}>{expiryText(a)}</span>
                {a.mine && a.count > a.ready && <span className="muted">有 {a.count - a.ready} 张还不能看</span>}
              </span>
            </a>
          </li>
        ))}
      </ul>
    </main>
  );
}
