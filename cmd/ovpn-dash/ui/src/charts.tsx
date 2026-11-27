// Small SVG/CSS charts: HeroUI has none, and these four forms need no library.
import { useId, useLayoutEffect, useRef, useState } from "react";
import { D, F, H, M, VN, bytes, duration } from "./lib";
import type { SessionRecord, UserDetail, Heat } from "./lib";

// seriesColor: slot by the entity's position in the full list, never by rank; past 8 = grey.
export const seriesColor = (i: number) => (i >= 0 && i < 8 ? `var(--s${i + 1})` : "var(--sx)");

export function Legend({ items }: { items: { name: string; color: string; value?: string }[] }) {
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
      {items.map((s) => (
        <li key={s.name} className="flex items-center gap-1.5">
          <i className="inline-block size-2.5 rounded-[3px]" style={{ background: s.color }} />
          {s.name}
          {s.value !== undefined && <b className="num font-semibold text-foreground">{s.value}</b>}
        </li>
      ))}
    </ul>
  );
}

function useWidth() {
  const ref = useRef<HTMLDivElement>(null);
  const [width, set] = useState(600);
  useLayoutEffect(() => {
    const el = ref.current!;
    const measure = () => set(Math.max(280, Math.floor(el.clientWidth)));
    measure();
    const o = new ResizeObserver(measure);
    o.observe(el);
    return () => o.disconnect();
  }, []);
  return [ref, width] as const;
}

// nice returns a round axis maximum split into 4 steps. With base 1024 the
// rounding happens in the display unit (KB, MB...) so ticks read 100 KB/s, not 97.7.
function nice(v: number, floor: number, integer?: boolean, base?: number) {
  v = Math.max(v, floor);
  let unit = base ? base ** Math.floor(Math.log(v) / Math.log(base)) : 1;
  if (base && v / unit > 500) unit *= base;
  const raw = v / unit / 4, e = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((k) => k * e).find((s) => s >= raw && (!integer || Number.isInteger(s)))!;
  return step * 4 * unit;
}
const TICK_STEPS = [M, 2 * M, 5 * M, 10 * M, 15 * M, 30 * M, H, 2 * H, 3 * H, 6 * H, 12 * H, D, 2 * D, 5 * D, 7 * D, 14 * D, 30 * D];
function timeTicks(from: number, to: number, width: number) {
  const step = TICK_STEPS.find((s) => (s / (to - from)) * width >= 64) ?? TICK_STEPS.at(-1)!;
  const fmt = step < D ? F.hm : F.dm, out: { t: number; label: string }[] = [];
  for (let t = Math.ceil((from + VN) / step) * step - VN; t <= to; t += step) out.push({ t, label: fmt.format(t) });
  return out;
}

export interface Point { t: number; v: number[] }

type XY = [number, number];
// curve: monotone cubic segments through the points (d3's curveMonotoneX): smooth,
// but it never overshoots, so a line never seems to dip below 0 or above a peak.
function curve(p: XY[]): [XY, XY, XY][] {
  const n = p.length;
  const dx = (i: number) => p[i + 1][0] - p[i][0];
  const m = (i: number) => (p[i + 1][1] - p[i][1]) / (dx(i) || 1);
  const t = p.map((_, i) => {
    if (i === 0) return m(0);
    if (i === n - 1) return m(n - 2);
    const a = m(i - 1), b = m(i);
    return a * b <= 0 ? 0 : (3 * (dx(i - 1) + dx(i))) / ((2 * dx(i) + dx(i - 1)) / a + (dx(i) + 2 * dx(i - 1)) / b);
  });
  return p.slice(0, -1).map((q, i) => {
    const h = dx(i) / 3;
    return [[q[0] + h, q[1] + t[i] * h], [p[i + 1][0] - h, p[i + 1][1] - t[i + 1] * h], p[i + 1]];
  });
}
const f = (q: XY) => `${q[0].toFixed(1)},${q[1].toFixed(1)}`;
const forward = (p: XY[]) => curve(p).map(([a, b, c]) => `C${f(a)} ${f(b)} ${f(c)}`).join("");
// the same curve walked back: every segment reversed, control points swapped
const backward = (p: XY[]) => curve(p).map(([a, b], i) => `C${f(b)} ${f(a)} ${f(p[i])}`).reverse().join("");

