// Connections that never finished their TLS handshake (CN "UNDEF"): scans, clients without a
// valid certificate. A user connecting is listed only for the second it takes, then leaves.
import { Alert, Button, Card } from "@heroui/react";
import { useState } from "react";
import { DateRange, Pager, usePaged } from "./History";
import type { Filters } from "./History";
import { F, PRESETS, addr, duration, usePref } from "./lib";
import type { Preset, Probe, Probes, Range, Status } from "./lib";
import { CountryCards, CountryTag, DataTable, Icon, Panel, ProfileTag, Search, Segmented, ServerTag } from "./ui";
import type { Col, Sort } from "./ui";

const PROBE_PRESETS: Preset[] = ["24h", "7d", "30d", "90d"];

export function ProbeLog({ server, multi, profiles, agents }: { server: string; multi: boolean; profiles: string[]; agents: Status["agents"] }) {
  // without the connect history a user seen mid-handshake before 0.1.9 may still be listed here
  const unchecked = agents.filter((a) => (!server || a.name === server) && a.logins !== "on");
  const [preset, setPreset] = usePref<Preset>("ppreset", "7d");
  const [range, setRange] = useState<Range>({ preset: PROBE_PRESETS.includes(preset) && preset in PRESETS ? preset : "7d" });
  const [q, setQ] = useState("");
  const [country, setCountry] = useState("");
  const [sort, setSort] = usePref<Sort>("sort.probes", { k: "start", dir: -1 });
  const filters: Filters = { range, q, server, country, proto: "", profile: "", plat: "", alerts: false };
  const r = usePaged<Probes>("api/probes", filters, sort);
  const s = r.data?.summary;

  const cols: Col<Probe>[] = [
    { id: "start", label: "Start", cell: (x) => <span className="num whitespace-nowrap">{F.abs.format(Date.parse(x.start))}</span> },
    { id: "real", label: "Real IP:port", cell: (x) => <span className="flex items-center"><CountryTag cc={x.country} /><span className="mono font-medium">{addr(x.real_ip, x.real_port)}</span></span> },
    ...(multi ? [{ id: "server", label: "Server", cell: (x: Probe) => <ServerTag name={x.server} /> }] : []),
    { id: "profile", label: "Reached", cell: (x) => <span title="The OpenVPN server (port) it connected to, not who it is"><ProfileTag name={x.profile} profiles={profiles} /></span> },
    { id: "proto", label: "Proto", cell: (x) => x.proto.toUpperCase() },
    { id: "dur", label: "Seen for", num: true, cell: (x) => duration((Date.parse(x.end) - Date.parse(x.start)) / 1000) },
    { id: "end", label: "Last seen", cell: (x) => <span className="num whitespace-nowrap text-muted">{F.abs.format(Date.parse(x.end))}</span> },
  ];

  return (
    <div className="grid min-w-0 gap-4">
      {unchecked.map((a) => (
        <Alert key={a.name} status="warning">
          <Alert.Indicator />
          <Alert.Content><Alert.Title>
            Not checked against the connect history{multi ? ` on ${a.name}` : ""}: {!a.logins ? "agent not answering"
              : a.logins === "off" ? "the agent has no data/log mounted (or is older than 0.1.10)" : a.logins}
          </Alert.Title></Alert.Content>
        </Alert>
      ))}
      <Card className="gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Segmented label="Time range" value={"preset" in range ? range.preset : ""} options={PROBE_PRESETS}
            onChange={(v) => { setRange({ preset: v as Preset }); setPreset(v as Preset); }} />
          <DateRange range={range} onChange={setRange} />
          <span className="ml-auto">
            <Button size="sm" variant="secondary" onPress={() => location.assign(r.csvURL())}><Icon name="download" className="size-4" />Export CSV</Button>
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Search value={q} onChange={setQ} placeholder="Real IP" className="w-64 max-sm:w-full" />
          <span className="text-xs text-muted">
            Connections to the OpenVPN ports that never logged in: scans from the internet, or a client with a wrong or revoked
            certificate. Checked against the connect history (the OpenVPN client-connect log): whatever logged in is a user, not listed here.
            Profile = the OpenVPN server (port) it reached, not who it is.
          </span>
        </div>
      </Card>
      {s && s.countries.length > 0 && (
        <Panel title="Where they come from" actions={<span className="num text-sm text-muted">{s.attempts.toLocaleString("en-GB")} attempts · {s.ips} IPs</span>}>
          <CountryCards items={s.countries.map(({ bytes: _, ...c }) => c)} value={country} onPick={setCountry} stale={r.loading} units={["IP", "attempt"]} />
        </Panel>
      )}
      <Panel title="Not authenticated" flush>
        <DataTable label="Connections not authenticated" cols={cols} rows={r.data?.probes ?? []} stale={r.loading || !!r.error}
          rowId={(x) => `${x.instance}|${x.start}|${x.real_ip}|${x.real_port}`} sort={sort} onSort={setSort} descFirst={["start", "end", "dur"]}
          rowClass={() => "row-alert"} empty={r.error || (!r.data ? "Loading…" : "No attempt in this range")} />
        <Pager page={r.page} total={s?.attempts ?? 0} onPage={r.setPage} />
      </Panel>
    </div>
  );
}
