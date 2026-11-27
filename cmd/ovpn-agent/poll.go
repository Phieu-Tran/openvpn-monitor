package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ovpn-dash/internal/mgmt"
)

type session struct {
	mgmt.Client
	RateRx float64 `json:"rate_rx"` // bytes/s received from the client
	RateTx float64 `json:"rate_tx"` // bytes/s sent to the client
}

type instance struct {
	Name      string    `json:"name"`
	Profile   string    `json:"profile"`
	Proto     string    `json:"proto"`
	Up        bool      `json:"up"`
	LastError string    `json:"last_error,omitempty"`
	LastOK    time.Time `json:"last_ok,omitzero"`
	Version   string    `json:"version,omitempty"`
	Sessions  []session `json:"sessions"`

	path   string
	prev   map[sessKey]sample
	errors uint64 // failed polls since start
}

type sessKey struct {
	cid   uint64
	since int64
}

type sample struct {
	rx, tx uint64
	at     time.Time
	dev    mgmt.Device // seen once while the "peer info" lines were in the log cache
}

type snapshot struct{ json, metrics []byte }

type poller struct {
	root    string
	timeout time.Duration
	insts   map[string]*instance // keyed by socket path; only the poll loop touches it
	polls   uint64
	snap    atomic.Pointer[snapshot]
}

func newPoller(root string, timeout time.Duration) *poller {
	return &poller{root: root, timeout: timeout, insts: map[string]*instance{}}
}

// poll discovers sockets, queries all instances concurrently and publishes a snapshot.
// Instances seen before stay listed (as down) when their socket disappears.
func (p *poller) poll() {
	start := time.Now()
	paths, _ := filepath.Glob(filepath.Join(p.root, "*", "mgmt-*.sock"))
	for _, path := range paths {
		if p.insts[path] == nil {
			profile := filepath.Base(filepath.Dir(path))
			proto := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "mgmt-"), ".sock")
			p.insts[path] = &instance{Name: "openvpn-" + proto + "-" + profile + "-v2", Profile: profile, Proto: proto, path: path}
			log.Printf("discovered %s (%s)", p.insts[path].Name, path)
		}
	}
	var wg sync.WaitGroup
	for _, in := range p.insts {
		wg.Add(1)
		go func() { defer wg.Done(); p.pollOne(in) }()
	}
	wg.Wait()

	list := make([]*instance, 0, len(p.insts))
	for _, in := range p.insts {
		list = append(list, in)
	}
	slices.SortFunc(list, func(a, b *instance) int {
		return cmp.Or(cmp.Compare(a.Profile, b.Profile), -cmp.Compare(a.Proto, b.Proto)) // udp before tcp
	})
	p.polls++
	js, _ := json.Marshal(struct {
		GeneratedAt  time.Time   `json:"generated_at"`
		AgentVersion string      `json:"agent_version"`
		Instances    []*instance `json:"instances"`
	}{time.Now().UTC(), version, list})
	p.snap.Store(&snapshot{json: js, metrics: p.metrics(list, start)})
}

func (p *poller) pollOne(in *instance) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	st, err := mgmt.Query(ctx, in.path)
	now := time.Now()
	if err != nil {
		if in.LastError != err.Error() {
			log.Printf("%s down: %v", in.Name, err)
		}
		in.Up, in.LastError, in.Sessions, in.prev = false, err.Error(), []session{}, nil
		in.errors++
		return
	}
	if !in.Up {
		log.Printf("%s up (%d sessions)", in.Name, len(st.Clients))
	}
	in.Up, in.LastError, in.LastOK, in.Version = true, "", now.UTC(), st.Version
	prev := in.prev
	in.prev = make(map[sessKey]sample, len(st.Clients))
	in.Sessions = make([]session, 0, len(st.Clients))
	for _, c := range st.Clients {
		s := session{Client: c}
		k := sessKey{c.CID, c.ConnectedSince.Unix()}
		if old, ok := prev[k]; ok {
			if dt := now.Sub(old.at).Seconds(); dt > 0 {
				s.RateRx, s.RateTx = rate(c.BytesRx, old.rx, dt), rate(c.BytesTx, old.tx, dt)
			}
			if s.Device.Plat == "" && old.dev.Plat != "" { // never trade a full device for a partial one
				s.Device = old.dev
			}
		}
		in.prev[k] = sample{c.BytesRx, c.BytesTx, now, s.Device}
		in.Sessions = append(in.Sessions, s)
	}
}