export function TimeChart({ label, points, series, from, to, step, fmt, stacked, area, integer, base, floor, empty }: {
  label: string; points: Point[]; series: { name: string; color: string }[]; from: number; to: number; step: number;
  fmt: (v: number) => string; stacked?: boolean; area?: boolean; integer?: boolean; base?: number; floor: number; empty: string;
}) {
  const [ref, W] = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const Hh = 220, m = { l: 62, r: 10, t: 10, b: 24 };
  const iw = W - m.l - m.r, ih = Hh - m.t - m.b;
  // tops[s][i]: the upper edge of series s at point i (cumulative when stacked)
  const tops = series.map((_, s) => points.map((p) => (stacked ? p.v.slice(0, s + 1).reduce((a, b) => a + b, 0) : p.v[s])));
  const vmax = Math.max(0, ...tops.flat());
  const ymax = nice(vmax, floor, integer, base);
  const x = (t: number) => m.l + ((t - from) / (to - from)) * iw;
  const y = (v: number) => m.t + ih - (v / ymax) * ih;

  // runs: stretches of consecutive samples; a gap in the data stays a gap.
  const runs: [number, number][] = [];
  points.forEach((p, i) => {
    if (i === 0 || p.t - points[i - 1].t > step * 2.5) runs.push([i, i]);
    else runs[runs.length - 1][1] = i;
  });
  const xy = (values: number[], a: number, b: number) => points.slice(a, b + 1).map((q, i): XY => [x(q.t), y(values[a + i])]);
  const edge = (values: number[], a: number, b: number) => {
    const q = xy(values, a, b);
    return `M${f(q[0])}` + (a === b ? "h0.5" : forward(q));
  };
  // a band: its upper edge forward, then its lower edge (the series below, or 0) back
  const fill = (s: number, a: number, b: number) => {
    const top = xy(tops[s], a, b);
    const low = stacked && s > 0 ? xy(tops[s - 1], a, b) : top.map(([px]): XY => [px, y(0)]);
    return `M${f(top[0])}${forward(top)}L${f(low[low.length - 1])}${backward(low)}Z`;
  };
  const uid = useId().replace(/:/g, "");

  const move = (ev: React.PointerEvent<SVGRectElement>) => {
    const r = ev.currentTarget.ownerSVGElement!.getBoundingClientRect();
    const t = from + (((ev.clientX - r.left) * (W / r.width) - m.l) / iw) * (to - from);
    let best = -1;
    points.forEach((p, i) => { if (best < 0 || Math.abs(p.t - t) < Math.abs(points[best].t - t)) best = i; });
    setHover(best >= 0 && Math.abs(points[best].t - t) <= Math.max(step * 1.5, ((to - from) / iw) * 8) ? best : null);
  };
  const p = hover !== null ? points[hover] : undefined;

  return (
    <div ref={ref} className="chart relative" style={{ height: Hh }}>
      <svg width={W} height={Hh} role="img" aria-label={`${label}, ${points.length} samples, max ${fmt(vmax)}`}>
        <defs>
          {/* soft fills: a wash of the series colour that fades towards the baseline */}
          {series.map((s, i) => (
            <linearGradient key={s.name} id={`${uid}-${i}`} x1={0} x2={0} y1={m.t} y2={m.t + ih} gradientUnits="userSpaceOnUse">
              <stop offset="0" style={{ stopColor: s.color, stopOpacity: stacked ? 0.55 : 0.3 }} />
              <stop offset="1" style={{ stopColor: s.color, stopOpacity: stacked ? 0.3 : 0.02 }} />
            </linearGradient>
          ))}
        </defs>
        {[0, 1, 2, 3, 4].map((i) => {
          const yy = Math.round(y((ymax * i) / 4)) + 0.5;
          return (
            <g key={i}>
              <line x1={m.l} x2={W - m.r} y1={yy} y2={yy} className={i ? "grid-line" : "axis-line"} />
              <text x={m.l - 8} y={yy + 4} className="tick" textAnchor="end">{i ? fmt((ymax * i) / 4) : "0"}</text>
            </g>
          );
        })}
        {timeTicks(from, to, iw).map((tk) => {
          const xx = x(tk.t);
          return xx < m.l + 14 || xx > W - m.r - 14 ? null : <text key={tk.t} x={xx} y={Hh - 6} className="tick" textAnchor="middle">{tk.label}</text>;
        })}
        {(area || stacked) && series.map((s, i) => runs.map(([a, b]) => a < b &&
          <path key={`f-${s.name}-${a}`} d={fill(i, a, b)} style={{ fill: `url(#${uid}-${i})` }} />))}
        {/* each band is outlined in its own colour, not separated by dark gaps */}
        {series.map((s, i) => runs.map(([a, b]) => (
          <path key={`l-${s.name}-${a}`} d={edge(tops[i], a, b)} className="line" style={{ stroke: s.color, strokeWidth: stacked ? 1.5 : 2 }} />
        )))}
        {p && (
          <>
            <line x1={x(p.t)} x2={x(p.t)} y1={m.t} y2={m.t + ih} className="axis-line" />
            {series.map((s, i) => <circle key={s.name} cx={x(p.t)} cy={y(tops[i][hover!])} r={4} strokeWidth={2} style={{ fill: s.color, stroke: "var(--surface)" }} />)}
          </>
        )}
        <rect x={m.l} y={0} width={iw} height={m.t + ih} fill="transparent" className="cursor-crosshair" onPointerMove={move} onPointerLeave={() => setHover(null)} />
      </svg>
      {!points.length && <div className="absolute inset-0 grid place-items-center text-sm text-muted">{empty}</div>}
      {p && (
        <div className="pointer-events-none absolute top-1.5 z-10 min-w-40 rounded-xl border border-border bg-overlay p-2.5 text-xs shadow-lg"
          style={x(p.t) > W / 2 ? { right: W - x(p.t) + 12 } : { left: x(p.t) + 12 }}>
          <div className="mb-1 text-muted">{F.dmhm.format(p.t)}{step > M ? ` (${Math.round(step / M)} min)` : ""}</div>
          {series.map((s, i) => (
            <div key={s.name} className="flex items-center gap-1.5">
              <i className="inline-block size-2.5 rounded-[3px]" style={{ background: s.color }} />
              {s.name}<b className="num ml-auto pl-3">{fmt(p.v[i])}</b>
            </div>
          ))}
          {stacked && <div className="mt-1 flex border-t border-border pt-1">Total<b className="num ml-auto">{fmt(p.v.reduce((a, b) => a + b, 0))}</b></div>}
        </div>
      )}
    </div>
  );
}

