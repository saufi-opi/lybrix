"use client";

/** API key management UI (Phase 2). All fetches go through /api/admin/* so
 * the browser never holds a bearer key. Raw keys are shown exactly once. */

import { useCallback, useEffect, useState } from "react";

interface KeyRow {
  id: string;
  name: string | null;
  scopes: string[] | null;
  collections: string[] | null;
  last_used_at: string | null;
  expires_at: string | null;
  revoked_at: string | null;
}

interface CollectionRow {
  id: string;
  name: string;
}

type Period = "1d" | "7d" | "30d" | "90d" | "never";

const EXPIRY_OPTIONS: { value: Period; label: string }[] = [
  { value: "never", label: "Never expires" },
  { value: "1d", label: "1 day" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
];

async function adminFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    const detail = await res.text();
    throw new Error(`API ${res.status}: ${detail.slice(0, 200)}`);
  }
  return res.json() as Promise<T>;
}

function relTime(iso: string | null): string {
  if (!iso) return "never";
  const diff = Date.now() - new Date(iso).getTime();
  const m = Math.round(diff / 60_000);
  if (m < 1) return "just now";
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

function expiresIn(iso: string | null): string | null {
  if (!iso) return null;
  const days = Math.ceil((new Date(iso).getTime() - Date.now()) / 86_400_000);
  if (days < 0) return null; // expired — status badge covers it
  if (days === 0) return "expires today";
  return `expires in ${days}d`;
}

function StatusBadge({ row }: { row: KeyRow }) {
  if (row.revoked_at) return <span className="badge revoked">revoked</span>;
  if (row.expires_at && new Date(row.expires_at).getTime() < Date.now())
    return <span className="badge expired">expired</span>;
  if (row.expires_at && new Date(row.expires_at).getTime() - Date.now() < 7 * 86_400_000)
    return <span className="badge expiring">expiring</span>;
  return <span className="badge active">active</span>;
}

const SCOPE_CLASS: Record<string, string> = {
  search: "chip search",
  ingest: "chip ingest",
  admin: "chip admin",
};

export function KeysManager() {
  const [keys, setKeys] = useState<KeyRow[] | null>(null);
  const [collections, setCollections] = useState<CollectionRow[]>([]);
  const [error, setError] = useState<string | null>(null);

  // create-modal state
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["search"]));
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [expiry, setExpiry] = useState<Period>("never");
  const [busy, setBusy] = useState(false);

  // copy-once state
  const [rawKey, setRawKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // revoke state
  const [revoking, setRevoking] = useState<KeyRow | null>(null);

  const load = useCallback(async () => {
    try {
      const [k, c] = await Promise.all([
        adminFetch<KeyRow[]>("/api/admin/v1/keys"),
        adminFetch<CollectionRow[]>("/api/admin/v1/collections"),
      ]);
      setKeys(k);
      setCollections(c);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setKeys([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function toggleScope(s: string) {
    setScopes((prev) => {
      const next = new Set(prev);
      if (next.has(s)) next.delete(s);
      else next.add(s);
      return next;
    });
  }

  function toggleCollection(id: string) {
    setPicked((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function createKey(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || scopes.size === 0) return;
    setBusy(true);
    try {
      const created = await adminFetch<KeyRow & { raw_key: string }>("/api/admin/v1/keys", {
        method: "POST",
        body: JSON.stringify({
          name: name.trim(),
          scopes: [...scopes],
          collections: picked.size ? [...picked] : null,
          expires_in: expiry,
        }),
      });
      setCreating(false);
      setName("");
      setScopes(new Set(["search"]));
      setPicked(new Set());
      setExpiry("never");
      setCopied(false);
      setRawKey(created.raw_key);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function revokeKey() {
    if (!revoking) return;
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/keys/${revoking.id}/revoke`, { method: "POST" });
      setRevoking(null);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  function copyRaw() {
    if (!rawKey) return;
    void navigator.clipboard.writeText(rawKey).then(() => setCopied(true));
  }

  if (keys === null) return <p className="muted">Loading keys…</p>;

  return (
    <>
      <div className="panel">
        <div className="panel-head">
          <h2>API keys</h2>
          <button type="button" className="primary" onClick={() => setCreating(true)}>
            + Create key
          </button>
        </div>
        <p className="muted">
          Bearer keys for the search/ingest API and MCP. Raw keys are stored hashed (sha256) and
          shown exactly once at creation.
        </p>
        {error && <p className="login-error">{error}</p>}
        {keys.length === 0 ? (
          <div className="empty-state">
            <div className="empty-icon">◇</div>
            <p className="empty-title">No API keys yet</p>
            <p className="muted">Create your first API key to start calling the API or MCP.</p>
          </div>
        ) : (
          <div className="key-list">
            {keys.map((k) => {
              const exp = !k.revoked_at ? expiresIn(k.expires_at) : null;
              return (
                <div className="key-card" key={k.id}>
                  <div className="key-card-top">
                    <span className="key-name">{k.name ?? "(unnamed)"}</span>
                    <StatusBadge row={k} />
                    <button
                      type="button"
                      className="btn-danger"
                      disabled={!!k.revoked_at || busy}
                      onClick={() => setRevoking(k)}
                    >
                      Revoke
                    </button>
                  </div>
                  <div className="key-card-meta">
                    {(k.scopes ?? []).map((s) => (
                      <span key={s} className={SCOPE_CLASS[s] ?? "chip"}>
                        {s}
                      </span>
                    ))}
                    {(k.collections ?? []).map((c) => (
                      <span key={c} className="badge coll">
                        {c}
                      </span>
                    ))}
                    {k.collections === null && <span className="badge coll">all collections</span>}
                  </div>
                  <div className="key-card-foot muted">
                    <span>last used {relTime(k.last_used_at)}</span>
                    {exp && <span> · {exp}</span>}
                    {k.expires_at && (
                      <span className="key-exp-abs">
                        {" "}
                        · until {new Date(k.expires_at).toLocaleDateString()}
                      </span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {creating && (
        // biome-ignore lint/a11y/noStaticElementInteractions: modal overlay click-to-close is intentional
        <div
          className="modal-overlay"
          onClick={() => !busy && setCreating(false)}
          role="presentation"
        >
          <form
            className="modal"
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
            onSubmit={createKey}
          >
            <h3>Create API key</h3>
            <label className="field">
              <span>Name</span>
              <input
                placeholder="e.g. ci-pipeline"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </label>
            <div className="field">
              <span>Scopes</span>
              <div className="check-row">
                {["search", "ingest", "admin"].map((s) => (
                  <label key={s} className="check">
                    <input
                      type="checkbox"
                      checked={scopes.has(s)}
                      onChange={() => toggleScope(s)}
                    />
                    {s}
                    {s === "admin" && <em className="check-warn">full control</em>}
                  </label>
                ))}
              </div>
            </div>
            <div className="field">
              <span>Collections</span>
              {collections.length === 0 ? (
                <p className="muted">No collections — key will be unrestricted (all).</p>
              ) : (
                <div className="check-row wrap">
                  {collections.map((c) => (
                    <label key={c.id} className="check">
                      <input
                        type="checkbox"
                        checked={picked.has(c.id)}
                        onChange={() => toggleCollection(c.id)}
                      />
                      {c.id}
                    </label>
                  ))}
                  <p className="muted">none selected = all collections</p>
                </div>
              )}
            </div>
            <label className="field">
              <span>Expires</span>
              <select value={expiry} onChange={(e) => setExpiry(e.target.value as Period)}>
                {EXPIRY_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
            <div className="modal-actions">
              <button type="button" onClick={() => setCreating(false)} disabled={busy}>
                Cancel
              </button>
              <button className="primary" type="submit" disabled={busy || scopes.size === 0}>
                {busy ? "Creating…" : "Create key"}
              </button>
            </div>
          </form>
        </div>
      )}

      {rawKey && (
        <div className="modal-overlay">
          <div className="modal">
            <h3>Key created</h3>
            <p className="warn-note">This key will not be shown again — copy it now.</p>
            <pre className="copy-box">{rawKey}</pre>
            <div className="modal-actions">
              <button type="button" onClick={copyRaw}>
                {copied ? "Copied ✓" : "Copy key"}
              </button>
              <button type="button" className="primary" onClick={() => setRawKey(null)}>
                Done
              </button>
            </div>
          </div>
        </div>
      )}

      {revoking && (
        // biome-ignore lint/a11y/useKeyWithClickEvents: modal overlay click-to-close, keyboard handled by buttons
        // biome-ignore lint/a11y/noStaticElementInteractions: modal overlay + dialog stopPropagation is intentional
        <div className="modal-overlay" onClick={() => !busy && setRevoking(null)}>
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
          >
            <h3>Revoke key</h3>
            <p>
              Revoke <strong>{revoking.name ?? "(unnamed)"}</strong>? Requests using this key will
              start failing immediately with 401. This cannot be undone.
            </p>
            <div className="modal-actions">
              <button type="button" onClick={() => setRevoking(null)} disabled={busy}>
                Cancel
              </button>
              <button type="button" className="btn-danger" onClick={revokeKey} disabled={busy}>
                {busy ? "Revoking…" : "Revoke key"}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
