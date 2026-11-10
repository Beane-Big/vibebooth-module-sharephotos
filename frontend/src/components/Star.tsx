import { StarIcon } from "../icons";

// Star 是每张照片右上角的星星：点一下选中（实心），再点一下取消（空心）。选中的照片可以一起下载。
export default function Star({ on, name, onToggle }: { on: boolean; name: string; onToggle: () => void }) {
  return (
    <button
      className={"star" + (on ? " on" : "")}
      aria-label={on ? `取消选中 ${name}` : `选中 ${name}`}
      aria-pressed={on}
      // 卡片靠按下、松开判断拖动和点击，点星星时不能让卡片当成点了照片。
      onPointerDown={(e) => e.stopPropagation()}
      onPointerUp={(e) => e.stopPropagation()}
      onClick={onToggle}
    >
      <StarIcon filled={on} />
    </button>
  );
}
