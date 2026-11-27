import { Card, Skeleton, Tooltip } from "@heroui/react";
import { useState } from "react";
import type { ReactNode } from "react";
import { Heatmap, Legend, Sparkline, TimeChart, seriesColor } from "./charts";
import { F, M, H, PRESETS, appName, bytes, countryName, duration, osName, rangeKey, rangeParams, rate, relative, sum, useJSON, usePref, addr } from "./lib";
import type { Heat, Live, LiveRow, Preset, Series } from "./lib";
import { Badge, ColumnMenu, CountryTag, DataTable, DeviceTag, ICONS, Icon, Panel, ProfileTabs, ProfileTag, Search, Segmented, Toggle, profileColor } from "./ui";
import type { Col, Sort } from "./ui";

const CHART_PRESETS: Preset[] = ["5m", "15m", "30m", "1h", "3h", "6h", "12h", "24h", "7d", "30d", "90d"];

const sortKeys: Record<string, (s: LiveRow) => number | string> = {
  cn: (s) => s.cn, profile: (s) => s.profile, proto: (s) => s.proto, vpn: (s) => s.vpn_ip,
  real: (s) => (s.country ?? "") + s.real_ip, since: (s) => Date.parse(s.connected_since),
  dur: (s) => -Date.parse(s.connected_since), traffic: (s) => s.bytes_rx + s.bytes_tx, rate: (s) => s.rate_rx + s.rate_tx,
  seen: (s) => (s.last_ref ? Date.parse(s.last_ref) : 0), cipher: (s) => s.cipher,
  device: (s) => (s.device?.plat ? osName(s.device) + appName(s.device) : "~"), // unknown last
};

function Kpi({ label, icon, value, sub, tone = "accent", children }: {
  label: string; icon: keyof typeof ICONS; value: ReactNode; sub?: ReactNode; tone?: "accent" | "danger" | "success"; children?: ReactNode;
}) {
  const tint = { accent: "bg-accent-soft text-accent", danger: "bg-danger-soft text-danger", success: "bg-success-soft text-success" }[tone];
  return (
    <Card className={`min-w-0 gap-1.5 ${tone === "danger" ? "ring-1 ring-danger" : ""}`}>
      <div className="flex items-center gap-2">
        <span className="mr-auto text-sm text-muted">{label}</span>
        <span className={`grid size-8 place-items-center rounded-full ${tint}`}><Icon name={icon} className="size-4" /></span>
      </div>
      {value === undefined ? <Skeleton className="h-8 w-20 rounded-lg" />
        : <span className={`num truncate text-[1.75rem] leading-8 font-semibold tracking-tight ${tone === "danger" ? "text-danger" : ""}`}>{value}</span>}
      <span className="truncate text-xs text-muted">{sub ?? "\u00a0"}</span>
      {children}
    </Card>
  );
}

