// Thin wrappers around HeroUI so the pages read as content, not markup.
import {
  Button, Card, Chip, Dropdown, Label, ListBox, SearchField, Select, Switch, Table, ToggleButton, ToggleButtonGroup, toast,
} from "@heroui/react";
import type { ReactNode } from "react";
import { siAndroid, siApple, siLinux } from "simple-icons";
import { seriesColor } from "./charts";
import { appName, bytes, countryName, flagEmoji, osName, platName } from "./lib";
import type { Device } from "./lib";

// A profile keeps one colour everywhere (charts, tags, row accents): its position in the
// sorted list of all known profiles.
export const profileColor = (profiles: string[], p: string) => seriesColor(profiles.indexOf(p));

export function ProfileTag({ name, profiles }: { name: string; profiles: string[] }) {
  const c = profileColor(profiles, name);
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border px-1.5 py-px text-xs font-medium whitespace-nowrap"
      style={{ background: `color-mix(in oklab, ${c} 14%, transparent)`, borderColor: `color-mix(in oklab, ${c} 38%, transparent)` }}>
      <i className="size-2 rounded-full" style={{ background: c }} />{name}
    </span>
  );
}

// ProfileTabs: one tab per profile with its count, "" = all. Filters a table at a glance.
export function ProfileTabs({ profiles, counts, value, onChange }: {
  profiles: string[]; counts: Record<string, number>; value: string; onChange: (p: string) => void;
}) {
  const total = Object.values(counts).reduce((a, b) => a + b, 0);
  const tab = (id: string, label: ReactNode, n: number, color?: string) => (
    <button key={id || "*"} type="button" aria-pressed={value === id} onClick={() => onChange(id)}
      className={`flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-1.5 text-sm font-medium whitespace-nowrap transition-colors ${value === id ? "border-transparent text-foreground" : "border-border text-muted hover:text-foreground"}`}
      style={value === id ? { background: color ? `color-mix(in oklab, ${color} 18%, transparent)` : "var(--default)", boxShadow: `inset 0 -2px 0 ${color ?? "var(--accent)"}` } : undefined}>
      {color && <i className="size-2 rounded-full" style={{ background: color }} />}
      {label}
      <span className="num rounded-md bg-default px-1.5 text-xs text-muted">{n}</span>
    </button>
  );
  return (
    <div role="group" aria-label="Profile" className="flex gap-2 overflow-x-auto pb-1">
      {tab("", "All profiles", total)}
      {profiles.filter((p) => counts[p] !== undefined).map((p) => tab(p, p, counts[p], profileColor(profiles, p)))}
    </div>
  );
}

// ServerTag names the OpenVPN host (agent) a row belongs to.
export const ServerTag = ({ name }: { name: string }) => (
  <span className="inline-flex items-center gap-1 rounded-md bg-default px-1.5 py-px text-xs font-medium whitespace-nowrap" title={`Server ${name}`}>
    <Icon name="server" className="size-3.5 text-muted" />{name}
  </span>
);

// ServerSwitch: "" = all servers, else one; a dot per agent (green answers, red not), online counts.
export function ServerSwitch({ servers, value, onChange }: {
  servers: { name: string; ok?: boolean; error?: string; online?: number }[]; value: string; onChange: (s: string) => void;
}) {
  const total = servers.reduce((a, s) => a + (s.online ?? 0), 0);
  const pill = (id: string, label: ReactNode, n: number | undefined, dot?: boolean, title?: string) => (
    <button key={id || "*"} type="button" aria-pressed={value === id} onClick={() => onChange(id)} title={title}
      className={`flex cursor-pointer items-center gap-2 rounded-full border px-3 py-1 text-sm font-medium whitespace-nowrap transition-colors ${value === id ? "border-accent bg-accent-soft text-accent-soft-foreground" : "border-border text-muted hover:text-foreground"}`}>
      {dot !== undefined && <i className={`size-2 rounded-full ${dot ? "bg-success" : "bg-danger"}`} />}
      {label}
      {n !== undefined && <span className="num rounded-full bg-default px-1.5 text-xs">{n}</span>}
    </button>
  );
  return (
    <div role="group" aria-label="Server" className="flex items-center gap-2 overflow-x-auto">
      <Icon name="server" className="size-4 text-muted" />
      {pill("", "All servers", total)}
      {servers.map((s) => pill(s.name, s.name, s.online, s.ok, s.ok === false ? `agent ${s.name}: ${s.error ?? "not answering"}` : `agent ${s.name}`))}
    </div>
  );
}

