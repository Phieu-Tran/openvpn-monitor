import { parseDate, today } from "@internationalized/date";
import { Button, Card, DateField, DateRangePicker, Pagination, RangeCalendar } from "@heroui/react";
import { useEffect, useState } from "react";
import { F, PRESETS, TZ, addr, appName, bytes, countryName, duration, platName, rangeKey, rangeMs, rangeParams, rate, relative, useDebounced, useJSON, usePref } from "./lib";
import type { DeviceUse, Facets, History, Preset, Range, SessionRecord, UserRow, Users } from "./lib";
import { Badge, CountryCards, CountryTag, DataTable, DeviceIcon, DeviceTag, Icon, Panel, Picker, ProfileTag, Search, Segmented, ServerTag, Toggle, profileColor } from "./ui";
import type { Col, Sort } from "./ui";

// server is chosen in the bar above every view, not in the filter bar.
export interface Filters { range: Range; q: string; server: string; profile: string; proto: string; country: string; plat: string; alerts: boolean }

const PAGE = 50;

function params(f: Filters, extra: Record<string, string> = {}) {
  const p = rangeParams(f.range);
  for (const [k, v] of Object.entries({ q: f.q, server: f.server, profile: f.profile, proto: f.proto, country: f.country, plat: f.plat, alerts: f.alerts ? "1" : "", ...extra })) {
    if (v) p.set(k, v);
  }
  return p;
}

export function DateRange({ range, onChange }: { range: Range; onChange: (r: Range) => void }) {
  const [from, to] = rangeMs(range);
  return (
    <DateRangePicker aria-label="Date range" maxValue={today(TZ)}
      value={{ start: parseDate(F.day.format(from)), end: parseDate(F.day.format(to)) }}
      onChange={(v) => v && onChange({ from: v.start.toString(), to: v.end.toString() })}>
      <DateField.Group>
        <DateField.Input slot="start">{(s) => <DateField.Segment segment={s} />}</DateField.Input>
        <DateRangePicker.RangeSeparator />
        <DateField.Input slot="end">{(s) => <DateField.Segment segment={s} />}</DateField.Input>
        <DateField.Suffix>
          <DateRangePicker.Trigger><DateRangePicker.TriggerIndicator /></DateRangePicker.Trigger>
        </DateField.Suffix>
      </DateField.Group>
      <DateRangePicker.Popover>
        <RangeCalendar aria-label="Date range">
          <RangeCalendar.Header>
            <RangeCalendar.Heading />
            <RangeCalendar.NavButton slot="previous" />
            <RangeCalendar.NavButton slot="next" />
          </RangeCalendar.Header>
          <RangeCalendar.Grid>
            <RangeCalendar.GridHeader>{(day) => <RangeCalendar.HeaderCell>{day}</RangeCalendar.HeaderCell>}</RangeCalendar.GridHeader>
            <RangeCalendar.GridBody>{(date) => <RangeCalendar.Cell date={date} />}</RangeCalendar.GridBody>
          </RangeCalendar.Grid>
        </RangeCalendar>
      </DateRangePicker.Popover>
    </DateRangePicker>
  );
}

const HISTORY_PRESETS: Preset[] = ["24h", "7d", "30d", "90d"];

