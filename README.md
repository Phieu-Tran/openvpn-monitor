# ovpn-dash

Shows who is connected to a set of OpenVPN servers: who is online right now (CN, profile,
UDP/TCP, VPN IP, real IP, traffic, rate), plus session history, per-user summaries and charts.

```text
[OpenVPN host <vpn-host>]                                [monitor host <monitor-host>]
 10 x openvpn -- data/run/mgmt-{udp,tcp}.sock              existing Nginx (TLS, LAN allow, basic auth)
        | (dir bind-mount, read-only)                                    | proxy_pass
 ovpn-agent (container, host network, uid 0, no caps)         ovpn-dash (127.0.0.1:9178) -> data/ (history)
   <vpn-host>:9177, bearer token   <---- HTTP, every 5s -----------------+
 iptables: tcp/9177 only from <monitor-host>
```

- **ovpn-agent** (image `vihat/ovpn-agent`): every 5s connects to each management socket,
  sends `status 3` and `log all` (each read until `END`), sends `quit`, closes. Never keeps the
  socket open, never sends anything else. `log all` only reads OpenVPN's log cache: the
  `peer info` lines (verb 3 or higher) tell each client's device (OS, app, OpenVPN version;
  self-reported, not verified). A session whose lines already left the cache has no device. Serves `GET /api/v1/sessions`, `GET /metrics` (bearer token), `GET /healthz`.
- **ovpn-dash** (image `vihat/ovpn-dash`): web UI (React + HeroUI, embedded in the binary) with
  three views, Online / History / Users: live table, charts, weekday x hour heatmap, session and
  user details, quick search (Ctrl+K), light/dark, filters and CSV export. Polls the agent
  server-side, so the token never reaches the browser, and records history in
  `data/ovpn-dash.db` (SQLite): one row per session and one chart sample per minute, kept
  90 days and at most 5 GB.
- **ovpn-fake**: lab only. 10 fake management sockets in the production layout.

Alert (red row, "Alerts now" tile, "Alerts only" filter): a session moving more than 5 MB/s
(`OVPN_ALERT_RATE`); History and Users use each session's peak rate.

The agent uses the Go standard library only. The dash adds the MaxMind DB reader
(`github.com/oschwald/maxminddb-golang`) for GeoIP and SQLite as pure Go
(`modernc.org/sqlite`, so the binary stays static). The page is built from npm packages at image
build time; it loads nothing from the internet. No OpenVPN config, image or compose change, no
OpenVPN restart.

## Repository layout

```text
cmd/ovpn-agent     poller, API, Prometheus metrics, -dump mode
cmd/ovpn-dash      agent poller, history store (SQLite), API; ui/ = page source, web/ = its build
cmd/ovpn-fake      lab simulator: -query (acts like ./vpn), -seed (made-up history)
internal/mgmt      management client + "status 3" parser; testdata/ = real OpenVPN 2.6 captures
deploy/agent       what goes on the OpenVPN host: docker-compose.yml + data/
deploy/dash        what goes on the monitor host: docker-compose.yml + data/
deploy/lab         fake + agent + dash, built from source
scripts/release.sh builds the images and the per-host folders into dist/
.github/workflows  tests; a v* tag publishes the images to GHCR
Dockerfile         one file, --build-arg APP=ovpn-agent|ovpn-dash|ovpn-fake --build-arg VERSION=x.y.z
```

## Develop

```sh
go vet ./...
go test ./...                      # works without Node: the page is not needed

cd cmd/ovpn-dash/ui                # Node 24+
npm ci
npm run build                      # type check + build into ../web, embedded by the next go build
npm run dev                        # live reload; /api goes to a dash on 127.0.0.1:8080
```

## Release

What changed in each version: [CHANGELOG.md](CHANGELOG.md).

On any machine with Docker (Git Bash on Windows works):

```sh
scripts/release.sh 0.1.8
```

Result in `dist/`:

```text
ovpn-agent-0.1.8.image.tar.gz    image vihat/ovpn-agent:0.1.8 (~3 MB)
ovpn-agent/                      docker-compose.yml + .env + data/ -> OpenVPN host
ovpn-dash-0.1.8.image.tar.gz     image vihat/ovpn-dash:0.1.8 (~10 MB)
ovpn-dash/                       docker-compose.yml + .env + data/ -> monitor host
SHA256SUMS
```

