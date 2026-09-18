"use client";

/** Usage (Phase 2): summary cards + per-key table, aggregated from key_usage
 * via GET /api/admin/v1/usage/summary. Period switcher: 24h / 7d / 30d. */

import { useCallback, useEffect, useState } from "react";

interface KeyUsageRow {
  key_id: string;
  name: string | null;
  calls: number;
  last_used_at: string | null;
}
interface Summary {
  period: string;
  total: number;
  by_key: KeyUsageRow[];
}

type Period = "24h" | "7d" | "30d";
const PERIODS: Period[] = ["24h", "7d", "30d"];

export default function UsagePage() {
  const [period, setPeriod] = useState<Period>("24h");
  const [current, setCurrent] = useState<Summary | null>(null);
  const [week, setWeek] = useState<Summary | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async (p: Period) => {
    setLoading(true);
    try {
      const [c, w] = await Promise.all([
        fetch(`/api/admin/v1/usage/summary?period=${p}`).then((r) => {
          if (!r.ok) throw new Error(`API ${r.status}`);
          return r.json() as Promise<Summary>;
        }),
        fetch("/api/admin/v1/usage/summary?period=7d").then((r) =>
          r.ok ? (r.json() as Promise<Summary>) : null,
        ),
      ]);
      setCurrent(c);
      setWeek(w);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(period);
  }, [period, load]);

  const top = current?.by_key[0];
  const activeKeys = current?.by_key.filter((k) => k.calls > 0).length ?? 0;

  return (
    <>
      <div className="panel">
        <div className="panel-head">
          <h2>Usage</h2>
          <div className="seg">
            {PERIODS.map((p) => (
              <button
                key={p}
                className={p === period ? "seg-active" : undefined}
                onClick={() => setPeriod(p)}
              >
                {p}
              </button>
            ))}
          </div>
        </div>
        {error && <p className="login-error">{error}</p>}
        {loading && !current ? (
          <p className="muted">Loading usage…</p>
        ) : (
          <>
            <div className="grid4">
              <div className="counter stat-card">
                <div className="num">{current?.total ?? "—"}</div>
                <p className="muted">calls · {period}</p>
              </div>
              <div className="counter stat-card">
                <div className="num">{week?.total ?? "—"}</div>
                <p className="muted">calls · 7d</p>
              </div>
              <div className="counter stat-card">
                <div className="num">{activeKeys}</div>
                <p className="muted">keys used · {period}</p>
              </div>
              <div className="counter stat-card">
                <div className="num top-key">
                  {top ? (top.name ?? top.key_id.slice(0, 8)) : "—"}
                </div>
                <p className="muted">top key {top ? `· ${top.calls} calls` : ""}</p>
              </div>
            </div>
            {current && current.by_key.length > 0 && (
              <table className="usage-table">
                <thead>
                  <tr>
                    <th>key</th>
                    <th>calls</th>
                    <th>last used</th>
                  </tr>
                </thead>
                <tbody>
                  {current.by_key.map((k) => (
                    <tr key={k.key_id}>
                      <td>{k.name ?? <code>{k.key_id.slice(0, 8)}</code>}</td>
                      <td>{k.calls}</td>
                      <td className="muted">
                        {k.last_used_at
                          ? new Date(k.last_used_at).toLocaleString()
                          : "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {current && current.by_key.length === 0 && (
              <p className="muted">No authenticated calls in the last {period}.</p>
            )}
          </>
        )}
      </div>
    </>
  );
}
