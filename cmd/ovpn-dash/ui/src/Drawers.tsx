// Detail panels (session, user) and the Ctrl+K quick search.
import { Button, Drawer, ListBox, Modal } from "@heroui/react";
import { useState } from "react";
import type { ReactNode } from "react";
import { DayBars, Timeline } from "./charts";
import { F, addr, appName, bytes, countryName, duration, osName, rangeParams, rate, relative, sum, useDebounced, useJSON } from "./lib";
import type { Access, Device, LiveRow, SessionRecord, UserDetail, Users } from "./lib";
import { Badge, CopyButton, CountryTag, DeviceIcon, DeviceTag, ProfileTag, Search, ServerTag } from "./ui";

function Sheet({ title, onClose, children }: { title: ReactNode; onClose: () => void; children: ReactNode }) {
  return (
    <Drawer.Backdrop isOpen onOpenChange={(open) => !open && onClose()}>
      <Drawer.Content placement="right">
        <Drawer.Dialog className="w-full max-w-full sm:w-[34rem]">
          <Drawer.CloseTrigger />
          <Drawer.Header><Drawer.Heading>{title}</Drawer.Heading></Drawer.Header>
          <Drawer.Body className="space-y-5">{children}</Drawer.Body>
        </Drawer.Dialog>
      </Drawer.Content>
    </Drawer.Backdrop>
  );
}

const Section = ({ title, note, children }: { title: string; note?: string; children: ReactNode }) => (
  <section>
    <h3 className="mb-2 text-sm font-semibold text-foreground">{title}</h3>
    {note && <p className="-mt-1.5 mb-2 text-xs text-muted">{note}</p>}
    {children}
  </section>
);

const detailURL = (cn: string) => () => `api/user?${new URLSearchParams([...rangeParams({ preset: "30d" }), ["cn", cn]])}`;

// Field: one label/value row of a detail list.
function Field({ label, children, copy }: { label: string; children: ReactNode; copy?: string }) {
  return (
    <div className="flex min-h-9 items-center gap-3 border-b border-border">
      <dt className="w-32 shrink-0 text-muted">{label}</dt>
      <dd className="num min-w-0 grow break-all">{children}</dd>
      {copy && <CopyButton text={copy} />}
    </div>
  );
}

// DeviceFields: everything the client said about itself.
function DeviceFields({ device: d }: { device?: Device }) {
  if (!d?.plat) return <p className="text-sm text-muted">Unknown: the client's peer info was no longer in OpenVPN's log when the agent first saw this session.</p>;
  return (
    <dl className="text-sm">
      <Field label="System"><span className="flex items-center gap-2"><DeviceIcon plat={d.plat} />{osName(d)}</span></Field>
      {d.plat === "linux" && d.plat_ver && <Field label="Kernel">{d.plat_ver}</Field>}
      {appName(d) && <Field label="App">{appName(d)}{d.gui && <span className="block text-xs text-muted">{d.gui}</span>}</Field>}
      {d.ver && <Field label="OpenVPN core">{d.ver}</Field>}
      {d.hwaddr && <Field label="MAC" copy={d.hwaddr}><span className="mono">{d.hwaddr}</span></Field>}
    </dl>
  );
}

const NOTE = "Reported by the client app when it connects (OpenVPN peer info), not verified.";

export function SessionDrawer({ session: s, online, multi, onUser, onClose }: { session: LiveRow; online: boolean; multi: boolean; onUser: (cn: string) => void; onClose: () => void }) {
  return (
    <Sheet title={<span className="flex items-center gap-2">{s.cn}{online ? <Badge tone="success">online</Badge> : <Badge tone="default">disconnected</Badge>}</span>} onClose={onClose}>
      <dl className="text-sm">
        <Field label="Common name" copy={s.cn}>{s.cn}</Field>
        {s.username && <Field label="Username">{s.username}</Field>}
        {multi && <Field label="Server"><ServerTag name={s.server} /></Field>}
        <Field label="Instance">{s.instance}</Field>
        <Field label="VPN IP" copy={s.vpn_ip}><span className="mono">{s.vpn_ip}</span></Field>
        {s.vpn_ip6 && <Field label="VPN IPv6" copy={s.vpn_ip6}><span className="mono">{s.vpn_ip6}</span></Field>}
        <Field label="Real address" copy={s.real_ip}><CountryTag cc={s.country} /><span className="mono">{addr(s.real_ip, s.real_port)}</span></Field>
        {s.country && s.country !== "LAN" && <Field label="Country">{countryName(s.country)}</Field>}
        <Field label="Connected">{`${F.abs.format(Date.parse(s.connected_since))} (${duration((Date.now() - Date.parse(s.connected_since)) / 1000)})`}</Field>
        <Field label="Traffic ↑ / ↓">{`${bytes(s.bytes_rx)} / ${bytes(s.bytes_tx)}`}</Field>
        <Field label="Rate ↑ / ↓">{`${rate(s.rate_rx)} / ${rate(s.rate_tx)}`}</Field>
        {s.last_ref && <Field label="Last packet">{`${F.abs.format(Date.parse(s.last_ref))} (${relative(Date.parse(s.last_ref))})`}</Field>}
        <Field label="Cipher">{s.cipher}</Field>
        <Field label="Client / peer ID">{`${s.cid} / ${s.peer_id}`}</Field>
      </dl>
      <Section title="Device" note={NOTE}><DeviceFields device={s.device} /></Section>
      <Button variant="secondary" onPress={() => onUser(s.cn)}>Open {s.cn}: 30 days of activity</Button>
    </Sheet>
  );
}

