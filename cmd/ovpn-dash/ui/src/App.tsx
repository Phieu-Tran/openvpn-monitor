import { Alert, Button, Meter, Toast } from "@heroui/react";
import { useEffect, useState } from "react";
import { QuickSearch, SessionDrawer, UserDrawer } from "./Drawers";
import { Sessions, UserList } from "./History";
import type { Filters } from "./History";
import { Online } from "./Online";
import { F, PRESETS, bytes, rate, useHash, useJSON, usePref } from "./lib";
import type { Facets, Live, LiveRow, Preset, Status } from "./lib";
import { Icon, ServerSwitch } from "./ui";

const VIEWS = [
  { id: "online", label: "Online", icon: "online" },
  { id: "history", label: "History", icon: "history" },
  { id: "users", label: "Users", icon: "users" },
] as const;

type Theme = "system" | "light" | "dark";
const NEXT_THEME: Record<Theme, Theme> = { system: "light", light: "dark", dark: "system" };
const THEME_ICON = { system: "system", light: "sun", dark: "moon" } as const;

function useTheme() {
  const [theme, setTheme] = usePref<Theme>("theme", "system");
  useEffect(() => {
    const os = matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      const dark = theme === "dark" || (theme === "system" && os.matches);
      const root = document.documentElement;
      root.classList.toggle("dark", dark);
      root.dataset.theme = dark ? "dark" : "light";
      root.style.colorScheme = dark ? "dark" : "light";
    };
    apply();
    os.addEventListener("change", apply);
    return () => os.removeEventListener("change", apply);
  }, [theme]);
  return [theme, setTheme] as const;
}

const Banner = ({ tone, children }: { tone: "danger" | "warning"; children: string }) => (
  <Alert status={tone} role="alert">
    <Alert.Indicator />
    <Alert.Content><Alert.Title>{children}</Alert.Title></Alert.Content>
  </Alert>
);

