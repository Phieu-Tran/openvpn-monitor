// Detail panels (session, user) and the Ctrl+K quick search.
import { Button, Drawer, ListBox, Modal } from "@heroui/react";
import { useState } from "react";
import type { ReactNode } from "react";
import { DayBars, Timeline } from "./charts";
import { F, addr, bytes, countryName, duration, rangeParams, rate, relative, sum, useDebounced, useJSON } from "./lib";
import type { LiveRow, UserDetail, Users } from "./lib";
import { Badge, CopyButton, CountryTag, Search } from "./ui";

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

const Section = ({ title, children }: { title: string; children: ReactNode }) => (
  <section><h3 className="mb-2 text-sm font-semibold text-foreground">{title}</h3>{children}</section>
);

const detailURL = (cn: string) => () => `api/user?${new URLSearchParams([...rangeParams({ preset: "30d" }), ["cn", cn]])}`;

export function SessionDrawer({ session: s, online, onUser, onClose }: { session: LiveRow; online: boolean; onUser: (cn: string) => void; onClose: () => void }) {
  const field = (label: string, value: ReactNode, copy?: string) => (
    <div className="flex min-h-9 items-center gap-3 border-b border-border">
      <dt className="w-32 shrink-0 text-muted">{label}</dt>
      <dd className="num min-w-0 grow break-all">{value}</dd>
      {copy && <CopyButton text={copy} />}
    </div>
  );
  return (
    <Sheet title={<span className="flex items-center gap-2">{s.cn}{online ? <Badge tone="success">online</Badge> : <Badge tone="default">disconnected</Badge>}</span>} onClose={onClose}>
      <dl className="text-sm">
        {field("Common name", s.cn, s.cn)}
        {s.username && field("Username", s.username)}
        {field("Instance", s.instance)}
        {field("VPN IP", <span className="mono">{s.vpn_ip}</span>, s.vpn_ip)}
        {s.vpn_ip6 && field("VPN IPv6", <span className="mono">{s.vpn_ip6}</span>, s.vpn_ip6)}
        {field("Real address", <><CountryTag cc={s.country} /><span className="mono">{addr(s.real_ip, s.real_port)}</span></>, s.real_ip)}
        {s.country && s.country !== "LAN" && field("Country", countryName(s.country))}
        {field("Connected", `${F.abs.format(Date.parse(s.connected_since))} (${duration((Date.now() - Date.parse(s.connected_since)) / 1000)})`)}
        {field("Traffic ↑ / ↓", `${bytes(s.bytes_rx)} / ${bytes(s.bytes_tx)}`)}
        {field("Rate ↑ / ↓", `${rate(s.rate_rx)} / ${rate(s.rate_tx)}`)}
        {s.last_ref && field("Last packet", `${F.abs.format(Date.parse(s.last_ref))} (${relative(Date.parse(s.last_ref))})`)}
        {field("Cipher", s.cipher)}
        {field("Client / peer ID", `${s.cid} / ${s.peer_id}`)}
      </dl>
      <Button variant="secondary" onPress={() => onUser(s.cn)}>Open {s.cn}: 30 days of activity</Button>
    </Sheet>
  );
}

export function UserDrawer({ cn, onSessions, onClose }: { cn: string; onSessions: (cn: string) => void; onClose: () => void }) {
  const r = useJSON<UserDetail>(`user-${cn}`, detailURL(cn), 30000);
  const d = r.loading ? undefined : r.data;
  return (
    <Sheet title={<span className="flex items-center gap-2">{cn}{d?.sessions.some((s) => s.open) && <Badge tone="success">online</Badge>}</span>} onClose={onClose}>
      {!d ? <p className="text-sm text-muted">{r.error || "Loading…"}</p> : !d.sessions.length ? <p className="text-sm text-muted">No sessions in the last 30 days.</p> : <>
        <p className="num text-sm text-muted">
          Last 30 days: {d.sessions.length >= 500 ? "500+" : d.sessions.length} sessions · ↑ {bytes(sum(d.days, (x) => x.bytes_rx))} / ↓ {bytes(sum(d.days, (x) => x.bytes_tx))} · {d.ips.length >= 100 ? "100+" : d.ips.length} real IPs
        </p>
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

export function QuickSearch({ live, onUser, onClose }: { live: LiveRow[]; onUser: (cn: string) => void; onClose: () => void }) {
  const [q, setQ] = useState("");
  const needle = useDebounced(q.trim()).toLowerCase();
  const past = useJSON<Users>(needle ? `find-${needle}` : "",
    () => `api/users?${new URLSearchParams([...rangeParams({ preset: "90d" }), ["q", needle], ["limit", "8"]])}`, 60000);
  const found = new Map<string, string>(); // cn -> description
  if (needle) {
    for (const s of live) {
      if ([s.cn, s.vpn_ip, s.real_ip].some((v) => v.toLowerCase().includes(needle))) found.set(s.cn, `online · ${s.profile} · ${s.vpn_ip} · ${s.real_ip}`);
    }
    if (!past.loading) {
      for (const u of past.data?.users ?? []) if (!found.has(u.cn)) found.set(u.cn, `${u.profiles.join(", ")} · seen ${relative(Date.parse(u.last_seen))}`);
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
