// 和平台打交道的地方都收在这里：取当前用户和他的权限。
// 没有后端（第 0 级）或者取不到时返回本地用户，页面照样能用；以后接入平台只改这个文件。
import { request } from "./api";
import { hasBackend } from "./config";

// all 只在后端开独立模式（npm run standalone）时为 true：本地用户拥有全部权限。
export type Me = { name: string; menus: string[]; perms: string[]; all: boolean };

// 本地用户：没有平台时拥有全部权限。按钮能不能点只是界面上的提示，真正的检查在后端。
const local: Me = { name: "", menus: [], perms: [], all: true };

export async function getMe(): Promise<Me> {
  if (!hasBackend) return local;
  try {
    const me = await request<Me>("/api/sharephotos/me");
    // 没有任何权限时后端返回 null。
    return { ...me, menus: me.menus ?? [], perms: me.perms ?? [] };
  } catch {
    return local;
  }
}

// can 判断当前用户有没有某项功能权限（module.yaml 的 permissions），用来隐藏没权限的按钮。
export function can(me: Me, perm: string) {
  return me.all || me.perms.includes(perm);
}