Builds are reproducible (same source + version = same binaries). Node is only needed inside Docker.

With GitHub instead: push the repo, then `git tag v0.1.8 && git push --tags`. The workflow
publishes `ghcr.io/<owner>/ovpn-agent:0.1.8` and `ghcr.io/<owner>/ovpn-dash:0.1.8`; on the hosts,
set that name on the `image:` line and use `docker compose pull` instead of `docker load`.

## Lab

Any Linux machine (or Docker Desktop) with Docker Compose v2.

```sh
cd deploy/lab
openssl rand -hex 32 > agent.token
chmod 644 agent.token          # lab only: dash runs as uid 65532
docker compose build
docker compose up -d
docker compose ps              # fake, agent, dash: running
```

Open <http://127.0.0.1:8080>. On a remote test machine use an SSH tunnel from your laptop:
`ssh -L 8080:127.0.0.1:8080 <user>@<test-machine>`.

Checks:

```sh
T=$(cat agent.token)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9177/api/v1/sessions                          # 401
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $T" http://127.0.0.1:9177/api/v1/sessions  # 200
curl -s http://127.0.0.1:9177/healthz                                                                    # ok
docker compose logs fake | grep 'conn close' | tail -3    # cmds=["status 3" "log all" "quit"], closed after ~1ms
docker compose exec fake /app -query /srv/ovpn/sale/mgmt-udp.sock   # a second client still gets in
curl -s -H "Authorization: Bearer $T" http://127.0.0.1:9177/metrics \
  | docker run --rm -i --entrypoint promtool prom/prometheus:v3.5.0 check metrics   # no output, exit 0
```

`cpaas-dev` TCP goes down for ~20s every minute (socket removed) and comes back.

History and charts fill up as the lab runs. To see them right away, seed 14 days of made-up data:

```sh
docker compose stop dash
docker run --rm -v ovpn-lab_dashdata:/data ovpn-fake:lab -seed /data
docker compose start dash
```

Stop: `docker compose down -v` (also deletes the lab history).

## Production

`<vpn-host>` = the OpenVPN host's LAN IP, `<monitor-host>` = the monitor host's, `<lan>` = the
LAN allowed to open the page (e.g. `10.0.0.0/24`).

Copy from `dist/`: the agent image + `ovpn-agent/` to the OpenVPN host, the dash image +
`ovpn-dash/` to the monitor host. Each host ends up with one folder:

```text
/opt/ovpn-agent/docker-compose.yml        /opt/ovpn-dash/docker-compose.yml
/opt/ovpn-agent/.env                      /opt/ovpn-dash/.env
/opt/ovpn-agent/data/agent.token          /opt/ovpn-dash/data/agent.token
                                          /opt/ovpn-dash/data/ovpn-dash.db*  (history)
```