export function Panel({ title, actions, children, flush }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; flush?: boolean }) {
  return (
    <Card className={`min-w-0 gap-3 ${flush ? "px-0 pb-2" : ""}`}>
      {(title || actions) && (
        <Card.Header className={`flex flex-row flex-wrap items-center gap-3 ${flush ? "px-4" : ""}`}>
          {title && <Card.Title className="mr-auto">{title}</Card.Title>}
          {actions}
        </Card.Header>
      )}
      <Card.Content className="min-w-0">{children}</Card.Content>
    </Card>
  );
}

export function Segmented({ label, value, options, onChange }: { label: string; value: string; options: readonly string[]; onChange: (v: string) => void }) {
  return (
    <ToggleButtonGroup aria-label={label} size="sm" selectionMode="single" disallowEmptySelection className="flex-wrap"
      selectedKeys={new Set(value ? [value] : [])}
      onSelectionChange={(keys) => { const [k] = keys; if (k !== undefined) onChange(String(k)); }}>
      {options.map((o) => <ToggleButton key={o} id={o}>{o}</ToggleButton>)}
    </ToggleButtonGroup>
  );
}

// Picker is a select whose first option, value "", means "all".
export function Picker({ label, all, value, options, onChange, name = (v) => v }: {
  label: string; all: string; value: string; options: string[]; onChange: (v: string) => void; name?: (v: string) => string;
}) {
  const items = [{ id: "*", text: all }, ...options.map((o) => ({ id: o, text: name(o) }))];
  return (
    <Select aria-label={label} className="w-40 max-sm:w-full" value={value || "*"} onChange={(k) => onChange(k === "*" || k === null ? "" : String(k))}>
      <Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger>
      <Select.Popover>
        <ListBox>
          {items.map((i) => <ListBox.Item key={i.id} id={i.id} textValue={i.text}>{i.text}<ListBox.ItemIndicator /></ListBox.Item>)}
        </ListBox>
      </Select.Popover>
    </Select>
  );
}

export function Search({ value, onChange, placeholder, autoFocus, className = "w-72 max-sm:w-full" }: {
  value: string; onChange: (v: string) => void; placeholder: string; autoFocus?: boolean; className?: string;
}) {
  return (
    <SearchField aria-label={placeholder} value={value} onChange={onChange} autoFocus={autoFocus} className={className}>
      <SearchField.Group>
        <SearchField.SearchIcon />
        <SearchField.Input placeholder={placeholder} className="w-full" />
        <SearchField.ClearButton />
      </SearchField.Group>
    </SearchField>
  );
}

export function Toggle({ label, value, onChange }: { label: string; value: boolean; onChange: (v: boolean) => void }) {
  return (
    <Switch size="sm" isSelected={value} onChange={onChange}>
      <Switch.Content><Switch.Control><Switch.Thumb /></Switch.Control><span className="text-sm whitespace-nowrap">{label}</span></Switch.Content>
    </Switch>
  );
}

export function ColumnMenu({ cols, hidden, onChange }: { cols: { id: string; label: string }[]; hidden: string[]; onChange: (hidden: string[]) => void }) {
  const optional = cols.filter((c) => c.id !== "cn");
  return (
    <Dropdown>
      <Button size="sm" variant="secondary">Columns</Button>
      <Dropdown.Popover>
        <Dropdown.Menu selectionMode="multiple" selectedKeys={new Set(optional.filter((c) => !hidden.includes(c.id)).map((c) => c.id))}
          onSelectionChange={(keys) => onChange(optional.filter((c) => keys !== "all" && !keys.has(c.id)).map((c) => c.id))}>
          {optional.map((c) => <Dropdown.Item key={c.id} id={c.id} textValue={c.label}><Dropdown.ItemIndicator /><Label>{c.label}</Label></Dropdown.Item>)}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown>
  );
}

export interface Sort { k: string; dir: 1 | -1 }
export interface Col<T> { id: string; label: string; num?: boolean; cell: (row: T) => ReactNode }