export function Online({ live, error, alertRate, profiles, onSession }: {
  live?: Live; error: string; alertRate: number; profiles: string[]; onSession: (s: LiveRow) => void;
}) {
  const [saved, setRange] = usePref<Preset>("range", "24h");
  const range: Preset = CHART_PRESETS.includes(saved) ? saved : "24h";
  const [heatDays, setHeatDays] = usePref<"7d" | "30d" | "90d">("heat", "30d");
  // short ranges move fast: refresh the charts more often for them
  const series = useJSON<Series>(`series-${range}`, () => `api/series?${rangeParams({ preset: range })}`, PRESETS[range] <= H ? 5000 : 60000);
  const heat = useJSON<Heat>(`heat-${heatDays}`, () => `api/heatmap?${rangeParams({ preset: heatDays })}`, 10 * M);

  const [q, setQ] = useState("");
  const [profile, setProfile] = useState("");
  const [onlyAlerts, setOnlyAlerts] = useState(false);
  const [sort, setSort] = usePref<Sort>("sort.online", { k: "profile", dir: 1 });
  const [hidden, setHidden] = usePref<string[]>("hidden.online", ["since", "cipher"]);

  const insts = live?.instances ?? [];
  const all: LiveRow[] = insts.flatMap((i) => i.sessions.map((s) => ({ ...s, instance: i.name, proto: i.proto, profile: i.profile })));
  const alerting = (s: LiveRow) => alertRate > 0 && s.rate_rx + s.rate_tx > alertRate;
  // Each profile has its own PKI: the same CN twice inside one profile looks like a shared certificate.
  const perCN = new Map<string, number>();
  for (const s of all) perCN.set(`${s.profile}\n${s.cn}`, (perCN.get(`${s.profile}\n${s.cn}`) ?? 0) + 1);
  const copies = (s: LiveRow) => perCN.get(`${s.profile}\n${s.cn}`) ?? 1;
  const down = insts.filter((i) => !i.up);
  const nAlerts = all.filter(alerting).length;

  // a response for another range must not be drawn on this range's axis
  const points = series.loading ? [] : (series.data?.points ?? []).map((p) => ({ ...p, t: Date.parse(p.t), n: sum(Object.values(p.online), (v) => v) }));
  const from = series.data ? Date.parse(series.data.from) : Date.now() - PRESETS[range];
  const to = series.data ? Date.parse(series.data.to) : Date.now();
  const step = (series.data?.step ?? 60) * 1000;
  const peak = points.reduce<(typeof points)[number] | undefined>((a, p) => (!a || p.n > a.n ? p : a), undefined);
  const chartProfiles = profiles.filter((p) => points.some((x) => x.online[p]));
  const empty = series.error || (series.loading ? "Loading…" : "No data yet — one sample is recorded per minute");

  const needle = q.trim().toLowerCase();
  const cmpKey = sortKeys[sort.k] ?? sortKeys.cn;
  const rows = all
    .filter((s) => (!profile || s.profile === profile) && (!onlyAlerts || alerting(s)) && (!needle ||
      [s.cn, s.vpn_ip, s.vpn_ip6, s.real_ip, s.profile, s.country, s.country && countryName(s.country), s.device?.plat && osName(s.device), appName(s.device)].some((v) => v?.toLowerCase().includes(needle))))
    .sort((a, b) => {
      const x = cmpKey(a), y = cmpKey(b);
      const r = typeof x === "number" ? x - (y as number) : String(x).localeCompare(String(y), undefined, { numeric: true });
      return r * sort.dir || a.profile.localeCompare(b.profile) || a.cn.localeCompare(b.cn);
    });

  const cols: Col<LiveRow>[] = [
    { id: "cn", label: "CN", cell: (s) => (
      <span className="flex items-center gap-1.5">
        <span className="font-semibold">{s.cn}</span>
        {copies(s) > 1 && <Badge tone="warning" title={`${s.cn} is online ${copies(s)} times in ${s.profile}: possible shared certificate`}>×{copies(s)}</Badge>}
        {alerting(s) && <Badge tone="danger" title={`rate ${rate(s.rate_rx + s.rate_tx)} > ${rate(alertRate)}`}>⚠ rate</Badge>}
      </span>) },
    { id: "profile", label: "Profile", cell: (s) => <ProfileTag name={s.profile} profiles={profiles} /> },
    { id: "proto", label: "Proto", cell: (s) => s.proto.toUpperCase() },
    { id: "vpn", label: "VPN IP", cell: (s) => <span className="mono">{s.vpn_ip}{s.vpn_ip6 && <span className="block text-xs text-muted">{s.vpn_ip6}</span>}</span> },
    { id: "real", label: "Real IP:port", cell: (s) => <><CountryTag cc={s.country} /><span className="mono">{addr(s.real_ip, s.real_port)}</span></> },
    { id: "device", label: "Device", cell: (s) => <DeviceTag device={s.device} /> },
    { id: "since", label: "Connected", cell: (s) => <span title={F.abs.format(Date.parse(s.connected_since))}>{relative(Date.parse(s.connected_since))}</span> },
    { id: "dur", label: "Duration", num: true, cell: (s) => duration((Date.now() - Date.parse(s.connected_since)) / 1000) },
    { id: "traffic", label: "Traffic ↑ / ↓", num: true, cell: (s) => `${bytes(s.bytes_rx)} / ${bytes(s.bytes_tx)}` },
    { id: "rate", label: "Rate ↑ / ↓", num: true, cell: (s) => `${rate(s.rate_rx)} / ${rate(s.rate_tx)}` },
    { id: "seen", label: "Last seen", cell: (s) => {
      if (!s.last_ref) return <span className="text-muted">–</span>;
      const t = Date.parse(s.last_ref);
      return <span title={F.abs.format(t)} className={Date.now() - t > M ? "text-muted" : ""}>{relative(t)}</span>;
    } },
    { id: "cipher", label: "Cipher", cell: (s) => <span className="text-muted">{s.cipher}</span> },
  ];

  return (
    <div className="grid min-w-0 gap-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Kpi label="Online now" icon="online" value={live ? all.length : undefined} sub={live && `${new Set(all.map((s) => s.cn)).size} users`}>
          <Sparkline values={points.map((p) => p.n)} />
        </Kpi>
        <Kpi label="Instances up" icon="server" tone={down.length ? "danger" : "success"} value={live ? `${insts.length - down.length} / ${insts.length}` : undefined}
          sub={live && (down.length ? <span className="text-danger" title={down.map((i) => i.name).join("\n")}>down: {down.map((i) => i.name).join(", ")}</span> : "all up")} />
        <Kpi label="Throughput now" icon="speed" value={live ? rate(sum(all, (s) => s.rate_rx + s.rate_tx)) : undefined}
          sub={live && `↑ ${rate(sum(all, (s) => s.rate_rx))} · ↓ ${rate(sum(all, (s) => s.rate_tx))}`}>
          <Sparkline values={points.map((p) => p.rx + p.tx)} />
        </Kpi>
        <Kpi label={`Peak, last ${range}`} icon="peak" value={series.loading ? undefined : peak?.n ?? "–"} sub={peak && `sessions at ${F.dmhm.format(peak.t)}`} />
        <Kpi label="Alerts now" icon="alert" tone={nAlerts > 0 ? "danger" : "success"} value={live ? nAlerts : undefined}
          sub={alertRate ? `sessions above ${rate(alertRate)}` : "alerts off"} />
      </div>

      <div className={`grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-3 ${error ? "stale" : ""}`}>
        {profiles.filter((p) => insts.some((i) => i.profile === p)).map((p) => (
          <Card key={p} className="gap-2 py-3">
            <div className="flex items-center gap-2">
              <i className="size-2.5 shrink-0 rounded-full" style={{ background: seriesColor(profiles.indexOf(p)) }} />
              <span className="mr-auto truncate text-sm font-semibold" title={p}>{p}</span>
              <span className="num text-xs whitespace-nowrap text-muted">{sum(insts.filter((i) => i.profile === p), (i) => i.sessions.length)} online</span>
            </div>
            <div className="flex flex-wrap gap-1.5">
            {insts.filter((i) => i.profile === p).map((i) => (
              <Tooltip key={i.name} delay={150}>
                <Tooltip.Trigger>
                  <span tabIndex={0} className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${i.up ? "bg-default" : "bg-danger-soft text-danger"}`}>
                    <i className={`inline-block size-1.5 rounded-full ${i.up ? "bg-success" : "bg-danger"}`} />
                    {i.proto.toUpperCase()} <b className="num">{i.up ? i.sessions.length : "down"}</b>
                  </span>
                </Tooltip.Trigger>
                <Tooltip.Content>
                  <div className="text-xs">
                    <b>{i.name}</b>
                    <div>{i.up ? `OpenVPN ${i.version ?? "?"}` : `DOWN: ${i.last_error}`}</div>
                    {i.last_ok && <div>last {i.up ? "poll" : "OK"} {F.abs.format(Date.parse(i.last_ok))}</div>}
                  </div>
                </Tooltip.Content>
              </Tooltip>
            ))}
            </div>
          </Card>
        ))}
      </div>

      <Panel title="Activity" actions={<Segmented label="Chart range" value={range} options={CHART_PRESETS} onChange={(v) => setRange(v as Preset)} />}>
        <div className="grid gap-6 xl:grid-cols-2">
          <figure className="min-w-0">
            <figcaption className="mb-2 flex flex-wrap items-center gap-x-4 text-sm text-muted">
              Online sessions by profile
              <Legend items={chartProfiles.map((p) => ({ name: p, color: seriesColor(profiles.indexOf(p)), value: String(points.at(-1)?.online[p] ?? 0) }))} />
            </figcaption>
            <TimeChart key={rangeKey({ preset: range })} label="Online sessions" stacked integer floor={4} from={from} to={to} step={step} empty={empty}
              points={chartProfiles.length ? points.map((p) => ({ t: p.t, v: chartProfiles.map((n) => p.online[n] ?? 0) })) : points.map((p) => ({ t: p.t, v: [0] }))}
              series={chartProfiles.length ? chartProfiles.map((p) => ({ name: p, color: seriesColor(profiles.indexOf(p)) })) : [{ name: "Online", color: "var(--s1)" }]}
              fmt={(v) => String(Math.round(v))} />
          </figure>
          <figure className="min-w-0">
            <figcaption className="mb-2 flex flex-wrap items-center gap-x-4 text-sm text-muted">
              Throughput
              <Legend items={[{ name: "↑ from clients", color: "var(--s1)" }, { name: "↓ to clients", color: "var(--s2)" }]} />
            </figcaption>
            <TimeChart label="Throughput" floor={1024} base={1024} from={from} to={to} step={step} empty={empty}
              points={points.map((p) => ({ t: p.t, v: [p.rx, p.tx] }))}
              series={[{ name: "↑ from clients", color: "var(--s1)" }, { name: "↓ to clients", color: "var(--s2)" }]}
              fmt={(v) => rate(v).replace(/\.0+ /, " ")} />
          </figure>
        </div>
      </Panel>

      <Panel title="Sessions" flush actions={<>
        <Toggle label="Alerts only" value={onlyAlerts} onChange={setOnlyAlerts} />
        <Search value={q} onChange={setQ} placeholder="CN, IP, country, profile or device" />
        <ColumnMenu cols={cols} hidden={hidden} onChange={setHidden} />
      </>}>
        <div className="px-4 pb-3">
          <ProfileTabs profiles={profiles} value={profile} onChange={setProfile}
            counts={Object.fromEntries(profiles.filter((p) => insts.some((i) => i.profile === p)).map((p) => [p, all.filter((s) => s.profile === p).length]))} />
        </div>
        <DataTable label="Online sessions" cols={cols.filter((c) => !hidden.includes(c.id))} rows={rows} stale={!!error}
          accent={(s) => profileColor(profiles, s.profile)}
          rowId={(s) => `${s.instance}|${s.cid}|${s.connected_since}`} sort={sort} onSort={setSort}
          descFirst={["dur", "traffic", "rate", "seen", "since"]}
          rowClass={(s) => (alerting(s) ? "row-alert" : copies(s) > 1 ? "row-warn" : "")} onRow={onSession}
          empty={!live ? (error ? "No data" : "Loading…") : q || profile || onlyAlerts ? "No session matches the filter" : "Nobody is online"} />
      </Panel>

      <Panel title="Usual load by weekday and hour" actions={<Segmented label="Heatmap range" value={heatDays} options={["7d", "30d", "90d"]} onChange={(v) => setHeatDays(v as "7d")} />}>
        {heat.data?.cells.length ? <Heatmap cells={heat.data.cells} />
          : <div className="py-8 text-center text-sm text-muted">{heat.error || (heat.loading ? "Loading…" : "No data yet")}</div>}
      </Panel>
    </div>
  );
}