Every host-specific value (IPs, the profiles' `data/run` paths, limits) is in `.env`;
`docker-compose.yml` reads it. Your real `deploy/<app>/docker-compose.yml` and `.env` stay in
this folder but are not in git: git has `docker-compose.example.yml` and `.env.example`.
Copy those once (`cp docker-compose.example.yml docker-compose.yml`, `cp .env.example .env`)
and fill in `.env`; the agent needs one volume line in the compose file and one `OVPN_RUN_*`
variable per OpenVPN profile. `scripts/release.sh` puts both files into `dist/`.

### 1. Monitor host (dash), first

```sh
docker load -i /tmp/ovpn-dash-0.1.8.image.tar.gz && rm /tmp/ovpn-dash-0.1.8.image.tar.gz
ss -ltn | grep ':9178 '                 # nothing: port is free
cd /opt/ovpn-dash
grep OVPN_AGENT .env                    # http://<vpn-host>:9177, or name=url,name=url (see "Several OpenVPN hosts")
openssl rand -hex 32 > data/agent.token # the shared token; copied to the agent host below
chown -R 65532:65532 data && chmod 750 data && chmod 600 data/agent.token
docker compose up -d
docker compose ps                       # Up ... (healthy)
curl -s http://127.0.0.1:9178/api/sessions   # {"error":"agent unreachable: ..."} until the agent is up
```

GeoIP (country flag + code next to each real IP) works out of the box: the dash image ships
DB-IP "IP to Country Lite" (CC BY 4.0) of its build month. Lookups are offline, in memory: no IP
leaves the host and nothing calls the internet. For fresher data, put a newer MaxMind-format
country database at `data/geoip.mmdb` (DB-IP Lite, free, no account; or MaxMind
GeoLite2-Country, free account); it wins over the built-in one and is picked up within
5 minutes, no restart. Monthly, if you like:

```sh
curl -sSfL https://download.db-ip.com/free/dbip-country-lite-$(date +%Y-%m).mmdb.gz | gunzip > data/geoip.mmdb.new   && mv data/geoip.mmdb.new data/geoip.mmdb && chown 65532:65532 data/geoip.mmdb
```

Settings in `.env` (`docker compose up -d` after a change): `OVPN_ALERT_RATE` (default
5MB per second), `OVPN_RETENTION` (default `90d`) and `OVPN_DATA_MAX` (default `5GB`, `0` = no
cap).

Point the existing Nginx at `http://127.0.0.1:9178` with TLS, `allow <lan>; deny all;`
and basic auth. The page loads and shows "agent unreachable".

### 2. OpenVPN host (agent + iptables)

```sh
docker load -i /tmp/ovpn-agent-0.1.8.image.tar.gz && rm /tmp/ovpn-agent-0.1.8.image.tar.gz
ls -ldn <profile-dir>/data/run          # drwx------ 0 0: hence user "0:0" (no capabilities)
cd /opt/ovpn-agent
cat .env                                # OVPN_LISTEN=<vpn-host>:9177, one OVPN_RUN_* per profile
mkdir -p data && chown root:root data && chmod 700 data   # root-owned: uid 0 has no caps, ownership is its only way in
# replace PASTE_TOKEN_HERE with the token from <monitor-host>:/opt/ovpn-dash/data/agent.token
(umask 077; printf '%s\n' 'PASTE_TOKEN_HERE' > data/agent.token)
docker compose up -d
docker compose exec agent /app -check   # all checks passed (every socket answers, all instances up)

# Firewall: 9177 only from the monitor host (and loopback, for the container healthcheck).
# ACCEPT ends up above DROP.
iptables -I INPUT ! -i lo -p tcp --dport 9177 -j DROP
iptables -I INPUT -p tcp --dport 9177 -s <monitor-host> -j ACCEPT
iptables -L INPUT -n -v --line-numbers | grep 9177
```

Make the two rules persistent with the mechanism the host already uses
(`netfilter-persistent save` on Debian/Ubuntu, `service iptables save` on RHEL/CentOS).

### 3. Verify

Each container checks itself with the exact mounts, permissions and network it runs with,
and prints one OK/FAIL line per check (exit code 0 = all good):

```sh
# OpenVPN host, /opt/ovpn-agent
docker compose ps                       # (healthy)
docker compose exec agent /app -check   # token, sockets, each instance answers, API 200/401, all up

# monitor host, /opt/ovpn-dash
docker compose ps                       # (healthy)
docker compose exec dash /app -check    # token, data/ writable, agent reachable + token accepted,
                                        # all instances up, history being written

# firewall, from any other LAN host: times out
curl -s -m 3 http://<vpn-host>:9177/healthz || echo blocked

# on the OpenVPN host: compare with OpenVPN's own status files and ./vpn
cat <profile-dir>/data/state/status-udp.log
```

Typical FAIL lines and what they mean:

| FAIL | Cause |
| --- | --- |
| `management sockets ...: none found` | the `OVPN_RUN_*` paths in the agent's `.env` |
| `.../mgmt-udp.sock: permission denied` | agent not running as `user: "0:0"` |
| `agent .../healthz: ... i/o timeout` | firewall on the OpenVPN host, or `OVPN_LISTEN` / `OVPN_AGENT` in the `.env` files |
| `401: data/agent.token differs` | the two `data/agent.token` files are not the same |
| `data directory /data writable: permission denied` | `chown -R 65532:65532 data` on the dash host |

Day-to-day health, without a token on the dash host:

- the page footer shows versions and how far back the history goes; a red banner appears on
  every view if history cannot be written;
- `curl -s http://127.0.0.1:9178/api/status` (dash host): agent ok/last success, history counts,
  write errors;
- `/metrics` on both: `ovpn_dash_agent_up`, `ovpn_dash_agent_poll_errors_total`,
  `ovpn_dash_history_write_errors_total`, `ovpn_dash_data_bytes`, `ovpn_agent_polls_total`,
  `ovpn_instance_poll_errors_total`, `ovpn_*_build_info`, Go memory;
- after 24h: `docker stats --no-stream` on both hosts (CPU near 0, tens of MB of memory).

### Upgrade

Build a new version (`scripts/release.sh 0.2.0`), `docker load` it on the host, change the
version on the `image:` line, `docker compose up -d`. The `data/` folders stay as they are. Going back = previous version on the `image:` line +
`docker compose up -d`.

History written by earlier builds (`history.jsonl`, `samples.jsonl`, `open.json`) is imported into
`data/ovpn-dash.db` on the first start and the files are renamed to `*.imported`; delete those
once you are satisfied.

### Several OpenVPN hosts

One dash shows several OpenVPN hosts: put an `ovpn-agent` on each (section 2, same
`data/agent.token` as the dash; the iptables rule allows the monitor host on each) and list
them all, by name, on the dash:

```sh
# /opt/ovpn-dash/.env
OVPN_AGENT=vpn-2=http://<vpn-host-1>:9177,vpn-4=http://<vpn-host-2>:9177
docker compose up -d && docker compose exec dash /app -check   # one OK pair per agent
```

- The name (letters, digits, `-`; no spaces or `/`) is stored with every session and chart
  sample: pick it once. Renaming later splits that server's history in two.
- Upgrading from 0.1.4 or older: the existing history becomes the **first** server's, once,
  on the first start. Keep the host you already monitor first.
- The bar above every view picks all servers or one. A server whose agent does not answer
  shows red with its last known sessions greyed; the others keep working and recording, and
  its sessions stay open until it answers again (no fake disconnects).

### Access log (which user opened which IP:port, when)

Optional, per OpenVPN host. The agent listens to the kernel's connection tracking ("new
connection" events over netlink) and keeps the TCP/UDP connections whose source is a VPN
client: before NAT, so the source is the client's VPN IP even where the host masquerades
towards the LAN. The dash names the user from the session that had that VPN IP, and stores
one row per user, destination IP:port and minute (first and last time, count), kept like the
rest of the history (`OVPN_RETENTION`, `OVPN_DATA_MAX`). Read only: it changes no firewall,
route or OpenVPN setting, and a slow or stopped agent never holds up traffic (the kernel drops
events for the agent only). IP:port only: no URLs, no content.

Left out, nothing to configure: connections to the VPN server itself and to the broadcast
address of each of its subnets (both read from the host's interfaces, so any subnet size),
multicast and 255.255.255.255. HTTPS (443) is recorded but hidden by default in the Access log
view ("Only 443" / "All ports" switch) and listed apart in a user's details.

Needs OpenVPN with `network_mode: host` (the VPN traffic in the host's network namespace),
and on the agent:

```sh
# .env: the VPN client subnets of this host (the "server" lines of its profiles)
OVPN_FLOWS=10.8.0.0/16
# docker-compose.yml: cap_add: [NET_ADMIN], next to cap_drop: [ALL]
docker compose up -d && docker compose exec agent /app -check   # access log: ... OK
```

The dash fetches each minute once it is over, so rows appear about a minute late; the agent
keeps an hour of minutes, so a dash outage of more than an hour loses that part. Connections
whose VPN IP matched no session show as "unknown". Inform your users that access is logged.

### History: size, retention, backup

- Samples older than 48 hours are merged to one per 10 minutes (peaks survive).
- Every hour the dash drops data older than `OVPN_RETENTION`; if the database is still above
  `OVPN_DATA_MAX` it drops the oldest days and the page shows a banner. Allow free disk space of
  about twice the cap (write-ahead log, large sorts, a backup).
- Backup, safe while the dash runs: `docker compose exec dash /app -backup` writes
  `data/backup-<time>.db`. Move it elsewhere: backups do not count against the cap and are never
  deleted by the dash. Restore: stop the dash, put the file back as `data/ovpn-dash.db`, delete
  `ovpn-dash.db-wal` and `-shm`, start.
- Measured on a laptop with 3,000,000 sessions (about 600 MB, 100 times the expected size):
  one poll is written in 40 ms, the 24-hour views answer in under 0.1 s, 7 days in under 1 s,
  and the 90-day totals and Users table take 3-10 s. Memory use does not grow with the data.

### Rollback (remove)

```sh
# OpenVPN host
cd /opt/ovpn-agent && docker compose down
iptables -D INPUT -p tcp --dport 9177 -s <monitor-host> -j ACCEPT
iptables -D INPUT ! -i lo -p tcp --dport 9177 -j DROP
# then save the rules again. Nothing to revert on OpenVPN.
```

## Test VM with a real OpenVPN 2.6

Nothing here touches the production OpenVPN host except `docker save` (read-only) and copying
files. `<VM>` = the test VM's IP, `<openvpn-image>` = your OpenVPN image, `<profile-dir>` = one
production profile's compose directory.

```sh
# 1. On the OpenVPN host: export the OpenVPN image and one profile's compose/conf (without data/ = no PKI)
docker save <openvpn-image> | gzip > /tmp/openvpn-image.tgz
scp /tmp/openvpn-image.tgz root@<VM>:/tmp/ && rm /tmp/openvpn-image.tgz
rsync -a --exclude data <profile-dir>/ root@<VM>:/srv/openvpn-test/

# 2. On <VM>: a test profile with a fresh PKI and 2 test clients
docker load -i /tmp/openvpn-image.tgz
cd /srv/openvpn-test
# rename the profile in docker-compose.yml so the containers do not clash, then
# init the PKI and create test1, test2 as for a new profile
docker compose up -d
ls -ldn data/run && ls -ln data/run/           # drwx------ 0 0; mgmt-{udp,tcp}.sock srwxrwxrwx
# connect test1 to UDP and test2 to TCP from a laptop/phone

# 3. On <VM>: the release folders from dist/ (both on the VM)
docker load -i /tmp/ovpn-agent-0.1.8.image.tar.gz
docker load -i /tmp/ovpn-dash-0.1.8.image.tar.gz
cd /opt/ovpn-agent
vi docker-compose.yml   # keep one volume line: ${OVPN_RUN_TEST}:/srv/ovpn/test:ro
printf 'OVPN_LISTEN=<VM>:9177\nOVPN_RUN_TEST=/srv/openvpn-test/data/run\n' > .env
(umask 077; openssl rand -hex 32 > data/agent.token)
docker compose up -d && docker compose exec agent /app -check   # 2 sockets, 2/2 instances up
cd /opt/ovpn-dash
printf 'OVPN_AGENT=http://<VM>:9177\n' > .env
cp /opt/ovpn-agent/data/agent.token data/ && chown -R 65532:65532 data && chmod 600 data/agent.token
docker compose up -d && docker compose exec dash /app -check    # all checks passed
```

Checks on `<VM>`:

- Laptop: `ssh -L 9178:127.0.0.1:9178 root@<VM>`, open <http://127.0.0.1:9178>: test1 (UDP), test2 (TCP).
- Run the usual `./vpn` commands while the agent runs: they still work.
- `cat /srv/openvpn-test/data/state/status-udp.log`: same CN, real address, connected since.
- Restart one OpenVPN container: the agent shows it down at most one cycle, then up again.
- Raw capture for the parser tests (save under `internal/mgmt/testdata/`, CNs and IPs masked):
  `docker run --rm --network none --user 0:0 -v /srv/openvpn-test/data/run:/s:ro vihat/ovpn-agent:0.1.8 -dump /s/mgmt-udp.sock`

Cleanup: `docker compose down` in `/opt/ovpn-agent`, `/opt/ovpn-dash` and `/srv/openvpn-test`.

## API

`GET /api/v1/sessions` on the agent (bearer):

```json
{"generated_at": "2026-11-26T09:26:37Z",
 "instances": [{"name": "openvpn-udp-all-dev-v2", "profile": "all-dev", "proto": "udp",
   "up": true, "last_error": "", "last_ok": "...", "version": "2.6.20",
   "sessions": [{"cn": "...", "real_ip": "...", "real_port": 51234, "vpn_ip": "10.8.0.2", "vpn_ip6": "",
     "bytes_rx": 0, "bytes_tx": 0, "rate_rx": 0, "rate_tx": 0, "connected_since": "...",
     "cid": 5, "peer_id": 0, "cipher": "AES-256-GCM", "last_ref": "...",
     "device": {"plat": "win", "plat_ver": "10.0.22631", "gui": "OpenVPN_GUI_11", "ver": "2.6.12"}}]}]}
```

Bytes and rates are from the server's side: `rx` = from the client (upload), `tx` = to the client.
Times are UTC (from OpenVPN's `time_t` columns); the page shows them in Asia/Ho_Chi_Minh.

`GET /metrics` on the agent (bearer): `ovpn_instance_up`, `ovpn_instance_clients`,
`ovpn_instance_poll_errors_total`, `ovpn_client_bytes_received_total`, `ovpn_client_bytes_sent_total`
(labels instance, profile, proto, cn; same CN summed), `ovpn_agent_build_info`, `ovpn_agent_polls_total`,
`ovpn_agent_last_poll_timestamp_seconds`, `ovpn_agent_poll_duration_seconds`, Go memory.
No real IPs or timestamps in labels.

Configuration is by environment variable (set in the compose files): agent `OVPN_LISTEN`,
`OVPN_ROOT`, `OVPN_TOKEN_FILE`; dash `OVPN_LISTEN`, `OVPN_AGENT`, `OVPN_TOKEN_FILE`, `OVPN_DATA`.
Every binary also has `-version`, `-check` and `-healthcheck`.

ovpn-dash (behind Nginx, no token):

- `GET /healthz`: `ok` (Docker healthcheck).
- `GET /api/status`: versions, per agent (`agents`) ok / last success / poll errors, history counts, write errors.
- `GET /metrics`: Prometheus text, dash health (see Verify).
- `GET /api/sessions`: the agent's last answer (refreshed every 5s).
- `GET /api/history` (also users/sessions/traffic per country and per device), `GET /api/users`: one page of the sessions (or per-CN sums) overlapping
  `from`..`to` (RFC 3339, default last 7 days), plus totals. Filters: `q` (CN, real or VPN IP),
  `cn`, `profile`, `proto`, `country`, `alerts=1`. Paging: `limit` (max 500), `offset`, `sort`,
  `dir=asc|desc`. `/api/history.csv` and `/api/users.csv` stream the whole filtered result.
- `GET /api/user?cn=`: sessions (newest 500), real IPs and traffic per day of one CN.
- `GET /api/series?from=&to=` (default last 24h): chart samples, downsampled to about 360 points
  (peak online per bucket, average rate).
- `GET /api/heatmap?from=&to=` (default 30 days): average and peak online per weekday and hour.
- `GET /api/facets`: profiles and countries present in the history.
- `GET /api/access` (and `/api/access.csv`): the access log, per user, destination and
  minute; filters `q` (user, VPN IP, `ip`, `:port`, `ip:port`), `cn`, `server`, `proto`,
  `port` (only this port), `xport` (all but this port), `dests=1` (also per destination).

The agent also serves `GET /api/v1/flows?since=<unix seconds>` (bearer): the access-log minutes
after `since` that are over, with `enabled`, `error`, `events`, `dropped` and `skip` (the
destinations it leaves out: this host's addresses and its subnets' broadcasts).

## Notes

- A session's end time is the last poll that saw it (accurate to 5s). Sessions on an instance
  that is down, or while the agent is unreachable, stay open until the instance answers again:
  a monitoring outage never produces fake disconnects. Sessions not seen for 24h are closed.
- History only exists from the day ovpn-dash is deployed; OpenVPN keeps no past sessions to import.
- A "same CN" warning is per profile: each profile has its own PKI, so the same CN in two
  profiles is normal; the same CN twice inside one profile looks like a shared certificate.
- While `./vpn` holds a management socket, that instance's poll times out (3s) and it shows
  as down for one cycle. The agent never blocks `./vpn`.
- An OpenVPN restart that recreates the socket is picked up on the next poll (the agent
  mounts the directory, not the socket file). If the entrypoint ever deletes the `data/run`
  directory itself, the mount goes stale: `docker compose restart` the agent.
