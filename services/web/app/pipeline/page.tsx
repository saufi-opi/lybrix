"use client";

/** Pipeline live view (2026-09-10): component strip, queue lanes end-to-end,
 *  in-flight jobs. Polls /v1/system/pipeline every 5s via apiClient. */

import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api-client";

interface Lane {
  waiting: number | null;
  in_flight: number | null;
  stale: number;
  consumers: number;
}
interface Pipeline {
  components: Record<string, string>;
  lanes: Record<string, Lane>;
  counts: Record<string, number>;
  qdrant_points: number | null;
  in_flight_parse: {
    title: string | null;
    idx: number;
    page_start: number;
    page_end: number;
    worker_id: string | null;
  }[];
}

const COMP_LABELS: Record<string, string> = {
  postgres: "postgres",
  redis: "redis",
  qdrant: "qdrant",
  tei_query: "tei-query",
  embed_backend: "embed (jetson)",
};

const STREAM_LABELS: Record<string, string> = {
  "doc.split": "doc.split",
  "doc.parse": "doc.parse",
  "doc.embed": "doc.embed",
};

function Dot({ state }: { state: string }) {
  return <span className={`pdot ${state === "ok" ? "pok" : "perr"}`} />;
}

function LaneRow({ name, lane }: { name: string; lane: Lane }) {
  return (
    <tr>
      <td className="mono">{STREAM_LABELS[name] ?? name}</td>
      <td>{lane.waiting ?? "—"}</td>
      <td>{lane.in_flight ?? "—"}</td>
      <td className={lane.stale > 0 ? "pwarn" : "muted"}>{lane.stale}</td>
      <td>{lane.consumers}</td>
    </tr>
  );
}

/** Visual queue lane between pipeline stages (mockup v2 style). */
function Lane({ name, lane, sample }: { name: string; lane?: Lane; sample: string }) {
  if (!lane) return <div className="lane" />;
  const pills: { cls: string; label: string }[] = [];
  if (lane.in_flight && lane.in_flight > 0) {
    pills.push({ cls: "r", label: `${lane.in_flight} in-flight` });
  }
  if (lane.stale > 0) {
    pills.push({ cls: "s", label: `⏳ ${lane.stale} stale` });
  }
  pills.push({ cls: "w", label: `+${(lane.waiting ?? 0).toLocaleString()} waiting` });
  return (
    <div className="lane">
      <div className="lane-hd">
        <span className="nm">{name}</span>
        <span className="n">{lane.waiting?.toLocaleString() ?? "—"}</span>
      </div>
      <div className="lane-box">
        {pills.slice(0, 3).map((p, i) => (
          <span key={i} className={`pill ${p.cls}`}>{p.label}</span>
        ))}
      </div>
      <div className="wire" />
    </div>
  );
}