// DataTable: sortable; onRow makes the whole row open its details (click, or Enter on a focused row).
// accent: a colour bar at the start of each row (the row's profile).
export function DataTable<T>({ label, cols, rows, rowId, sort, onSort, descFirst, rowClass, empty, stale, onRow, accent }: {
  label: string; cols: Col<T>[]; rows: T[]; rowId: (r: T) => string; sort: Sort; onSort: (s: Sort) => void;
  descFirst: string[]; rowClass?: (r: T) => string; empty: string; stale?: boolean; onRow?: (r: T) => void; accent?: (r: T) => string;
}) {
  const items = rows.map((r) => ({ id: rowId(r), r }));
  return (
    <Table variant="secondary" className={`data-table ${onRow ? "clickable" : ""} ${stale ? "stale" : ""}`}>
      <Table.ScrollContainer>
        <Table.Content aria-label={label}
          onRowAction={onRow && ((key) => { const it = items.find((i) => i.id === key); if (it) onRow(it.r); })}
          sortDescriptor={{ column: sort.k, direction: sort.dir > 0 ? "ascending" : "descending" }}
          onSortChange={(d) => {
            const k = String(d.column);
            onSort({ k, dir: k === sort.k ? (sort.dir > 0 ? -1 : 1) : descFirst.includes(k) ? -1 : 1 });
          }}>
          <Table.Header columns={cols}>
            {(c) => (
              <Table.Column id={c.id} allowsSorting isRowHeader={c.id === "cn"} className={c.num ? "text-right" : ""}>
                {({ sortDirection }) => <Table.SortableColumnHeader sortDirection={sortDirection}>{c.label}</Table.SortableColumnHeader>}
              </Table.Column>
            )}
          </Table.Header>
          <Table.Body items={items} dependencies={[cols]} renderEmptyState={() => <div className="p-8 text-center text-muted">{empty}</div>}>
            {(item) => (
              <Table.Row id={item.id} columns={cols} className={rowClass?.(item.r) ?? ""}>
                {(c) => (
                  <Table.Cell className={c.num ? "num text-right" : ""}
                    style={accent && c.id === cols[0].id ? { boxShadow: `inset 3px 0 0 ${accent(item.r)}` } : undefined}>
                    {c.cell(item.r)}
                  </Table.Cell>
                )}
              </Table.Row>
            )}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  );
}

export function CountryTag({ cc }: { cc?: string }) {
  if (!cc) return <span className="mr-2 text-xs text-muted" title="Country unknown">··</span>;
  if (cc === "LAN") return <span className="mr-2 text-xs text-muted" title="Private / internal address">LAN</span>;
  return (
    <span className="mr-2 inline-flex items-center gap-1 text-xs font-semibold text-muted" title={countryName(cc)}>
      <span className="flag text-[15px] leading-none">{flagEmoji(cc)}</span>{cc}
    </span>
  );
}

