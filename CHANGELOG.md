# Changelog

Versions apply to both images, `ovpn-agent` and `ovpn-dash`. Upgrade: load the new images,
change the version on the `image:` line of each host's `docker-compose.yml`,
`docker compose up -d` (see README, Upgrade).

## 0.1.10 - 2026-11-27

Upgrade the agents too, with the new data/log volumes (see README, "Connect history").

### Added

- Connect history: when OpenVPN has a client-connect hook writing
  `data/log/history-YYYY-MM.log` (one line per successful login), the agent reads it, read-only,
  from `/srv/ovpn-log/<profile>` (`OVPN_HISTORY_ROOT`; times in `OVPN_HISTORY_TZ`, default
  Asia/Ho_Chi_Minh) and serves the logins (`GET /api/v1/logins?since=`). The dash names with
  them what it saw as UNDEF: a session or a "Not authenticated" entry with the same server and
  real IP:port, within two minutes of a login, was that user; such an entry goes back to the
  user's sessions. `-check` reports the profiles and the latest login.

### Fixed

- "Not authenticated" listed users: 0.1.9 moved sessions stored by older versions as UNDEF
  without a VPN IP there, and some were users caught mid-handshake (e.g. a 21 s login over
  TCP). With the connect history they go back to their users; the view says where it is not
  checked. Its "Profile" column is now "Reached": the server (port) connected to, not who.

## 0.1.9 - 2026-11-27

Dash only: the agents stay on 0.1.8.

### Added

- "Not authenticated": connections to the OpenVPN ports that never finish their TLS
  handshake (no valid certificate: scans from the internet, a wrong or revoked certificate),
  logged apart and in red. Online lists the ones in progress (red panel, red count in the
  menu); a view lists every attempt (start, real IP and country, server, profile, how long),
  per country, searchable by IP, with CSV. `/api/probes`, `/api/probes.csv`.

### Fixed

- A "user" named UNDEF, with real IPs from many countries: OpenVPN shows a connection whose
  TLS handshake is not done yet with the common name UNDEF. Scans were stored as sessions,
  and a real user caught in that second kept the name UNDEF (with their access log rows).
  Now such a connection is never a user: it goes to "Not authenticated" and, once its
  handshake is done, to the user with their real name. Sessions stored as UNDEF get the real
  name on the next poll while still open; the scans among them (never a VPN IP) move to
  "Not authenticated"; older ones that cannot be named show a red "not authenticated" badge.
- Online counts, charts and per-profile numbers no longer count connections not yet authenticated.

## 0.1.8 - 2026-11-27

Upgrade the agents too (the access-log filter below lives there).

### Changed

- Access log: connections to the VPN server itself (its tun gateways, LAN and public
  addresses) and to the broadcast address of its subnets (Windows NetBIOS chatter) are left
  out by the agent, which reads both from its interfaces: any subnet size, nothing to
  configure. Multicast (mDNS, SSDP) and 255.255.255.255 are left out too. Rows 0.1.7 stored to
  these addresses are removed once the upgraded agent reports them.
- Access log: HTTPS (port 443) apart: hidden by default, "Only 443" or "All ports" on demand;
  a user's details list 443 separately. `/api/access` takes `port=` and `xport=` (all but).
- A user's details show "Access, last 30 days" right under the summary, always (with a note
  when nothing is recorded): the destinations (IP:port, connections, last time) and the latest
  rows, with a link to all of them.

### Fixed

- Access log rows of some users showed "unknown": OpenVPN can list a client before it has its
  VPN IP, and a session first seen that way kept an empty VPN IP in the history. The VPN IP
  is now updated on later polls, and rows of the last two hours without a user are matched
  again on every poll.

## 0.1.7 - 2026-11-27

### Added

- Access log: which user opened which IP:port, when. Optional per OpenVPN host:
  `OVPN_FLOWS=<VPN client subnets>` and `cap_add: [NET_ADMIN]` on the agent, which then
  listens to the kernel's "new connection" events (read only; source = the client's VPN IP,
  before NAT). The dash stores one row per user, destination and minute (first/last time,
  count), names the user from the session that had the VPN IP, and shows an "Access log" view
  (search by user, IP or `ip:port`, CSV) and "Recent access" in a user's details. A banner says
  which agents do not record it and why.

## 0.1.6 - 2026-11-27

### Added

- Countries (GeoIP of the real IP): Online shows "Connecting from", users and sessions per
  country right now; History shows "Countries", users, sessions and traffic per country in the
  filtered range. A card filters by that country. `/api/history` returns `countries`.

## 0.1.5 - 2026-11-27

### Added

- Several OpenVPN hosts in one dash: one `ovpn-agent` per host, and
  `OVPN_AGENT=name=url,name=url` on the dash (e.g.
  `vpn-a=http://<vpn-host-1>:9177,vpn-b=http://<vpn-host-2>:9177`). All agents share the
  dash's `data/agent.token`. A server bar above every view picks all servers or one; Online
  groups the instances by server (agent status, instances up, online, rate) and charts online
  sessions by server; tables get a Server column, the CSV files a `server`/`servers` column.
