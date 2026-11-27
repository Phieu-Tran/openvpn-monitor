// The access log: which user opened connections to which IP:port, when (agents with OVPN_FLOWS).
import { Alert, Button, Card } from "@heroui/react";
import { useEffect, useState } from "react";
import { DateRange, Pager, usePaged } from "./History";
import type { Filters } from "./History";
import { F, PRESETS, addr, usePref } from "./lib";
import type { Access, AccessRow, Preset, Range, Status } from "./lib";
import { DataTable, Icon, Panel, Picker, ProfileTag, Search, Segmented, ServerTag, profileColor } from "./ui";
import type { Col, Sort } from "./ui";

const ACCESS_PRESETS: Preset[] = ["1h", "24h", "7d", "30d", "90d"];

export function AccessLog({ server, multi, profiles, agents, linked, onUser }: {
  server: string; multi: boolean; profiles: string[]; agents: Status["agents"]; linked: string | null; onUser: (cn: string) => void;
}) {
  const [preset, setPreset] = usePref<Preset>("apreset", "24h");
  const [range, setRange] = useState<Range>({ preset: ACCESS_PRESETS.includes(preset) && preset in PRESETS ? preset : "24h" });
  const [q, setQ] = useState(linked ?? "");
  useEffect(() => { if (linked !== null) setQ(linked); }, [linked]); // #access?q=name from a user's details
  const [proto, setProto] = useState("");
  const [sort, setSort] = usePref<Sort>("sort.access", { k: "time", dir: -1 });
  const filters: Filters = { range, q, server, proto, profile: "", country: "", plat: "", alerts: false };
  const r = usePaged<Access>("api/access", filters, sort);
  const s = r.data?.summary;

  // agents where the log is not running: say so, with the reason
  const off = agents.filter((a) => (!server || a.name === server) && a.flows !== "on");
  const why = (f?: string) => (!f ? "agent not answering" : f === "off" ? "OVPN_FLOWS not set on the agent (or agent older than 0.1.7)" : f);

  const cols: Col<AccessRow>[] = [
    { id: "time", label: "Time", cell: (x) => {
      const a = Date.parse(x.first), b = Date.parse(x.last);
      return <span className="num whitespace-nowrap" title={`first ${F.abs.format(a)}\nlast ${F.abs.format(b)}`}>{F.abs.format(a)}{b !== a && <span className="text-muted"> → {F.clock.format(b)}</span>}</span>;
    } },
    { id: "cn", label: "User", cell: (x) => (x.cn ? <span className="font-semibold">{x.cn}</span>
      : <span className="text-muted" title="No session had this VPN IP at that time">unknown</span>) },
    ...(multi ? [{ id: "server", label: "Server", cell: (x: AccessRow) => <ServerTag name={x.server} /> }] : []),
    { id: "profile", label: "Profile", cell: (x) => (x.profile ? <ProfileTag name={x.profile} profiles={profiles} /> : null) },
    { id: "src", label: "VPN IP", cell: (x) => <span className="mono">{x.src}</span> },
    { id: "dst", label: "Destination", cell: (x) => <span className="mono font-medium">{addr(x.dst, x.port)}</span> },
    { id: "proto", label: "Proto", cell: (x) => x.proto.toUpperCase() },
    { id: "n", label: "Connections", num: true, cell: (x) => x.n },
  ];

  return (
    <div className="grid min-w-0 gap-4">
      {off.map((a) => (
        <Alert key={a.name} status={a.flows && a.flows !== "off" ? "danger" : "warning"}>
          <Alert.Indicator />
          <Alert.Content><Alert.Title>Access log not recorded{multi ? ` on ${a.name}` : ""}: {why(a.flows)}</Alert.Title></Alert.Content>
        </Alert>
      ))}
      <Card className="gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Segmented label="Time range" value={"preset" in range ? range.preset : ""} options={ACCESS_PRESETS}
            onChange={(v) => { setRange({ preset: v as Preset }); setPreset(v as Preset); }} />
          <DateRange range={range} onChange={setRange} />
          <span className="ml-auto">
            <Button size="sm" variant="secondary" onPress={() => location.assign(r.csvURL())}><Icon name="download" className="size-4" />Export access CSV</Button>
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Search value={q} onChange={setQ} placeholder="User, VPN IP, IP or IP:port (e.g. :3389)" className="w-80 max-sm:w-full" />
          <Picker label="Protocol" all="TCP + UDP" value={proto} options={["tcp", "udp"]} name={(v) => v.toUpperCase()} onChange={setProto} />
          <span className="text-xs text-muted">New TCP/UDP connections from VPN clients, counted per user, destination and minute. IP:port only: no URLs, no content.</span>
        </div>
      </Card>
      <Panel title="Access log" flush actions={s && !r.loading && (
        <span className="num text-sm text-muted">
          {s.rows.toLocaleString("en-GB")} rows · {s.connections.toLocaleString("en-GB")} connections · {s.users} {s.users === 1 ? "user" : "users"} · {s.destinations} destinations
        </span>)}>
        <DataTable label="Access log" cols={cols} rows={r.data?.access ?? []} stale={r.loading || !!r.error}
          rowId={(x) => `${x.server}|${x.first}|${x.src}|${x.dst}|${x.port}|${x.proto}`} sort={sort} onSort={setSort} descFirst={["time", "n"]}
          accent={(x) => (x.profile ? profileColor(profiles, x.profile) : "transparent")}
          onRow={(x) => x.cn && onUser(x.cn)} empty={r.error || (!r.data ? "Loading…" : "No connections match")} />
        <Pager page={r.page} total={s?.rows ?? 0} onPage={r.setPage} />
      </Panel>
    </div>
  );
}