export default function PipelinePage() {
  const [data, setData] = useState<Pipeline | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [history, setHistory] = useState<number[]>([]);
  const lastDone = useRef<number | null>(null);

  const refresh = useCallback(async () => {
    try {
      const d = (await apiClient.pipeline()) as unknown as Pipeline;
      setData(d);
      setErr(null);
      const done = d.counts.shards_done ?? 0;
      const prev = lastDone.current;
      if (prev !== null && done >= prev) {
        setHistory((h) => [...h.slice(-59), done - prev]);
      }
      lastDone.current = done;
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, [refresh]);

  if (err && !data) {
    return <div className="panel">pipeline API unreachable: {err}</div>;
  }
  if (!data) return <div className="muted">loading pipeline…</div>;

  const c = data.counts;
  const samples = history.length;
  const rate = samples > 0 ? history.reduce((a, b) => a + b, 0) / (samples / 12) : null;
  const etaH =
    rate && rate > 0 && (c.shards_pending ?? 0) > 0
      ? ((c.shards_pending as number) / rate / 60).toFixed(1)
      : null;
  const readyPct =
    c.docs_total ? Math.round(((c.docs_ready ?? 0) / c.docs_total) * 100) : 0;
  const inFlightParse = data.lanes["doc.parse"]?.in_flight ?? 0;
  const qdrantPoints = data.qdrant_points;

  return (
    <>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", marginBottom: "0.75rem" }}>
        <h2 style={{ margin: 0, fontSize: 18, fontWeight: 700 }}>Pipeline <span style={{ color: "var(--accent)" }}>Live View</span></h2>
        <span className="live-badge">
          {err ? <><span className="pdot perr" /> retrying…</> : <><span className="pdot pok" /> live · 5s</>}
        </span>
      </div>
      <div className="grid5">
        <div className="panel counter">
          <div className="num">
            {c.docs_ready ?? "—"}
            <span className="muted" style={{ fontSize: "0.55em" }}>/{c.docs_total ?? "—"}</span>
          </div>
          <div className="muted" style={{ fontSize: 11 }}>
            docs ready · {readyPct}%
          </div>
        </div>
        <div className="panel counter">
          <div className="num">{c.shards_done ?? "—"}</div>
          <div className="muted" style={{ fontSize: 11 }}>
            shards done · <span style={{ color: "var(--ok)" }}>{c.shards_failed ?? 0} failed</span>
          </div>
        </div>
        <div className="panel counter">
          <div className="num">{c.shards_pending ?? "—"}</div>
          <div className="muted" style={{ fontSize: 11 }}>pending shards</div>
        </div>
        <div className="panel counter">
          <div className="num" style={{ color: "var(--accent)" }}>
            {rate !== null ? (
              <>
                {rate.toFixed(1)}
                <span className="muted" style={{ fontSize: "0.55em" }}>/min</span>
              </>
            ) : (
              <span className="muted">measuring…</span>
            )}
          </div>
          <div className="muted" style={{ fontSize: 11 }}>live throughput</div>
        </div>
        <div className="panel counter">
          <div className="num" style={{ color: "var(--ok)" }}>
            {etaH ? `~${etaH}h` : <span className="muted">—</span>}
          </div>
          <div className="muted" style={{ fontSize: 11 }}>ETA at live rate</div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: "1rem" }}>
        <h3>Components</h3>
        <div className="compstrip">
          {Object.entries(data.components).map(([name, state]) => (
            <span key={name} className="compchip">
              <Dot state={state} /> {COMP_LABELS[name] ?? name}
            </span>
          ))}
        </div>
      </div>

      {/* ===== visual pipeline flow: stage → lane → stage ===== */}
      <div className="panel" style={{ marginBottom: "1rem", overflowX: "auto" }}>
        <h3>Flow</h3>
        <div className="flow">
          <div className="stage">
            <h4><span className="stepno">1</span> Split</h4>
            <div className="big">{c.docs_parsing + c.docs_ready} docs</div>
            <div className="sm">splitter · nssp</div>
          </div>
          <Lane name="doc.split" lane={data.lanes["doc.split"]} sample="docs" />
          <div className="stage">
            <h4><span className="stepno">2</span> Parse ×{data.lanes["doc.parse"]?.consumers ?? "—"}</h4>
            <div className="big">{inFlightParse} active</div>
            <div className="sm">nssp×3 + nsschat×3<br/>avg 124s/shard</div>
          </div>
          <Lane name="doc.parse" lane={data.lanes["doc.parse"]} sample="shards" />
          <div className="stage">
            <h4><span className="stepno">3</span> Embed</h4>
            <div className="big">{data.components.embed_backend === "ok" ? "jetson gpu" : "embed down"}</div>
            <div className="sm">ollama bge-m3</div>
          </div>
          <Lane name="doc.embed" lane={data.lanes["doc.embed"]} sample="docs" />
          <div className="stage">
            <h4><span className="stepno">4</span> Index</h4>
            <div className="big">{qdrantPoints ?? "—"} pts</div>
            <div className="sm">qdrant chunks</div>
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: "1rem" }}>
        <h3>Queue lanes — waiting / in-flight / stale / consumers</h3>
        <table>
          <thead>
            <tr>
              <th>stream</th><th>waiting</th><th>in-flight</th><th>stale &gt;15m</th><th>active consumers</th>
            </tr>
          </thead>
          <tbody>
            {Object.entries(data.lanes).map(([name, lane]) => (
              <LaneRow key={name} name={name} lane={lane} />
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel">
        <h3>In-flight parse jobs (lease holders)</h3>
        {data.in_flight_parse.length === 0 ? (
          <div className="muted">no shard currently claimed</div>
        ) : (
          <table>
            <thead>
              <tr><th>doc</th><th>shard</th><th>pages</th><th>worker</th></tr>
            </thead>
            <tbody>
              {data.in_flight_parse.map((j, i) => (
                <tr key={i}>
                  <td>{j.title ?? "(untitled)"}</td>
                  <td>#{j.idx}</td>
                  <td className="mono">{j.page_start}–{j.page_end}</td>
                  <td className="muted mono">{j.worker_id ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {err && (
        <div className="muted" style={{ marginTop: "0.5rem", fontSize: 12 }}>
          last refresh failed: {err} — retrying in 5s
        </div>
      )}
    </>
  );
}
