"use client";

/** API key management UI (Phase 2). All fetches go through /api/admin/* so
 * the browser never holds a bearer key. Raw keys are shown exactly once.
 * Create / raw-key / revoke modals are shadcn Dialogs; feedback via sonner. */

import { cn } from "cn";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { StateBadge } from "@/components/state-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

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

function keyStatus(row: KeyRow): string {
  if (row.revoked_at) return "revoked";
  if (row.expires_at && new Date(row.expires_at).getTime() < Date.now()) return "expired";
  if (row.expires_at && new Date(row.expires_at).getTime() - Date.now() < 7 * 86_400_000)
    return "expiring";
  return "active";
}

const SCOPE_CLASS: Record<string, string> = {
  search: "border-press bg-press-wash text-press-deep",
  ingest: "border-warning bg-ochre-wash text-warning",
  admin: "border-ledger bg-ledger-wash text-ledger",
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
      toast.success(`API key "${created.name ?? created.id.slice(0, 8)}" created`, {
        description: "Copy the raw key now — it will not be shown again.",
      });
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
      toast.error("create failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function revokeKey() {
    if (!revoking) return;
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/keys/${revoking.id}/revoke`, { method: "POST" });
      toast.success(`key "${revoking.name ?? revoking.id.slice(0, 8)}" revoked`);
      setRevoking(null);
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
      toast.error("revoke failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  function copyRaw() {
    if (!rawKey) return;
    void navigator.clipboard
      .writeText(rawKey)
      .then(() => {
        setCopied(true);
        toast.success("copied to clipboard");
      })
      .catch(() => toast.error("copy failed — select the key manually"));
  }

  if (keys === null) return <p className="text-muted-foreground">Loading keys…</p>;

  return (
    <>
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <CardTitle className="font-serif text-[19px] font-semibold">API keys</CardTitle>
            <Button type="button" onClick={() => setCreating(true)}>
              + Create key
            </Button>
          </div>
          <CardDescription>
            Bearer keys for the search/ingest API and MCP. Raw keys are stored hashed (sha256) and
            shown exactly once at creation.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {error && (
            <p className="mt-0 mb-3 font-mono text-[12.5px] text-redink" role="alert">
              {error}
            </p>
          )}
          {keys.length === 0 ? (
            <div className="py-10 text-center">
              <div className="mb-2 text-[28px] text-muted-foreground">◇</div>
              <p className="m-0 mb-1 font-semibold">No API keys yet</p>
              <p className="m-0 text-muted-foreground">
                Create your first API key to start calling the API or MCP.
              </p>
            </div>
          ) : (
            <div className="mt-2 flex flex-col gap-2.5">
              {keys.map((k) => {
                const exp = !k.revoked_at ? expiresIn(k.expires_at) : null;
                return (
                  <div
                    key={k.id}
                    className="rounded-lg border border-sheet-edge bg-sheet px-4 py-3.5 transition-colors duration-150 hover:border-ink-faint"
                  >
                    <div className="flex flex-wrap items-center gap-2.5">
                      <span className="text-sm font-semibold">{k.name ?? "(unnamed)"}</span>
                      <StateBadge state={keyStatus(k)} />
                      <Button
                        type="button"
                        variant="destructive"
                        size="xs"
                        className="ml-auto"
                        disabled={!!k.revoked_at || busy}
                        onClick={() => setRevoking(k)}
                      >
                        Revoke
                      </Button>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1.5">
                      {(k.scopes ?? []).map((s) => (
                        <span
                          key={s}
                          className={cn(
                            "inline-block rounded-[2px] border px-2 py-0.5 font-mono text-[0.72rem] font-semibold",
                            SCOPE_CLASS[s] ??
                              "border-sheet-edge bg-paper-deep text-muted-foreground",
                          )}
                        >
                          {s}
                        </span>
                      ))}
                      {(k.collections ?? []).map((c) => (
                        <Badge
                          key={c}
                          className="rounded border-dashed bg-transparent font-mono text-[0.68rem] text-muted-foreground"
                        >
                          {c}
                        </Badge>
                      ))}
                      {k.collections === null && (
                        <Badge className="rounded border-dashed bg-transparent font-mono text-[0.68rem] text-muted-foreground">
                          all collections
                        </Badge>
                      )}
                    </div>
                    <div className="mt-2 text-xs text-muted-foreground">
                      <span>last used {relTime(k.last_used_at)}</span>
                      {exp && <span> · {exp}</span>}
                      {k.expires_at && (
                        <span className="text-muted-foreground/60">
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
        </CardContent>
      </Card>

      {/* ===== create modal ===== */}
      <Dialog open={creating} onOpenChange={(open) => !busy && setCreating(open)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">
              Create API key
            </DialogTitle>
          </DialogHeader>
          <form onSubmit={createKey} className="flex flex-col gap-3.5">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="key-name" className="text-xs font-semibold text-muted-foreground">
                Name
              </Label>
              <Input
                id="key-name"
                placeholder="e.g. ci-pipeline"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs font-semibold text-muted-foreground">Scopes</Label>
              <div className="flex items-center gap-4">
                {["search", "ingest", "admin"].map((s) => (
                  <Label key={s} className="cursor-pointer gap-1.5 text-[13px]">
                    <input
                      type="checkbox"
                      checked={scopes.has(s)}
                      onChange={() => toggleScope(s)}
                      className="size-[15px] cursor-pointer accent-[var(--press)]"
                    />
                    {s}
                    {s === "admin" && (
                      <em className="text-[11px] not-italic text-warning">full control</em>
                    )}
                  </Label>
                ))}
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs font-semibold text-muted-foreground">Collections</Label>
              {collections.length === 0 ? (
                <p className="m-0 text-muted-foreground">
                  No collections — key will be unrestricted (all).
                </p>
              ) : (
                <div className="flex flex-wrap items-center gap-3">
                  {collections.map((c) => (
                    <Label key={c.id} className="cursor-pointer gap-1.5 font-mono text-[12.5px]">
                      <input
                        type="checkbox"
                        checked={picked.has(c.id)}
                        onChange={() => toggleCollection(c.id)}
                        className="size-[15px] cursor-pointer accent-[var(--press)]"
                      />
                      {c.id}
                    </Label>
                  ))}
                  <p className="m-0 text-muted-foreground">none selected = all collections</p>
                </div>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="key-expiry" className="text-xs font-semibold text-muted-foreground">
                Expires
              </Label>
              <Select value={expiry} onValueChange={(v) => setExpiry(v as Period)}>
                <SelectTrigger id="key-expiry" className="w-full" aria-label="expiry">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EXPIRY_OPTIONS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setCreating(false)}
                disabled={busy}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={busy || scopes.size === 0}>
                {busy ? "Creating…" : "Create key"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* ===== raw-key modal (shown exactly once) ===== */}
      <Dialog open={rawKey !== null} onOpenChange={(open) => !open && setRawKey(null)}>
        <DialogContent showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">Key created</DialogTitle>
            <DialogDescription>This key will not be shown again — copy it now.</DialogDescription>
          </DialogHeader>
          <pre className="m-0 rounded border border-rail-edge bg-rail px-3 py-2.5 font-mono text-[12.5px] break-all whitespace-pre-wrap text-[#cde3d6]">
            {rawKey}
          </pre>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={copyRaw}>
              {copied ? "Copied ✓" : "Copy key"}
            </Button>
            <Button type="button" onClick={() => setRawKey(null)}>
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ===== revoke modal ===== */}
      <Dialog open={revoking !== null} onOpenChange={(open) => !open && !busy && setRevoking(null)}>
        <DialogContent showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">Revoke key</DialogTitle>
            <DialogDescription>
              Revoke <strong className="text-ink">{revoking?.name ?? "(unnamed)"}</strong>? Requests
              using this key will start failing immediately with 401. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setRevoking(null)}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button type="button" variant="destructive" onClick={revokeKey} disabled={busy}>
              {busy ? "Revoking…" : "Revoke key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