export function App() {
  const { path, query } = useHash();
  const view = path === "history" || path === "users" ? path : "online";
  const [theme, setTheme] = useTheme();
  const [narrow, setNarrow] = usePref("nav.narrow", false);

  const live = useJSON<Live>("live", () => "api/sessions", 5000);
  const status = useJSON<Status>("status", () => "api/status", 30000);
  const facets = useJSON<Facets>("facets", () => "api/facets", 5 * 60000);

  const [preset, setPreset] = usePref<Preset>("hpreset", "7d");
  const [filters, setFilterState] = useState<Filters>(() => ({
    range: { preset: preset in PRESETS ? preset : "7d" }, q: query.get("q") ?? "", server: "", profile: "", proto: "", country: "", plat: "", alerts: false,
  }));
  // One agent per OpenVPN host. The configured ones first, then names only the history knows.
  const [saved, setServer] = usePref("server", "");
  const servers = [...new Set([...(status.data?.agents.map((a) => a.name) ?? []), ...(live.data?.servers.map((s) => s.name) ?? []), ...(facets.data?.servers ?? [])])];
  const multi = servers.length > 1;
  const server = multi && servers.includes(saved) ? saved : "";
  const setFilters = (f: Filters) => { setFilterState(f); if ("preset" in f.range) setPreset(f.range.preset); };
  // links like #history?q=name set the search box
  const linked = query.get("q");
  useEffect(() => { if (linked !== null) setFilterState((f) => ({ ...f, q: linked })); }, [linked]);

  const [session, setSession] = useState<LiveRow>();
  const [user, setUser] = useState("");
  const [finding, setFinding] = useState(false);
  const openUser = (cn: string) => { setSession(undefined); setFinding(false); setUser(cn); };
  const showSessions = (cn: string) => {
    setUser("");
    setFilterState((f) => ({ ...f, q: cn })); // the hash may not change if it already names this user
    location.hash = "#history?q=" + encodeURIComponent(cn);
  };
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") { e.preventDefault(); setFinding(true); }
    };
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
  }, []);

  const rows: LiveRow[] = (live.data?.instances ?? []).flatMap((i) => i.sessions.map((s) => ({ ...s, server: i.server, instance: i.name, proto: i.proto, profile: i.profile })));
  const shown = server ? rows.filter((r) => r.server === server) : rows;
  // a profile keeps its chart colour: the position in this sorted list of all known profiles
  const profiles = [...new Set([...(facets.data?.profiles ?? []), ...(live.data?.instances.map((i) => i.profile) ?? [])])].sort();
  const same = (a: LiveRow, b: LiveRow) => a.server === b.server && a.instance === b.instance && a.cid === b.cid && a.connected_since === b.connected_since;
  const down = live.data?.servers.filter((s) => !s.ok) ?? [];
  const current = session && rows.find((r) => same(r, session));

  const st = status.data?.store;
  const alertRate = status.data?.alerts.rate ?? 0;
  const banners: { tone: "danger" | "warning"; text: string }[] = [];
  if (st && st.write_errors > 0) banners.push({ tone: "danger", text: `History is not being saved (${st.write_errors} write errors): check the data/ folder ownership (uid 65532) and free disk space on the dash host.` });
  if (status.error) banners.push({ tone: "danger", text: `The dashboard server does not answer: ${status.error}` });
  if (!live.error && view === "online") {
    for (const s of down) banners.push({ tone: "danger", text: `Agent ${s.name} does not answer: ${s.error}` + (s.last_ok ? ` — its sessions are as of ${F.abs.format(Date.parse(s.last_ok))}` : "") });
  }
  if (live.error && view === "online") banners.push({ tone: "danger", text: live.error + (live.data ? ` — showing data from ${F.abs.format(Date.parse(live.data.generated_at))}` : "") });
  if (st?.trimmed) banners.push({ tone: "warning", text: `History was trimmed to stay under ${bytes(st.max_bytes)}: the oldest data is from ${st.oldest ? F.abs.format(Date.parse(st.oldest)) : "–"}.` });

  const agentText = live.error ? (live.error.includes("401") && !multi ? "Agent: bad token" : multi ? "Agents unreachable" : "Agent unreachable")
    : !live.data ? "Connecting…" : down.length ? `${down.length} of ${live.data.servers.length} agents down` : "Live";
  const chip = live.error ? "bg-danger-soft text-danger" : !live.data ? "bg-default text-muted" : down.length ? "bg-warning-soft text-warning" : "bg-success-soft text-success";
  const nav = VIEWS.map((v) => (
    <a key={v.id} href={`#${v.id}`} aria-current={view === v.id ? "page" : undefined} title={v.label}
      className={`relative flex min-h-11 items-center gap-3 rounded-xl px-3 text-sm font-medium whitespace-nowrap transition-colors ${view === v.id ? "bg-accent-soft text-accent-soft-foreground" : "text-muted hover:bg-default hover:text-foreground"}`}>
      <Icon name={v.icon} /><span className={narrow ? "md:hidden" : ""}>{v.label}</span>
      {v.id === "online" && live.data && (
        <span className={`num rounded-full bg-accent px-1.5 text-[11px] leading-5 font-semibold text-accent-foreground ${narrow ? "md:absolute md:top-0.5 md:right-1" : "ml-auto"}`}>{shown.length}</span>
      )}
    </a>
  ));
  const props = { filters: { ...filters, server }, setFilters, facets: facets.data, alertRate, onUser: openUser, profiles, multi };

  return (
    <div className="flex min-h-screen bg-background text-foreground">
      <Toast.Provider />
      <aside className={`sticky top-0 hidden h-screen shrink-0 flex-col gap-1 border-r border-border bg-surface p-3 md:flex ${narrow ? "w-[4.5rem]" : "w-60"}`}>
        <div className="mb-4 flex items-center gap-2.5 px-1.5 py-1">
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-accent text-accent-foreground" aria-hidden="true">
            <svg viewBox="0 0 24 24" className="size-4.5" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 3l7 3v5c0 4.5-3 8.5-7 10-4-1.5-7-5.5-7-10V6zM9 12l2 2 4-4" />
            </svg>
          </span>
          {!narrow && <span className="leading-tight"><b className="block text-sm">OpenVPN Monitor</b><span className="text-xs text-muted">who is online</span></span>}
        </div>
        <nav aria-label="Views" className="grid gap-1">{nav}</nav>
        <div className="mt-auto grid gap-3">
          {!narrow && st && st.max_bytes > 0 && (
            <Meter aria-label="History storage" size="sm" value={st.data_bytes} maxValue={st.max_bytes} className="px-2">
              <span className="num col-span-full text-xs text-muted">History {bytes(st.data_bytes)} / {bytes(st.max_bytes)}</span>
              <Meter.Track><Meter.Fill /></Meter.Track>
            </Meter>
          )}
          <Button size="sm" variant="ghost" aria-label={narrow ? "Expand the sidebar" : "Collapse the sidebar"} onPress={() => setNarrow(!narrow)}>
            <Icon name={narrow ? "right" : "left"} />{!narrow && "Collapse"}
          </Button>
        </div>
      </aside>

      <div className="flex min-w-0 grow flex-col">
        <header className="sticky top-0 z-20 flex items-center gap-3 border-b border-border bg-surface/85 px-4 py-2.5 backdrop-blur">
          <h1 className="text-lg font-semibold">{VIEWS.find((v) => v.id === view)!.label}</h1>
          {/* looks like a search box, opens the Ctrl+K search */}
          <button type="button" onClick={() => setFinding(true)}
            className="mx-auto flex h-9 w-full max-w-sm cursor-pointer items-center gap-2 rounded-xl border border-border bg-default px-3 text-sm text-muted transition-colors hover:text-foreground">
            <Icon name="search" className="size-4" /><span className="truncate">Find a user or IP…</span>
            <kbd className="ml-auto rounded-md border border-border px-1.5 text-[11px] max-sm:hidden">Ctrl K</kbd>
          </button>
          <span title={live.error || `last update ${live.data ? F.abs.format(Date.parse(live.data.generated_at)) : "–"}`}
            className={`flex shrink-0 items-center gap-2 rounded-full px-2.5 py-1 text-xs font-medium ${chip}`}>
            <i className={live.data && !live.error && !down.length ? "live-dot" : "size-2 rounded-full bg-current"} />
            {agentText}
            {live.data && !live.error && <span className="num font-normal max-sm:hidden">{F.clock.format(Date.parse(live.data.generated_at))}</span>}
          </span>
          <Button isIconOnly size="sm" variant="ghost" aria-label={`Theme: ${theme}. Switch to ${NEXT_THEME[theme]}`} onPress={() => setTheme(NEXT_THEME[theme])}>
            <Icon name={THEME_ICON[theme]} />
          </Button>
        </header>
        <nav aria-label="Views" className="flex gap-1 overflow-x-auto border-b border-border bg-surface px-3 py-1.5 md:hidden">{nav}</nav>
        {multi && (
          <div className="border-b border-border bg-surface px-4 py-2">
            <ServerSwitch value={server} onChange={setServer} servers={servers.map((name) => {
              const s = live.data?.servers.find((x) => x.name === name);
              return { name, ok: s?.ok, error: s?.error, online: live.data && s ? rows.filter((r) => r.server === name).length : undefined };
            })} />
          </div>
        )}

        <main className="mx-auto grid w-full max-w-[1600px] min-w-0 content-start gap-4 p-4">
          {banners.map((b) => <Banner key={b.text} tone={b.tone}>{b.text}</Banner>)}
          {view === "online" && <Online live={live.data} error={live.error} alertRate={alertRate} profiles={profiles} server={server} multi={multi} onSession={setSession} />}
          {view === "history" && <Sessions {...props} />}
          {view === "users" && <UserList {...props} />}
        </main>

        <footer className="mx-auto mt-auto w-full max-w-[1600px] px-4 pb-5 text-xs text-muted">
          {status.data && st && [
            `ovpn-dash ${status.data.version}`,
            multi ? `agents ${status.data.agents.map((a) => `${a.name} ${a.version || "?"}`).join(", ")}` : `agent ${status.data.agents[0]?.version || "?"}`,
            `history since ${st.oldest ? F.abs.format(Date.parse(st.oldest)) : "–"}: ${st.sessions.toLocaleString("en-GB")} sessions, ${bytes(st.data_bytes)}, kept ${Math.round(st.retention_days)} days`,
            `alert above ${alertRate ? rate(alertRate) : "off"}`,
            status.data.geoip_ok ? `IP geolocation: ${status.data.geoip} (offline database)` : "GeoIP not installed",
          ].join(" · ")}
          {/* DB-IP Lite is CC BY 4.0: credit the source */}
          {status.data?.geoip_ok && status.data.geoip.startsWith("DBIP") && (
            <> · <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-foreground">IP Geolocation by DB-IP</a></>
          )}
        </footer>
      </div>

      {session && <SessionDrawer session={current ?? session} online={!!current || !!live.error} multi={multi} onUser={openUser} onClose={() => setSession(undefined)} />}
      {user && <UserDrawer cn={user} profiles={profiles} multi={multi} onSessions={showSessions} onClose={() => setUser("")} />}
      {finding && <QuickSearch live={rows} multi={multi} onUser={openUser} onClose={() => setFinding(false)} />}
    </div>
  );
}