// CountryCards: users and sessions per country (of the real IP), most users first, with the
// share of sessions as a bar. A card picks that country; again clears it. "" = unknown.
export function CountryCards({ items, value, onPick, stale }: {
  items: { country: string; users: number; sessions: number; bytes?: number }[]; value: string; onPick: (cc: string) => void; stale?: boolean;
}) {
  const total = items.reduce((a, c) => a + c.sessions, 0) || 1;
  return (
    <ul className={`grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-2 ${stale ? "stale" : ""}`}>
      {items.map((c) => {
        const pct = (c.sessions / total) * 100;
        const name = !c.country ? "Unknown" : c.country === "LAN" ? "LAN (private)" : countryName(c.country);
        return (
          <li key={c.country || "?"}>
            <button type="button" aria-pressed={value === c.country} disabled={!c.country} title={c.country ? `${name}: show only these` : "No country for these IPs"}
              onClick={() => onPick(value === c.country ? "" : c.country)}
              className={`grid w-full gap-1.5 rounded-xl border px-3 py-2 text-left transition-colors enabled:cursor-pointer ${value === c.country ? "border-accent bg-accent-soft" : "border-border enabled:hover:bg-default"}`}>
              <span className="flex items-center gap-2">
                <span className="flag w-6 text-xl leading-none">{c.country && c.country !== "LAN" ? flagEmoji(c.country) : ""}</span>
                <span className="min-w-0 grow truncate text-sm font-medium">{name}</span>
                <b className="num text-sm">{c.users}</b>
              </span>
              <span className="h-1 overflow-hidden rounded-full bg-default"><span className="block h-full rounded-full bg-accent" style={{ width: `${pct}%` }} /></span>
              <span className="num text-xs text-muted">
                {c.users} {c.users === 1 ? "user" : "users"} · {c.sessions} {c.sessions === 1 ? "session" : "sessions"} ({pct < 1 ? "<1" : Math.round(pct)}%){c.bytes !== undefined && ` · ${bytes(c.bytes)}`}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

export function CopyButton({ text }: { text: string }) {
  return (
    <Button size="sm" variant="ghost" aria-label={`Copy ${text}`} onPress={() => {
      // the clipboard needs a secure context: HTTPS or localhost
      if (!navigator.clipboard) return void toast("Copy needs HTTPS or localhost");
      navigator.clipboard.writeText(text).then(() => toast(`Copied ${text}`), () => toast("Copy was blocked by the browser"));
    }}>Copy</Button>
  );
}

export function Badge({ tone, children, title }: { tone: "success" | "danger" | "warning" | "default"; children: ReactNode; title?: string }) {
  return <span title={title} className="whitespace-nowrap"><Chip size="sm" variant="soft" color={tone}>{children}</Chip></span>;
}

// Line icons (24px grid, stroke), drawn inline: no icon font, nothing fetched.
export const ICONS = {
  online: "M3 12h4l3-8 4 16 3-8h4",
  history: "M12 7v5l3 2M3 12a9 9 0 1 0 3-6.7M3 4v4h4",
  users: "M16 20v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1M9.5 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7M21 20v-1a4 4 0 0 0-3-3.9M16 4.1a3.5 3.5 0 0 1 0 6.8",
  server: "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01",
  speed: "M7 17l5-5 4 4 5-5M16 11h5v5",
  peak: "M3 20h18M6 16l4-6 4 3 5-8",
  alert: "M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0",
  system: "M3 5h18v12H3zM8 21h8M12 17v4",
  sun: "M12 3v2M12 19v2M5 5l1.5 1.5M17.5 17.5 19 19M3 12h2M19 12h2M5 19l1.5-1.5M17.5 6.5 19 5M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8",
  moon: "M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14M20 20l-4-4",
  left: "M15 6l-6 6 6 6",
  download: "M12 4v11M7 10l5 5 5-5M5 20h14",
  right: "M9 6l6 6-6 6",
  device: "M5 5h14v10H5zM2 19h20M10 15v4M14 15v4",
  log: "M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01",
};
export const Icon = ({ name, className = "size-5" }: { name: keyof typeof ICONS; className?: string }) => (
  <svg viewBox="0 0 24 24" className={`${className} shrink-0`} fill="none" stroke="currentColor" strokeWidth={1.8}
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={ICONS[name]} /></svg>
);

// OS logos (Simple Icons, CC0; Windows drawn here). Brand colour where it reads on both themes,
// the text colour for the black/yellow ones.
const LOGOS: Record<string, { path: string; color: string }> = {
  win: { path: "M3 3h8.6v8.6H3zM12.4 3H21v8.6h-8.6zM3 12.4h8.6V21H3zM12.4 12.4H21V21h-8.6z", color: "#0078d4" },
  mac: { path: siApple.path, color: "currentColor" },
  ios: { path: siApple.path, color: "currentColor" },
  android: { path: siAndroid.path, color: "#3ddc84" },
  linux: { path: siLinux.path, color: "currentColor" },
};

export function DeviceIcon({ plat, className = "size-4" }: { plat?: string; className?: string }) {
  const logo = plat ? LOGOS[plat] : undefined;
  if (!logo) return <span title={platName(plat)} className="inline-flex text-muted"><Icon name="device" className={className} /></span>;
  return (
    <svg viewBox="0 0 24 24" className={`${className} shrink-0`} fill={logo.color} role="img" aria-label={platName(plat)}>
      <title>{platName(plat)}</title><path d={logo.path} />
    </svg>
  );
}

// DeviceTag: logo + OS, the app below; the full peer info in the tooltip.
export function DeviceTag({ device: d, compact }: { device?: Device; compact?: boolean }) {
  if (!d?.plat) return <span className="text-xs text-muted" title="The client's peer info was not in OpenVPN's log">–</span>;
  const app = appName(d);
  const title = [osName(d), d.plat === "linux" && d.plat_ver, app, d.ver && `OpenVPN core ${d.ver}`].filter(Boolean).join("\n");
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap" title={title}>
      <DeviceIcon plat={d.plat} />
      {compact ? <span className="text-sm">{osName(d)}</span>
        : <span className="leading-tight"><span className="block text-sm">{osName(d)}</span>{app && <span className="block text-xs text-muted">{app}</span>}</span>}
    </span>
  );
}
