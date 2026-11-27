import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";

export const TZ = "Asia/Ho_Chi_Minh";
export const M = 60e3, H = 3600e3, D = 86400e3;
export const VN = 7 * H; // UTC+7, no DST

// ---------- API types

// What the client reported about itself (OpenVPN IV_* peer info). Self-reported, not verified.
export interface Device { plat?: string; plat_ver?: string; gui?: string; ver?: string; hwaddr?: string }

export interface LiveSession {
  device?: Device;
  cn: string; username?: string; real_ip: string; real_port: number; vpn_ip: string; vpn_ip6: string;
  bytes_rx: number; bytes_tx: number; rate_rx: number; rate_tx: number; connected_since: string;
  cid: number; peer_id: number; cipher: string; last_ref?: string; country?: string;
}
// The CN OpenVPN shows until a connection's TLS handshake is done: a user connecting right now,
// or a scanner / a client without a valid certificate that never finishes.
export const UNDEF = "UNDEF";

export interface Instance {
  server: string; name: string; profile: string; proto: string; up: boolean; last_error?: string; last_ok?: string;
  version?: string; sessions: LiveSession[];
}
// One agent = one OpenVPN host. Its instances stay in the list (last good answer) while it is unreachable.
export interface LiveServer { name: string; ok: boolean; error?: string; last_ok?: string; agent_version?: string }
export interface Live { generated_at: string; servers: LiveServer[]; instances: Instance[] }
export type LiveRow = LiveSession & { server: string; instance: string; profile: string; proto: string };

export interface SessionRecord {
  server: string; cn: string; profile: string; proto: string; instance: string; cid: number; real_ip: string; real_port: number;
  vpn_ip: string; start: string; end: string; bytes_rx: number; bytes_tx: number; peak_rate?: number;
  open?: boolean; country?: string; device?: Device;
}
export interface DeviceUse { plat: string; gui?: string; ver?: string; users: number; sessions: number; last: string }
// The access log: a user's new connections to one destination in one minute.
export interface AccessRow {
  server: string; cn: string; profile: string; src: string; dst: string; port: number; proto: string;
  first: string; last: string; n: number;
}
export interface AccessDest { dst: string; port: number; proto: string; connections: number; minutes: number; first: string; last: string }
export interface Access {
  access: AccessRow[]; destinations?: AccessDest[] | null;
  summary: { rows: number; connections: number; users: number; destinations: number };
}
export interface Probe {
  server: string; instance: string; profile: string; proto: string; real_ip: string; real_port: number; country?: string; start: string; end: string;
}
export interface Probes { probes: Probe[]; summary: { attempts: number; ips: number; countries: CountryUse[] } }
export interface CountryUse { country: string; users: number; sessions: number; bytes: number }
export interface History {
  sessions: SessionRecord[];
  devices: DeviceUse[];
  countries: CountryUse[];
  summary: { total: number; users: number; online_seconds: number; bytes_rx: number; bytes_tx: number; alerts: number };
}
export interface UserRow {
  cn: string; servers: string[]; profiles: string[]; sessions: number; online_seconds: number; bytes_rx: number; bytes_tx: number;
  peak_rate: number; last_seen: string; online: boolean; countries: string[]; ips: number; plats: string[];
}
export interface Users { users: UserRow[]; summary: { total: number; online: number; alerts: number } }
export interface UserDetail {
  sessions: SessionRecord[];
  ips: { ip: string; country?: string; first: string; last: string; sessions: number; bytes: number }[];
  devices: (Device & { first: string; last: string; sessions: number })[];
  days: { day: string; bytes_rx: number; bytes_tx: number }[];
}
export interface Series {
  from: string; to: string; step: number;
  points: { t: string; online: Record<string, number>; servers?: Record<string, number>; up: number; total: number; rx: number; tx: number }[];
}
export interface Heat { cells: { day: number; hour: number; avg: number; max: number }[] }
export interface Facets { servers: string[]; profiles: string[]; countries: string[]; plats: string[] }
export interface Status {
  version: string; geoip: string; geoip_ok: boolean; alerts: { rate: number };
  agents: { name: string; ok: boolean; error?: string; version?: string; flows?: string; flows_dropped?: number; logins?: string }[];
  store: {
    sessions: number; open: number; samples: number; oldest?: string; write_errors: number;
    retention_days: number; data_bytes: number; max_bytes: number; trimmed?: string;
  };
}