function FilterBar({ filters: f, set, facets, csv }: { filters: Filters; set: (f: Filters) => void; facets?: Facets; csv: { label: string; url: () => string } }) {
  const active = [f.q, f.profile, f.proto, f.country, f.plat, f.alerts].filter(Boolean).length;
  return (
    <Card className="gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <Segmented label="Time range" value={"preset" in f.range ? f.range.preset : ""} options={HISTORY_PRESETS} onChange={(v) => set({ ...f, range: { preset: v as Preset } })} />
        <DateRange range={f.range} onChange={(range) => set({ ...f, range })} />
        <span className="ml-auto">
          <Button size="sm" variant="secondary" onPress={() => location.assign(csv.url())}><Icon name="download" className="size-4" />{csv.label}</Button>
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Search value={f.q} onChange={(q) => set({ ...f, q })} placeholder="Filter by CN or IP" className="w-56 max-sm:w-full" />
        <Picker label="Profile" all="All profiles" value={f.profile} options={facets?.profiles ?? []} onChange={(profile) => set({ ...f, profile })} />
        <Picker label="Protocol" all="UDP + TCP" value={f.proto} options={["udp", "tcp"]} name={(v) => v.toUpperCase()} onChange={(proto) => set({ ...f, proto })} />
        <Picker label="Country" all="All countries" value={f.country} options={facets?.countries ?? []} onChange={(country) => set({ ...f, country })}
          name={(c) => (c === "LAN" ? countryName(c) : `${c} · ${countryName(c)}`)} />
        <Picker label="Device" all="All devices" value={f.plat} options={[...(facets?.plats ?? []), "unknown"]}
          name={(p) => (p === "unknown" ? "Unknown device" : platName(p))} onChange={(plat) => set({ ...f, plat })} />
        <Toggle label="Alerts only" value={f.alerts} onChange={(alerts) => set({ ...f, alerts })} />
        {active > 0 && (
          <Button size="sm" variant="ghost" onPress={() => set({ ...f, q: "", profile: "", proto: "", country: "", plat: "", alerts: false })}>
            Clear {active} filter{active > 1 ? "s" : ""}
          </Button>
        )}
      </div>
    </Card>
  );
}

export function Pager({ page, total, onPage }: { page: number; total: number; onPage: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / PAGE));
  if (total <= PAGE) return null;
  return (
    <Pagination className="px-4 pt-2">
      <Pagination.Summary>{page * PAGE + 1}–{Math.min(total, (page + 1) * PAGE)} of {total.toLocaleString("en-GB")}</Pagination.Summary>
      <Pagination.Content>
        <Pagination.Item>
          <Pagination.Previous isDisabled={page === 0} onPress={() => onPage(page - 1)}><Pagination.PreviousIcon /><span>Previous</span></Pagination.Previous>
        </Pagination.Item>
        <Pagination.Item><span className="px-2 text-sm text-muted">Page {page + 1} / {pages.toLocaleString("en-GB")}</span></Pagination.Item>
        <Pagination.Item>
          <Pagination.Next isDisabled={page >= pages - 1} onPress={() => onPage(page + 1)}><span>Next</span><Pagination.NextIcon /></Pagination.Next>
        </Pagination.Item>
      </Pagination.Content>
    </Pagination>
  );
}

// usePaged runs one filtered, sorted, paged query; the database does the work.
// extra: more query parameters, part of the key (a new value starts at the first page).
export function usePaged<T>(path: string, filters: Filters, sort: Sort, extra: Record<string, string> = {}) {
  const q = useDebounced(filters.q.trim());
  const f = { ...filters, q };
  const base = JSON.stringify([rangeKey(f.range), q, f.server, f.profile, f.proto, f.country, f.plat, f.alerts, sort, extra]);
  const [page, setPage] = useState({ base, n: 0 });
  const n = page.base === base ? page.n : 0; // new filters start at the first page
  useEffect(() => { if (page.base !== base) setPage({ base, n: 0 }); }, [base, page.base]);
  const order = { sort: sort.k, dir: sort.dir > 0 ? "asc" : "desc" };
  const result = useJSON<T>(`${path}|${base}|${n}`, () => `${path}?${params(f, { ...extra, ...order, limit: String(PAGE), offset: String(n * PAGE) })}`, 30000);
  return { ...result, page: n, setPage: (v: number) => setPage({ base, n: v }), csvURL: () => `${path}.csv?${params(f, { ...extra, ...order })}` };
}

