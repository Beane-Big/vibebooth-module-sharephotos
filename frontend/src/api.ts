export type Photo = {
  id: number;
  name: string;
  size: number;
  width: number;
  height: number;
  takenAt: string | null;
  status: "processing" | "ready" | "failed";
  downloads: number;
  createdAt: string;
};

export type Album = {
  id: number;
  title: string;
  note: string;
  ownerName: string;
  coverId: number; // 设定的封面，0 表示没设
  cover: number; // 实际显示的封面
  sort: "time" | "name";
  expiresAt: string;
  retainDays: number;
  views: number;
  createdAt: string;
  count: number;
  ready: number;
  totalBytes: number;
  mine: boolean;
  expired: boolean;
  deleteAt: string;
  token?: string; // 只有上传者本人拿得到
  code?: string;
};

export type AlbumDetail = { album: Album; photos: Photo[]; rateMbps: number };

export type Settings = { expireDays: number; retainDays: number; rateMbps: number; diskFree: number; diskTotal: number };

// 新建和修改相册时提交的内容，没写的字段不变。
export type AlbumInput = Partial<{
  title: string;
  note: string;
  coverId: number;
  sort: "time" | "name";
  expireDays: number;
  retainDays: number;
  withCode: boolean;
  newCode: boolean;
  resetLink: boolean;
}>;

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

// 调用本模块后端的对内接口；登录 Cookie 由浏览器自动带上，网关负责校验。
export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (res.status === 401) {
    location.href = "/login?next=" + encodeURIComponent(location.pathname);
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body.error ?? "请求失败");
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

const base = "/api/sharephotos";

export const api = {
  albums: () => request<Album[]>(`${base}/albums`),
  album: (id: number) => request<AlbumDetail>(`${base}/albums/${id}`),
  createAlbum: (input: AlbumInput) => request<Album>(`${base}/albums`, { method: "POST", body: JSON.stringify(input) }),
  updateAlbum: (id: number, input: AlbumInput) =>
    request<AlbumDetail>(`${base}/albums/${id}`, { method: "PATCH", body: JSON.stringify(input) }),
  deleteAlbum: (id: number) => request<void>(`${base}/albums/${id}`, { method: "DELETE" }),
  deletePhoto: (album: number, photo: number) => request<void>(`${base}/albums/${album}/photos/${photo}`, { method: "DELETE" }),
  settings: () => request<Settings>(`${base}/settings`),
  saveSettings: (s: Settings) => request<Settings>(`${base}/settings`, { method: "PUT", body: JSON.stringify(s) }),
};

export type PhotoKind = "thumb" | "preview" | "original";

// 取图和打包下载的网址。门户里和分享页的接口一样，只是前缀不同：门户里是 /albums/<id>，分享页是 /s/<令牌>。
export type GalleryUrls = {
  photo: (p: Pick<Photo, "id">, kind: PhotoKind, inline?: boolean) => string;
  zip: (ids?: number[]) => string;
};

export function galleryUrls(prefix: string): GalleryUrls {
  return {
    photo: (p, kind, inline) => `${prefix}/photos/${p.id}/${kind}${inline ? "?inline=1" : ""}`,
    zip: (ids) => `${prefix}/zip${ids ? "?ids=" + ids.join(",") : ""}`,
  };
}

export const albumUrls = (id: number) => galleryUrls(`${base}/albums/${id}`);
export const shareUrls = (token: string) => galleryUrls(`${base}/s/${token}`);

// 分享链接的完整网址，复制给家人用。
export const shareLink = (token: string) => `${location.origin}/apps/sharephotos/s/${token}`;

// uploadPhoto 上传一张照片，onProgress 报告已经传了多少（0–1）。用 XMLHttpRequest 是因为 fetch 拿不到上传进度。
export function uploadPhoto(albumId: number, file: File, onProgress: (ratio: number) => void): Promise<Photo> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `${base}/albums/${albumId}/photos`);
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
    xhr.onload = () => {
      let body: { error?: string } = {};
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        // 网关出错时返回的不是 JSON
      }
      if (xhr.status === 401) location.href = "/login?next=" + encodeURIComponent(location.pathname);
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as Photo);
      else reject(new ApiError(xhr.status, body.error ?? `上传失败（${xhr.status}）`));
    };
    xhr.onerror = () => reject(new ApiError(0, "网络断了，没有传完"));
    const form = new FormData();
    form.append("file", file);
    xhr.send(form);
  });
}

// formatBytes 把字节数写成 MB 或 GB，照片大小一般在几 MB。
export function formatBytes(n: number) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(n >= 10 << 30 ? 0 : 1) + " GB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(n >= 100 << 20 ? 0 : 1) + " MB";
  return Math.max(1, Math.round(n / 1024)) + " KB";
}

// formatDate 写成“11月17日”，跨年时带上年份；withTime 时加上“20:00”。
export function formatDate(iso: string, withTime = false) {
  const d = new Date(iso);
  const sameYear = d.getFullYear() === new Date().getFullYear();
  let s = `${sameYear ? "" : d.getFullYear() + "年"}${d.getMonth() + 1}月${d.getDate()}日`;
  if (withTime) s += ` ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  return s;
}

// daysLeft 是离某个时刻还有几天，不足一天按一天算。
export function daysLeft(iso: string) {
  return Math.max(0, Math.ceil((new Date(iso).getTime() - Date.now()) / 86_400_000));
}

// expiryText 说明链接还能用多久，到期了说明照片哪天删除。
export function expiryText(a: Album) {
  if (a.expired) return `链接已到期，照片将在${formatDate(a.deleteAt)}删除`;
  return `${formatDate(a.expiresAt, true)}前有效，还有 ${daysLeft(a.expiresAt)} 天`;
}

// albumPath 是门户里一个相册的页面。
export const albumPath = (id: number) => `/apps/sharephotos/a/${id}`;

// downloadMinutes 按限速估计下载要几分钟；不限速时按 5 Mbps 估计。
export function downloadMinutes(bytes: number, rateMbps: number) {
  const bytesPerSec = (rateMbps > 0 ? rateMbps : 5) * 125_000;
  return Math.max(1, Math.ceil(bytes / bytesPerSec / 60));
}

// 微信内置浏览器下载不了 zip 这类文件，单张图片要长按保存。
export const inWeChat = /MicroMessenger/i.test(navigator.userAgent);

// iPhone 和 iPad 上直接下载的图片进“文件”而不是“照片”，要用系统的分享面板“存储图像”。新的 iPad 自称 Macintosh，靠触屏区分。
export const onIOS = /iPhone|iPad|iPod/.test(navigator.userAgent) || (/Macintosh/.test(navigator.userAgent) && navigator.maxTouchPoints > 1);

// copyText 复制到剪贴板。门户的 iframe 可能没有剪贴板权限，失败时退回到选中文字再复制的老办法。
export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = document.createElement("textarea");
    area.value = text;
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    const ok = document.execCommand("copy");
    area.remove();
    return ok;
  }
}
