import { useEffect, useState, type FormEvent } from "react";
import { api, formatBytes, type Settings as SettingsData } from "../api";
import { BackIcon } from "../icons";

// 设置页，从首页右上角的“设置”进入：新相册的默认有效期、到期后保留几天、下载原图的总带宽。保存时由后端检查“上传照片”权限。
export default function Settings() {
  const [form, setForm] = useState<SettingsData | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    api
      .settings()
      .then(setForm)
      .catch((err) => setError(err.message));
  }, []);

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!form) return;
    setBusy(true);
    setError("");
    setMessage("");
    try {
      setForm(await api.saveSettings(form));
      setMessage("已保存");
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  const set = (patch: Partial<SettingsData>) => form && setForm({ ...form, ...patch });
  const num = (n: number) => (Number.isNaN(n) ? "" : n);

  return (
    <main className="content stack">
      <div className="page-head">
        <a className="pill" href="/apps/sharephotos/">
          <BackIcon />
          全部相册
        </a>
        <h1 className="page-title">设置</h1>
      </div>
      {!form && (error ? <p className="error">{error}</p> : <p className="muted">加载中…</p>)}
      {form && (
        <form className="stack" onSubmit={save}>
          <section className="card stack">
            <h1>新相册的默认值</h1>
            <p className="muted">只影响以后新建的相册；已有的相册在相册页里单独续期和修改。</p>
            <label className="field">
              分享链接有效几天
              <input type="number" min={1} max={365} value={num(form.expireDays)} onChange={(e) => set({ expireDays: e.target.valueAsNumber })} />
            </label>
            <label className="field">
              链接到期后照片再保留几天
              <span className="muted">保留期间只有上传者能看到，可以续期；过了就连同照片一起删除。</span>
              <input type="number" min={0} max={365} value={num(form.retainDays)} onChange={(e) => set({ retainDays: e.target.valueAsNumber })} />
            </label>
          </section>
          <section className="card stack">
            <h1>下载限速</h1>
            <p className="muted">
              所有人下载原图加起来最多用多少带宽，留一些给门户和其他应用，免得有人下载时别的页面打不开。按服务器的带宽填，比如 5 Mbps 的服务器填 4；填 0 表示不限速。缩略图和大图预览不受限制。
            </p>
            <label className="field">
              最多多少 Mbps
              <input type="number" min={0} max={10000} value={num(form.rateMbps)} onChange={(e) => set({ rateMbps: e.target.valueAsNumber })} />
            </label>
          </section>
          {form.diskTotal > 0 && (
            <section className="card stack">
              <h1>硬盘空间</h1>
              <div className="progress" aria-hidden>
                <span style={{ width: `${((form.diskTotal - form.diskFree) / form.diskTotal) * 100}%` }} />
              </div>
              <p className="muted">
                服务器硬盘还剩 {formatBytes(form.diskFree)}，共 {formatBytes(form.diskTotal)}。剩下不到 1 GB 时不能再上传，先删掉一些旧相册。
              </p>
            </section>
          )}
          <div className="actions">
            <button type="submit" disabled={busy}>
              {busy ? "保存中…" : "保存"}
            </button>
            {message && <span className="success">{message}</span>}
            {error && <span className="error">{error}</span>}
          </div>
        </form>
      )}
    </main>
  );
}