- A server whose agent does not answer is shown red with its last known sessions (greyed);
  the others keep working and recording. `/api/status` lists `agents`; the dash metrics carry
  a `server` label.

### Upgrade

- The history so far becomes the first server's in `OVPN_AGENT`, once, on the first start:
  put the existing OpenVPN host first and pick the names before upgrading. A name is stored
  with every session; renaming a server later splits its history.
- `/api/status`: `agent` is now `agents` (a list).

## 0.1.4 - 2026-11-27

### Added

- History: "Devices that connected" in the filtered range (OS + app, users, sessions, last
  seen); a card filters the history by that OS. A Device filter (OS, or "Unknown device") in
  History and Users; the Device column shows OS and app.

### Fixed

- Devices disappeared a few minutes after connecting: the agent's own polls fill OpenVPN's
  250-line log cache in ~4 minutes, and when a client's `peer info` block was cut at the top,
  the remaining half (no OS) replaced the full device. Cut blocks are now ignored, and the agent
  never trades a full device for a partial one.
- After an agent restart, open sessions get their device back from the dash's history at once
  instead of after the next renegotiation (up to an hour).
- Half devices stored by 0.1.3 are cleared when the dash starts.

## 0.1.3 - 2026-11-27

### Added

- Device of each session (OS with logo, OS version, app, OpenVPN version, MAC when sent), from
  the `peer info` lines in OpenVPN's log cache: the agent now also sends `log all` on the same
  connection. Needs verb 3 or higher on the server; self-reported by the client, not verified.
- Device column in Online and History, device logos in Users, a Device section in the session
  panel. The user panel now shows online sessions, a 30-day summary, devices and recent sessions.
- CSV: `device` column in sessions, `platforms` in users.
- Existing databases get the new `device` column on start; older sessions show "–".

### Changed

- Charts: the drawn line is a moving average (ranges of 60+ points; the tooltip keeps the
  exact values), and each stacked band fades from its own top edge, so the colours stay clear.

## 0.1.2 - 2026-11-27

### Security

- Built with Go 1.26.11 instead of 1.26.3. govulncheck finds 20 known vulnerabilities in the
  older standard library that the binaries reach (mostly `net/http`, also `net/textproto`,
  `crypto/tls`, `crypto/x509`), all fixed by 1.26.11: GO-2026-5026, -5037, -5039, -5856,
  -5972, -6089, -6090, -6218, -6603, -6605, -6607 to -6613, -6617, -6809, -6859.
  No code change; upgrade both hosts.
- CI fails on any vulnerability govulncheck finds (that is how these were caught).

## 0.1.1 - 2026-11-26

### Added

- ovpn-dash ships a country database (DB-IP "IP to Country Lite", 2026-11, CC BY 4.0): country
  flags work right after install. A file at `data/geoip.mmdb` still wins over it, so the monthly
  refresh needs no new image. `-check` and the page footer say which one is in use; the footer
  credits DB-IP as its licence asks.
- Profiles are told apart at a glance: each has one colour, the same in the charts, a tag in the
  Profile column, and a bar at the start of each row. The Online table has profile tabs with
  their session counts instead of the profile drop-down, and sorts by profile by default.

### Changed

- Deploy files: one `docker-compose.yml` per host that takes every host-specific value (IPs, the
  profiles' `data/run` paths, limits) from `.env`. Git has `docker-compose.example.yml` and
  `.env.example`; the real files stay out of git (`.gitignore`). `scripts/release.sh` copies both
  into `dist/`.
- `OVPN_AGENT` has no built-in default any more: ovpn-dash refuses to start without it.

## 0.1.0 - 2026-11-26

First release.

### ovpn-agent

- Polls every OpenVPN management socket every 5 s (`status 3`, then `quit`: never holds the
  socket), serves `/api/v1/sessions` and Prometheus `/metrics` behind a bearer token, `/healthz`,
  `-check`, `-dump`.

### ovpn-dash

- History in SQLite (`data/ovpn-dash.db`): one row per session, one chart sample per minute
  (merged to 10 minutes after 48 hours). Kept 90 days and at most 5 GB, oldest days first
  (`OVPN_RETENTION`, `OVPN_DATA_MAX`). Filtering, sorting, paging, totals and CSV export run in
  the database. History files of earlier builds (`*.jsonl`, `open.json`) are imported on the first
  start. `-backup` writes a consistent copy while the dash runs.
- Web UI (React + HeroUI, served from the binary, nothing loaded from the internet, strict CSP):
  Online view with KPIs, instance status, stacked online-by-profile and throughput charts (5 min to
  90 days), live session table, weekday x hour heatmap; History and Users views with date range,
  filters, paging and CSV; session and user details (14-day timeline, traffic per day, real IPs);
  quick search (Ctrl+K); light / dark / system theme.
- Alerts on the page: a session above `OVPN_ALERT_RATE` (default 5 MB/s), the same CN twice in
  one profile, instances down, history not being written, history trimmed by the size cap.
