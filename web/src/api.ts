import { create } from "zustand";
export interface User {
  id: number;
  username: string;
  role: "admin" | "operator";
  must_change: boolean;
}
export const useAuth = create<{
  user: User | null;
  csrf: string;
  set: (user: User | null, csrf?: string) => void;
}>((set) => ({
  user: null,
  csrf: "",
  set: (user, csrf = "") => set({ user, csrf }),
}));
export async function api<T = any>(
  path: string,
  method = "GET",
  data?: unknown,
): Promise<T> {
  const response = await fetch("/api" + path, {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": useAuth.getState().csrf,
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 401) useAuth.getState().set(null);
    throw new Error(result.error || `请求失败 ${response.status}`);
  }
  return result;
}
export const bytes = (n: number = 0) => {
  if (n === 0) return "0 B";
  const i = Math.min(Math.floor(Math.log(Math.max(n, 1)) / Math.log(1024)), 5);
  return `${(n / 1024 ** i).toFixed(i > 0 ? 1 : 0)} ${["B", "KiB", "MiB", "GiB", "TiB", "PiB"][i]}`;
};
export const date = (s: string) =>
  s ? new Date(s).toLocaleString("zh-CN", { hour12: false }) : "—";