// ---------- time ranges: a preset means "the last N, up to now"; dates mean whole days

export const PRESETS = {
  "5m": 5 * M, "15m": 15 * M, "30m": 30 * M, "1h": H, "3h": 3 * H, "6h": 6 * H, "12h": 12 * H,
  "24h": D, "7d": 7 * D, "30d": 30 * D, "90d": 90 * D,
};
export type Preset = keyof typeof PRESETS;
export type Range = { preset: Preset } | { from: string; to: string }; // YYYY-MM-DD

export function rangeMs(r: Range): [number, number] {
  if ("preset" in r) { const now = Date.now(); return [now - PRESETS[r.preset], now]; }
  return [Date.parse(r.from + "T00:00:00+07:00"), Date.parse(r.to + "T23:59:59+07:00")];
}
export function rangeParams(r: Range): URLSearchParams {
  const [from, to] = rangeMs(r);
  return new URLSearchParams({ from: new Date(from).toISOString(), to: new Date(to).toISOString() });
}
export const rangeKey = (r: Range) => ("preset" in r ? r.preset : `${r.from}_${r.to}`);

// ---------- formatting

export const F = {
  abs: new Intl.DateTimeFormat("en-GB", { timeZone: TZ, dateStyle: "short", timeStyle: "medium" }),
  clock: new Intl.DateTimeFormat("en-GB", { timeZone: TZ, timeStyle: "medium" }),
  hm: new Intl.DateTimeFormat("en-GB", { timeZone: TZ, hour: "2-digit", minute: "2-digit" }),
  dm: new Intl.DateTimeFormat("en-GB", { timeZone: TZ, day: "2-digit", month: "2-digit" }),
  dmhm: new Intl.DateTimeFormat("en-GB", { timeZone: TZ, day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" }),
  day: new Intl.DateTimeFormat("en-CA", { timeZone: TZ }), // YYYY-MM-DD
  rel: new Intl.RelativeTimeFormat("en", { numeric: "auto" }),
};
export function bytes(n: number) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 2 : 1) : Math.round(n)) + " " + u[i];
}
export const rate = (n: number) => (n > 0 ? bytes(n) + "/s" : "–");
export function duration(s: number) {
  s = Math.max(0, Math.floor(s));
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  const p = (x: number) => String(x).padStart(2, "0");
  return d ? `${d}d ${h}h ${p(m)}m` : h ? `${h}h ${p(m)}m` : `${m}m ${p(s % 60)}s`;
}
export function relative(t: number) {
  const s = (t - Date.now()) / 1000, a = Math.abs(s);
  if (a < 60) return F.rel.format(Math.round(s), "second");
  if (a < 3600) return F.rel.format(Math.round(s / 60), "minute");
  if (a < 86400) return F.rel.format(Math.round(s / 3600), "hour");
  return F.rel.format(Math.round(s / 86400), "day");
}
export const addr = (ip: string, port: number) => (!port ? ip : (ip.includes(":") ? `[${ip}]` : ip) + ":" + port);
export const sum = <T,>(list: T[], f: (x: T) => number) => list.reduce((a, x) => a + f(x), 0);

let regionNames: Intl.DisplayNames | null = null;
try { regionNames = new Intl.DisplayNames(["en"], { type: "region" }); } catch { /* old browser */ }
export const countryName = (cc: string) => {
  if (cc === "LAN") return "LAN (private)";
  try { return regionNames?.of(cc) ?? cc; } catch { return cc; }
};
export const flagEmoji = (cc: string) => String.fromCodePoint(...[...cc].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));

// ---------- devices

const PLATS: Record<string, string> = { win: "Windows", mac: "macOS", ios: "iOS", android: "Android", linux: "Linux" };
export const platName = (p?: string) => (p ? PLATS[p] ?? p : "Unknown");

