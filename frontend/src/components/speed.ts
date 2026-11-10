import { useEffect, useRef, useState } from "react";
import { formatBytes } from "../api";

// speedText 把速度和剩余字节写成“1.2 MB/秒 · 还要约 3 分钟”，上传和下载进度共用。
export function speedText(bytesPerSec: number, remaining: number) {
  const seconds = remaining / bytesPerSec;
  const left = seconds < 60 ? "不到 1 分钟" : `约 ${Math.ceil(seconds / 60)} 分钟`;
  return `${formatBytes(bytesPerSec)}/秒 · 还要${left}`;
}

// useSpeed 按最近几秒传了多少字节算速度（字节/秒）。bytes 是到现在一共传了多少；active 为 false 时不算，返回 0。
// 网络卡住时进度不会变，所以每秒重新算一次，速度会跟着降下来。
export function useSpeed(bytes: number, active: boolean) {
  const samples = useRef<[number, number][]>([]);
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!active) {
      samples.current = [];
      return;
    }
    const t = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [active]);
  if (!active) return 0;
  const now = Date.now();
  const s = samples.current.filter(([t]) => now - t < 8000);
  // 字节数变少了（比如一张重传）就重新开始算。
  samples.current = s.length && bytes < s[s.length - 1][1] ? [[now, bytes]] : [...s, [now, bytes]];
  const [t0, b0] = samples.current[0];
  return now - t0 < 1000 ? 0 : ((bytes - b0) * 1000) / (now - t0);
}
