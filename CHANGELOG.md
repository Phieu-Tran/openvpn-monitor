# Changelog

Versions apply to both images, `ovpn-agent` and `ovpn-dash`. Upgrade: load the new images,
change the version on the `image:` line of each host's `docker-compose.yml`,
`docker compose up -d` (see README, Upgrade).

## Unreleased

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