// osName: "Windows 11", "macOS 14.6.1", "Android 14"; Windows 11 still reports 10.0, the build tells them apart.
export function osName(d?: Device) {
  if (!d?.plat) return "Unknown";
  const v = d.plat_ver;
  if (d.plat === "win" && v) { const build = Number(v.split(".")[2]); return build >= 22000 ? "Windows 11" : build ? "Windows 10" : "Windows"; }
  if (d.plat === "linux" || !v) return platName(d.plat); // linux sends the kernel version: shown apart
  return `${platName(d.plat)} ${v}`;
}

// appName turns IV_GUI_VER (or IV_VER without a GUI) into the client app people know.
export function appName(d?: Device) {
  const g = d?.gui ?? "";
  const ver = (re: RegExp) => g.match(re)?.[1] ?? "";
  if (g.startsWith("OpenVPN_GUI")) return `OpenVPN GUI ${ver(/^OpenVPN_GUI_([\d.]+)/)}`.trim();
  if (/^OC(Windows|macOS)_|^net\.openvpn\.connect\./.test(g)) return `OpenVPN Connect ${ver(/_([\d.]+)/)}`.trim();
  if (g.startsWith("de.blinkt.openvpn")) return `OpenVPN for Android ${ver(/_([\d.]+)/)}`.trim();
  if (g.startsWith("Tunnelblick")) return `Tunnelblick ${ver(/^Tunnelblick_([\d.]+)/)}`.trim();
  if (g) return g.replace(/_/g, " ");
  if (!d?.ver) return "";
  return d.ver.startsWith("3.") ? "OpenVPN 3 client" : `OpenVPN ${d.ver}`;
}

// ---------- data

export async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(url, { cache: "no-store", signal });
  const j = (r.headers.get("Content-Type") ?? "").includes("json") ? await r.json() : null;
  if (!r.ok || !j) throw new Error(j?.error || `HTTP ${r.status} ${r.statusText}`.trim());
  return j as T;
}

// useJSON loads url() now and every `every` ms, pausing while the tab is hidden.
// A new key reloads at once; the previous data stays on screen until it arrives.
// An empty key loads nothing.
export function useJSON<T>(key: string, url: () => string, every: number) {
  const [state, set] = useState<{ data?: T; error: string; key?: string }>({ error: "" });
  const urlRef = useRef(url);
  urlRef.current = url;
  useEffect(() => {
    if (!key) return;
    const ctl = new AbortController();
    let busy = false, loaded = false;
    const run = async () => {
      if (busy || (loaded && document.hidden)) return;
      busy = true;
      try {
        const data = await getJSON<T>(urlRef.current(), ctl.signal);
        if (!ctl.signal.aborted) set({ data, error: "", key });
      } catch (e) {
        if (!ctl.signal.aborted) set((s) => ({ data: s.data, error: (e as Error).message, key }));
      } finally {
        busy = false;
        loaded = true;
      }
    };
    run();
    const timer = setInterval(run, every);
    document.addEventListener("visibilitychange", run);
    return () => { ctl.abort(); clearInterval(timer); document.removeEventListener("visibilitychange", run); };
  }, [key, every]);
  return { data: state.data, error: state.error, loading: state.key !== key };
}

// usePref is useState remembered in this browser.
export function usePref<T>(name: string, initial: T) {
  const [value, set] = useState<T>(() => {
    try { const v = localStorage.getItem("ovpn." + name); return v === null ? initial : (JSON.parse(v) as T); }
    catch { return initial; }
  });
  const save = useCallback((v: T) => {
    set(v);
    try { localStorage.setItem("ovpn." + name, JSON.stringify(v)); } catch { /* private mode */ }
  }, [name]);
  return [value, save] as const;
}

export function useDebounced<T>(value: T, ms = 250) {
  const [v, set] = useState(value);
  useEffect(() => { const t = setTimeout(() => set(value), ms); return () => clearTimeout(t); }, [value, ms]);
  return v;
}

export function useHash() {
  const hash = useSyncExternalStore(
    (cb) => { addEventListener("hashchange", cb); return () => removeEventListener("hashchange", cb); },
    () => location.hash,
  );
  const [path, query] = hash.slice(1).split("?");
  return { path, query: new URLSearchParams(query ?? "") };
}