func rate(cur, old uint64, dt float64) float64 {
	if cur < old {
		return 0
	}
	return float64(cur-old) / dt
}

var labelEsc = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// metrics renders Prometheus text format. No real IPs or timestamps in labels.
func (p *poller) metrics(list []*instance, start time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# HELP ovpn_agent_build_info Agent version.\n# TYPE ovpn_agent_build_info gauge\novpn_agent_build_info{version=\"%s\"} 1\n", labelEsc.Replace(version))
	fmt.Fprintf(&b, "# HELP ovpn_agent_polls_total Poll rounds since start.\n# TYPE ovpn_agent_polls_total counter\novpn_agent_polls_total %d\n", p.polls)
	fmt.Fprintf(&b, "# HELP ovpn_agent_last_poll_timestamp_seconds End of the last poll round (unix time).\n# TYPE ovpn_agent_last_poll_timestamp_seconds gauge\novpn_agent_last_poll_timestamp_seconds %d\n", time.Now().Unix())
	fmt.Fprintf(&b, "# HELP ovpn_agent_poll_duration_seconds Duration of the last poll round.\n# TYPE ovpn_agent_poll_duration_seconds gauge\novpn_agent_poll_duration_seconds %g\n", time.Since(start).Seconds())
	lbl := func(in *instance) string {
		return fmt.Sprintf(`instance="%s",profile="%s",proto="%s"`, labelEsc.Replace(in.Name), labelEsc.Replace(in.Profile), labelEsc.Replace(in.Proto))
	}
	b.WriteString("# HELP ovpn_instance_up Whether the last management poll of the instance succeeded.\n# TYPE ovpn_instance_up gauge\n")
	for _, in := range list {
		up := 0
		if in.Up {
			up = 1
		}
		fmt.Fprintf(&b, "ovpn_instance_up{%s} %d\n", lbl(in), up)
	}
	b.WriteString("# HELP ovpn_instance_poll_errors_total Failed management polls since the agent started.\n# TYPE ovpn_instance_poll_errors_total counter\n")
	for _, in := range list {
		fmt.Fprintf(&b, "ovpn_instance_poll_errors_total{%s} %d\n", lbl(in), in.errors)
	}
	b.WriteString("# HELP ovpn_instance_clients Connected client sessions.\n# TYPE ovpn_instance_clients gauge\n")
	for _, in := range list {
		fmt.Fprintf(&b, "ovpn_instance_clients{%s} %d\n", lbl(in), len(in.Sessions))
	}
	for _, m := range []struct {
		name, help string
		val        func(session) uint64
	}{
		{"ovpn_client_bytes_received_total", "Bytes received from the client, summed over sessions with the same CN.", func(s session) uint64 { return s.BytesRx }},
		{"ovpn_client_bytes_sent_total", "Bytes sent to the client, summed over sessions with the same CN.", func(s session) uint64 { return s.BytesTx }},
	} {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", m.name, m.help, m.name)
		for _, in := range list {
			sums := map[string]uint64{}
			for _, s := range in.Sessions {
				sums[s.CN] += m.val(s)
			}
			for _, cn := range slices.Sorted(maps.Keys(sums)) {
				fmt.Fprintf(&b, "%s{%s,cn=\"%s\"} %d\n", m.name, lbl(in), labelEsc.Replace(cn), sums[cn])
			}
		}
	}
	return b.Bytes()
}