const end = (x: SessionRecord) => (x.open ? Date.now() : Date.parse(x.end));

export function UserDrawer({ cn, profiles, multi, onSessions, onAccess, onClose }: {
  cn: string; profiles: string[]; multi: boolean; onSessions: (cn: string) => void; onAccess: (cn: string) => void; onClose: () => void;
}) {
  const r = useJSON<UserDetail>(`user-${cn}`, detailURL(cn), 30000);
  const acc = useJSON<Access>(`access-${cn}`, () => `api/access?${new URLSearchParams([...rangeParams({ preset: "30d" }), ["cn", cn], ["limit", "10"]])}`, 60000);
  const d = r.loading ? undefined : r.data;
  const open = d?.sessions.filter((s) => s.open) ?? [];
  const secs = d ? sum(d.sessions, (s) => (end(s) - Date.parse(s.start)) / 1000) : 0;
  const first = d?.sessions.at(-1), last = d?.sessions[0];
  return (
    <Sheet title={<span className="flex items-center gap-2">{cn}{open.length > 0 && <Badge tone="success">online</Badge>}</span>} onClose={onClose}>
      {!d ? <p className="text-sm text-muted">{r.error || "Loading…"}</p> : !d.sessions.length ? <p className="text-sm text-muted">No sessions in the last 30 days.</p> : <>
        {open.length > 0 && (
          <Section title={`Online now (${open.length})`}>
            <ul className="space-y-2">
              {open.map((s) => (
                <li key={`${s.instance}|${s.cid}`} className="rounded-lg border border-border p-3 text-sm">
                  <div className="flex flex-wrap items-center gap-2">
                    {multi && <ServerTag name={s.server} />}<ProfileTag name={s.profile} profiles={profiles} /><span className="text-xs text-muted">{s.proto.toUpperCase()}</span>
                    <span className="mono">{s.vpn_ip}</span>
                    <span className="ml-auto text-xs text-muted">since {F.dmhm.format(Date.parse(s.start))} · {duration((Date.now() - Date.parse(s.start)) / 1000)}</span>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1">
                    <DeviceTag device={s.device} />
                    <span><CountryTag cc={s.country} /><span className="mono text-xs">{addr(s.real_ip, s.real_port)}</span></span>
                  </div>
                </li>
              ))}
            </ul>
          </Section>
        )}
        <Section title="Last 30 days">
          <dl className="text-sm">
            {multi && <Field label="Servers"><span className="flex flex-wrap gap-1">{[...new Set(d.sessions.map((s) => s.server))].sort().map((v) => <ServerTag key={v} name={v} />)}</span></Field>}
            <Field label="Profiles"><span className="flex flex-wrap gap-1">{[...new Set(d.sessions.map((s) => s.profile))].sort().map((p) => <ProfileTag key={p} name={p} profiles={profiles} />)}</span></Field>
            <Field label="Sessions">{d.sessions.length >= 500 ? "500+" : d.sessions.length} · {duration(secs)} online</Field>
            <Field label="Traffic ↑ / ↓">{bytes(sum(d.days, (x) => x.bytes_rx))} / {bytes(sum(d.days, (x) => x.bytes_tx))}</Field>
            {first && <Field label="First seen">{F.abs.format(Date.parse(first.start))}</Field>}
            {last && <Field label="Last seen">{last.open ? "online now" : `${F.abs.format(end(last))} (${relative(end(last))})`}</Field>}
            <Field label="Real IPs">{d.ips.length >= 100 ? "100+" : d.ips.length} {[...new Set(d.ips.map((p) => p.country ?? ""))].filter(Boolean).map((c) => <CountryTag key={c} cc={c} />)}</Field>
          </dl>
        </Section>
        <Section title={`Devices (${d.devices.length})`} note={NOTE}>
          {!d.devices.length ? <p className="text-sm text-muted">None reported yet.</p> : (
            <ul className="text-sm">
              {d.devices.map((v, i) => (
                <li key={i} className="flex items-center gap-3 border-b border-border py-2">
                  <DeviceIcon plat={v.plat} className="size-6" />
                  <div className="min-w-0 grow">
                    <div className="font-medium">{osName(v)}{v.plat === "linux" && v.plat_ver && <span className="font-normal text-muted"> · {v.plat_ver}</span>}</div>
                    <div className="text-xs text-muted">{[appName(v), v.ver && `core ${v.ver}`, v.hwaddr && `MAC ${v.hwaddr}`].filter(Boolean).join(" · ")}</div>
                    <div className="num text-xs text-muted">first {F.dmhm.format(Date.parse(v.first))} · last {F.dmhm.format(Date.parse(v.last))} · {v.sessions} sessions</div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>
        <Section title="Recent sessions">
          <ul className="text-sm">
            {d.sessions.slice(0, 10).map((s) => (
              <li key={`${s.instance}|${s.cid}|${s.start}`} className="flex items-center gap-3 border-b border-border py-1.5">
                <DeviceIcon plat={s.device?.plat} />
                <div className="min-w-0 grow">
                  <div className="num">{F.dmhm.format(Date.parse(s.start))} → {s.open ? <Badge tone="success">online</Badge> : F.hm.format(end(s))}
                    <span className="text-muted"> · {duration((end(s) - Date.parse(s.start)) / 1000)}</span></div>
                  <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">{multi && <ServerTag name={s.server} />}<ProfileTag name={s.profile} profiles={profiles} />{s.proto.toUpperCase()} · <span><CountryTag cc={s.country} />{s.real_ip}</span></div>
                </div>
                <span className="num text-xs whitespace-nowrap text-muted">↑ {bytes(s.bytes_rx)} / ↓ {bytes(s.bytes_tx)}</span>
              </li>
            ))}
          </ul>
        </Section>
        {!!acc.data?.access.length && (
          <Section title="Recent access" note="New connections to IP:port, per minute (access log).">
            <ul className="text-sm">
              {acc.data.access.map((x) => (
                <li key={`${x.server}|${x.first}|${x.dst}|${x.port}|${x.proto}`} className="flex items-center gap-3 border-b border-border py-1.5">
                  <span className="num w-28 shrink-0 text-xs text-muted">{F.dmhm.format(Date.parse(x.first))}</span>
                  <span className="mono min-w-0 grow break-all">{addr(x.dst, x.port)} <span className="text-xs text-muted">{x.proto.toUpperCase()}</span></span>
                  {multi && <ServerTag name={x.server} />}
                  <span className="num text-xs whitespace-nowrap text-muted">×{x.n}</span>
                </li>
              ))}
            </ul>
            <Button size="sm" variant="ghost" className="mt-1" onPress={() => onAccess(cn)}>All access of {cn}</Button>
          </Section>
        )}
        <Section title="Online times, last 14 days"><Timeline sessions={d.sessions} /></Section>
        <Section title="Traffic per day (by session start)"><DayBars days={d.days} /></Section>
        <Section title="Real IPs">
          <ul className="text-sm">
            {d.ips.map((p) => (
              <li key={p.ip} className="flex items-center gap-2 border-b border-border py-1.5">
                <CountryTag cc={p.country} />
                <div className="min-w-0 grow">
                  <div className="mono break-all">{p.ip}</div>
                  <div className="num text-xs text-muted">first {F.dmhm.format(Date.parse(p.first))} · last {F.dmhm.format(Date.parse(p.last))} · {p.sessions} sessions · {bytes(p.bytes)}</div>
                </div>
                <CopyButton text={p.ip} />
              </li>
            ))}
          </ul>
        </Section>
        <Button variant="secondary" onPress={() => onSessions(cn)}>All sessions of {cn}</Button>
      </>}
    </Sheet>
  );
}

export function QuickSearch({ live, multi, onUser, onClose }: { live: LiveRow[]; multi: boolean; onUser: (cn: string) => void; onClose: () => void }) {
  const [q, setQ] = useState("");
  const needle = useDebounced(q.trim()).toLowerCase();
  const past = useJSON<Users>(needle ? `find-${needle}` : "",
    () => `api/users?${new URLSearchParams([...rangeParams({ preset: "90d" }), ["q", needle], ["limit", "8"]])}`, 60000);
  const found = new Map<string, string>(); // cn -> description
  if (needle) {
    for (const s of live) {
      if ([s.cn, s.vpn_ip, s.real_ip].some((v) => v.toLowerCase().includes(needle))) found.set(s.cn, `online · ${multi ? s.server + " · " : ""}${s.profile} · ${s.vpn_ip} · ${s.real_ip}`);
    }
    if (!past.loading) {
      for (const u of past.data?.users ?? []) if (!found.has(u.cn)) found.set(u.cn, `${multi ? u.servers.join(", ") + " · " : ""}${u.profiles.join(", ")} · seen ${relative(Date.parse(u.last_seen))}`);
    }
  }
  const items = [...found].slice(0, 10);
  return (
    <Modal.Backdrop isOpen onOpenChange={(open) => !open && onClose()}>
      <Modal.Container placement="top">
        <Modal.Dialog aria-label="Find a user" className="gap-2 p-3">
          <Search value={q} onChange={setQ} placeholder="Find a user by CN or IP" autoFocus className="w-full" />
          {items.length ? (
            <ListBox aria-label="Results" onAction={(k) => onUser(String(k))}>
              {items.map(([cn, text]) => (
                <ListBox.Item key={cn} id={cn} textValue={cn}>
                  <div><div className="font-medium">{cn}</div><div className="text-xs text-muted">{text}</div></div>
                </ListBox.Item>
              ))}
            </ListBox>
          ) : <p className="p-3 text-sm text-muted">{!needle ? "Type a name, VPN IP or real IP (last 90 days)." : past.loading ? "Searching…" : past.error || "No match"}</p>}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  );
}