export function Sparkline({ values }: { values: number[] }) {
  if (values.length < 2) return <div className="h-7" />;
  const max = Math.max(1, ...values);
  const d = values.map((v, i) => `${i ? "L" : "M"}${(i / (values.length - 1)) * 100},${26 - (v / max) * 24}`).join("");
  return (
    <svg viewBox="0 0 100 28" preserveAspectRatio="none" className="h-7 w-full" aria-hidden="true">
      <path d={d} fill="none" stroke="var(--s1)" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// Heatmap: the usual number of sessions per weekday and hour, one blue ramp.
export function Heatmap({ cells }: { cells: Heat["cells"] }) {
  const [hover, setHover] = useState("");
  const max = Math.max(0, ...cells.map((c) => c.avg));
  const at = new Map(cells.map((c) => [c.day * 24 + c.hour, c]));
  return (
    <div>
      <div className="overflow-x-auto">
        <div className="grid min-w-[560px] gap-0.5" style={{ gridTemplateColumns: "2.5rem repeat(24, minmax(0, 1fr))" }}>
          {[1, 2, 3, 4, 5, 6, 0].map((day) => [
            <span key={day} className="text-xs leading-5 text-muted">{WEEKDAYS[day]}</span>,
            ...Array.from({ length: 24 }, (_, h) => {
              const c = at.get(day * 24 + h);
              const level = c && c.avg > 0 ? Math.min(6, 1 + Math.floor((c.avg / max) * 5.999)) : 0;
              const text = `${WEEKDAYS[day]} ${String(h).padStart(2, "0")}:00 · ${c ? `${c.avg.toFixed(1)} online on average, peak ${c.max}` : "no data"}`;
              return <div key={`${day}-${h}`} className="h-5 rounded-[3px]" title={text} role="img" aria-label={text}
                style={{ background: `var(--h${level})` }} onPointerEnter={() => setHover(text)} onPointerLeave={() => setHover("")} />;
            }),
          ])}
          <span />
          {Array.from({ length: 24 }, (_, h) => <span key={h} className="num text-center text-[10px] text-muted">{h % 3 === 0 ? h : ""}</span>)}
        </div>
      </div>
      <div className="mt-2 flex items-center gap-2 text-xs text-muted">
        <span>0</span>
        {[1, 2, 3, 4, 5, 6].map((l) => <i key={l} className="inline-block size-3 rounded-[3px]" style={{ background: `var(--h${l})` }} />)}
        <span>{max.toFixed(1)} sessions</span>
        <span className="ml-auto" aria-live="polite">{hover}</span>
      </div>
    </div>
  );
}

// Timeline: one row per day (Asia/Ho_Chi_Minh), a bar for every session over the 24 hours.
export function Timeline({ sessions, days = 14 }: { sessions: SessionRecord[]; days?: number }) {
  const today = Math.floor((Date.now() + VN) / D);
  return (
    <div className="space-y-0.5">
      {Array.from({ length: days }, (_, i) => today - i).map((day) => {
        const start = day * D - VN, end = start + D;
        return (
          <div key={day} className="flex items-center gap-2">
            <span className="num w-11 shrink-0 text-xs text-muted">{F.dm.format(start + 12 * H)}</span>
            <div className="relative h-3.5 grow overflow-hidden rounded-[3px] bg-default">
              {sessions.map((s) => {
                const a = Math.max(start, Date.parse(s.start)), b = Math.min(end, s.open ? Date.now() : Date.parse(s.end));
                if (b <= a) return null;
                return <i key={`${s.instance}-${s.cid}-${s.start}`} className="absolute inset-y-0 min-w-0.5 rounded-[2px]"
                  title={`${s.profile} ${s.proto.toUpperCase()} · ${F.hm.format(a)}–${F.hm.format(b)} · ${duration((b - a) / 1000)}`}
                  style={{ left: `${((a - start) / D) * 100}%`, width: `${((b - a) / D) * 100}%`, background: "var(--s1)" }} />;
              })}
            </div>
          </div>
        );
      })}
      <div className="num flex justify-between pl-13 text-[10px] text-muted"><span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>24:00</span></div>
    </div>
  );
}

// DayBars: traffic per day, upload and download stacked with a 2px gap.
export function DayBars({ days }: { days: UserDetail["days"] }) {
  const byDay = new Map(days.map((d) => [d.day, d]));
  // every day gets a slot, so quiet days show as gaps
  const last = Array.from({ length: 30 }, (_, i) => F.day.format(Date.now() - (29 - i) * D))
    .map((day) => byDay.get(day) ?? { day, bytes_rx: 0, bytes_tx: 0 });
  const max = Math.max(1, ...last.map((d) => d.bytes_rx + d.bytes_tx));
  return (
    <div className="space-y-2">
      <Legend items={[{ name: "↑ from client", color: "var(--s1)" }, { name: "↓ to client", color: "var(--s2)" }]} />
      <div className="flex h-28 items-end gap-0.5 border-b border-border">
        {last.map((d) => (
          <div key={d.day} className="flex h-full min-w-0 flex-1 flex-col justify-end gap-0.5" title={`${d.day}\n↑ ${bytes(d.bytes_rx)}\n↓ ${bytes(d.bytes_tx)}`}>
            <i className="block rounded-t-[4px]" style={{ height: `${(d.bytes_tx / max) * 100}%`, background: "var(--s2)" }} />
            <i className="block" style={{ height: `${(d.bytes_rx / max) * 100}%`, background: "var(--s1)" }} />
          </div>
        ))}
      </div>
      <div className="num flex justify-between text-[10px] text-muted"><span>{last[0]?.day}</span><span>max {bytes(max)}</span><span>{last.at(-1)?.day}</span></div>
    </div>
  );
}