// Devices: every kind of device (OS + app) that connected in the filtered sessions.
// A card filters the history by its OS; again clears it.
function Devices({ list, plat, loading, onPlat }: { list?: DeviceUse[]; plat: string; loading: boolean; onPlat: (p: string) => void }) {
  if (!list) return null;
  return (
    <Panel title="Devices that connected" actions={<span className="text-xs text-muted">reported by the client app (OpenVPN peer info), not verified</span>}>
      {!list.length ? <p className={`text-sm text-muted ${loading ? "stale" : ""}`}>No device reported in this range.</p> : (
        <ul className={`grid grid-cols-[repeat(auto-fill,minmax(230px,1fr))] gap-2 ${loading ? "stale" : ""}`}>
          {list.map((d) => (
            <li key={d.plat + d.gui}>
              <button type="button" aria-pressed={plat === d.plat} onClick={() => onPlat(plat === d.plat ? "" : d.plat)}
                className={`flex w-full cursor-pointer items-center gap-3 rounded-xl border px-3 py-2 text-left transition-colors ${plat === d.plat ? "border-accent bg-accent-soft" : "border-border hover:bg-default"}`}>
                <DeviceIcon plat={d.plat} className="size-6" />
                <span className="min-w-0 grow">
                  <span className="block text-sm font-medium">{platName(d.plat)}</span>
                  <span className="block truncate text-xs text-muted" title={d.gui}>{appName(d) || "–"}</span>
                  <span className="num block text-xs text-muted">{d.users} {d.users === 1 ? "user" : "users"} · {d.sessions} sessions · {relative(Date.parse(d.last))}</span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

interface PageProps { filters: Filters; setFilters: (f: Filters) => void; facets?: Facets; alertRate: number; onUser: (cn: string) => void; profiles: string[]; multi: boolean }

export function Sessions({ filters, setFilters, facets, alertRate, onUser, profiles, multi }: PageProps) {
  const [sort, setSort] = usePref<Sort>("sort.history", { k: "start", dir: -1 });
  const r = usePaged<History>("api/history", filters, sort);
  const s = r.data?.summary;
  const alerting = (x: SessionRecord) => alertRate > 0 && (x.peak_rate ?? 0) > alertRate;
  const end = (x: SessionRecord) => (x.open ? Date.now() : Date.parse(x.end));
  const cols: Col<SessionRecord>[] = [
    { id: "cn", label: "CN", cell: (x) => (
      <span className="flex items-center gap-1.5">
        <span className="font-semibold">{x.cn}</span>
        {alerting(x) && <Badge tone="danger" title={`peak rate ${rate(x.peak_rate!)} > ${rate(alertRate)}`}>⚠ peak</Badge>}
      </span>) },
    ...(multi ? [{ id: "server", label: "Server", cell: (x: SessionRecord) => <ServerTag name={x.server} /> }] : []),
    { id: "profile", label: "Profile", cell: (x) => <ProfileTag name={x.profile} profiles={profiles} /> },
    { id: "proto", label: "Proto", cell: (x) => x.proto.toUpperCase() },
    { id: "real", label: "Real IP", cell: (x) => <><CountryTag cc={x.country} /><span className="mono">{addr(x.real_ip, x.real_port)}</span></> },
    { id: "vpn", label: "VPN IP", cell: (x) => <span className="mono">{x.vpn_ip}</span> },
    { id: "device", label: "Device", cell: (x) => <DeviceTag device={x.device} /> },
    { id: "start", label: "Start", cell: (x) => <span title={F.abs.format(Date.parse(x.start))}>{F.dmhm.format(Date.parse(x.start))}</span> },
    { id: "end", label: "End", cell: (x) => (x.open ? <Badge tone="success">online now</Badge> : <span title={F.abs.format(end(x))}>{F.dmhm.format(end(x))}</span>) },
    { id: "dur", label: "Duration", num: true, cell: (x) => duration((end(x) - Date.parse(x.start)) / 1000) },
    { id: "traffic", label: "Traffic ↑ / ↓", num: true, cell: (x) => `${bytes(x.bytes_rx)} / ${bytes(x.bytes_tx)}` },
  ];
  return (
    <div className="grid min-w-0 gap-4">
      <FilterBar filters={filters} set={setFilters} facets={facets} csv={{ label: "Export sessions CSV", url: r.csvURL }} />
      {r.data && (
        <Panel title="Countries" actions={<span className="text-xs text-muted">of the real IP (offline GeoIP database), users who connected in this range</span>}>
          {r.data.countries.length ? <CountryCards items={r.data.countries} value={filters.country} stale={r.loading} onPick={(country) => setFilters({ ...filters, country })} />
            : <p className="text-sm text-muted">No sessions in this range.</p>}
        </Panel>
      )}
      <Devices list={r.data?.devices} plat={filters.plat} loading={r.loading} onPlat={(plat) => setFilters({ ...filters, plat })} />
      <Panel title="Sessions" flush actions={s && !r.loading && (
        <span className="num text-sm text-muted">
          {s.total.toLocaleString("en-GB")} sessions · {s.users} {s.users === 1 ? "user" : "users"} · {duration(s.online_seconds)} online · ↑ {bytes(s.bytes_rx)} / ↓ {bytes(s.bytes_tx)} · {s.alerts} with alerts
        </span>)}>
        <DataTable label="Session history" cols={cols} rows={r.data?.sessions ?? []} stale={r.loading || !!r.error}
          rowId={(x) => `${x.instance}|${x.cid}|${x.start}`} sort={sort} onSort={setSort} descFirst={["start", "end", "dur", "traffic"]}
          rowClass={(x) => (alerting(x) ? "row-alert" : "")} onRow={(x) => onUser(x.cn)} accent={(x) => profileColor(profiles, x.profile)} empty={r.error || (!r.data ? "Loading…" : "No sessions match")} />
        <Pager page={r.page} total={s?.total ?? 0} onPage={r.setPage} />
      </Panel>
    </div>
  );
}

export function UserList({ filters, setFilters, facets, alertRate, onUser, profiles, multi }: PageProps) {
  const [sort, setSort] = usePref<Sort>("sort.users", { k: "last", dir: -1 });
  const r = usePaged<Users>("api/users", filters, sort);
  const s = r.data?.summary;
  const alerting = (u: UserRow) => alertRate > 0 && u.peak_rate > alertRate;
  const cols: Col<UserRow>[] = [
    { id: "cn", label: "CN", cell: (u) => (
      <span className="flex items-center gap-1.5">
        <span className="font-semibold">{u.cn}</span>
        {alerting(u) && <Badge tone="danger" title={`peak rate ${rate(u.peak_rate)} > ${rate(alertRate)}`}>⚠ peak</Badge>}
      </span>) },
    ...(multi ? [{ id: "servers", label: "Servers", cell: (u: UserRow) => <span className="flex gap-1">{u.servers.map((s) => <ServerTag key={s} name={s} />)}</span> }] : []),
    { id: "profiles", label: "Profiles", cell: (u) => <span className="flex gap-1">{u.profiles.map((p) => <ProfileTag key={p} name={p} profiles={profiles} />)}</span> },
    { id: "n", label: "Sessions", num: true, cell: (u) => u.sessions },
    { id: "time", label: "Time online", num: true, cell: (u) => duration(u.online_seconds) },
    { id: "traffic", label: "Traffic ↑ / ↓", num: true, cell: (u) => `${bytes(u.bytes_rx)} / ${bytes(u.bytes_tx)}` },
    { id: "countries", label: "Countries", cell: (u) => u.countries.map((c) => <CountryTag key={c} cc={c} />) },
    { id: "ips", label: "Real IPs", num: true, cell: (u) => u.ips },
    { id: "plats", label: "Devices", cell: (u) => <span className="flex items-center gap-1.5">{u.plats.length ? u.plats.map((p) => <DeviceIcon key={p} plat={p} />) : <span className="text-muted">–</span>}</span> },
    { id: "last", label: "Last seen", cell: (u) => (u.online ? <Badge tone="success">online now</Badge>
      : <span title={F.abs.format(Date.parse(u.last_seen))}>{relative(Date.parse(u.last_seen))}</span>) },
  ];
  return (
    <div className="grid min-w-0 gap-4">
      <FilterBar filters={filters} set={setFilters} facets={facets} csv={{ label: "Export users CSV", url: r.csvURL }} />
      <Panel title="Users" flush actions={s && !r.loading && (
        <span className="num text-sm text-muted">{s.total} users · {s.online} online now · {s.alerts} with alerts</span>)}>
        <DataTable label="Users" cols={cols} rows={r.data?.users ?? []} stale={r.loading || !!r.error} rowId={(u) => u.cn}
          sort={sort} onSort={setSort} descFirst={["n", "time", "traffic", "ips", "last"]}
          rowClass={(u) => (alerting(u) ? "row-alert" : "")} onRow={(u) => onUser(u.cn)} empty={r.error || (!r.data ? "Loading…" : "No users match")} />
        <Pager page={r.page} total={s?.total ?? 0} onPage={r.setPage} />
      </Panel>
    </div>
  );
}
