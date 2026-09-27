import { useEffect, useState, type FormEvent } from "react";
import type { AdminClient } from "../api/client";
import type { ApiKey, CreatedApiKey, KeyStatus, Plan } from "../api/types";
import { SortableTable, type Column } from "../components/SortableTable";
import { Dialog, ErrorState, FieldError, FormError, Loading, Panel } from "../components/ui";
import { fmtDate } from "../format";
import { useAsync } from "../hooks";

const PAGE_SIZE = 100;

export function ApiKeys({ client }: { client: AdminClient }) {
  const [status, setStatus] = useState<KeyStatus | "">("");
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const [nextAfter, setNextAfter] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | undefined>(undefined);
  const [reloadNonce, setReloadNonce] = useState(0);
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<number | null>(null);
  const [actionError, setActionError] = useState<Error | undefined>(undefined);

  // First page; "Load more" appends following pages using the next_after cursor.
  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setError(undefined);
    client
      .listApiKeys({ ...(status ? { status } : {}), limit: PAGE_SIZE }, ctrl.signal)
      .then(
        (page) => {
          setKeys(page.api_keys);
          setNextAfter(page.next_after);
          setLoading(false);
        },
        (e: unknown) => {
          if (ctrl.signal.aborted) return;
          setError(e instanceof Error ? e : new Error(String(e)));
          setLoading(false);
        },
      );
    return () => ctrl.abort();
  }, [client, status, reloadNonce]);

  async function loadMore() {
    if (nextAfter === null) return;
    setLoading(true);
    try {
      const page = await client.listApiKeys({ ...(status ? { status } : {}), limit: PAGE_SIZE, after: nextAfter });
      setKeys((k) => [...k, ...page.api_keys]);
      setNextAfter(page.next_after);
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setLoading(false);
    }
  }

  async function revoke(key: ApiKey) {
    if (!window.confirm(`Revoke key ${key.id} for “${key.application}”? Requests using it will be rejected with 403 immediately.`)) return;
    setRevoking(key.id);
    setActionError(undefined);
    try {
      const updated = await client.revokeApiKey(key.id);
      setKeys((ks) => (status === "active" ? ks.filter((k) => k.id !== key.id) : ks.map((k) => (k.id === key.id ? updated : k))));
    } catch (e) {
      setActionError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setRevoking(null);
    }
  }

  const columns: Column<ApiKey>[] = [
    { key: "id", header: "ID", align: "right", sortValue: (k) => k.id, render: (k) => k.id },
    { key: "status", header: "Status", sortValue: (k) => k.status, render: (k) => <span className={`badge ${k.status}`}>{k.status}</span> },
    { key: "application", header: "Application", sortValue: (k) => k.application, render: (k) => k.application },
    { key: "owner", header: "Owner", sortValue: (k) => k.owner_email, render: (k) => <span className="break">{k.owner_email}</span> },
    { key: "plan", header: "Plan", sortValue: (k) => k.plan, render: (k) => k.plan },
    { key: "created", header: "Created", sortValue: (k) => k.created_at, render: (k) => fmtDate(k.created_at) },
    { key: "revoked", header: "Revoked", sortValue: (k) => k.revoked_at, render: (k) => fmtDate(k.revoked_at) },
    {
      key: "actions",
      header: "",
      render: (k) =>
        k.status === "active" ? (
          <button type="button" className="small danger" disabled={revoking === k.id} onClick={() => void revoke(k)}>
            {revoking === k.id ? "Revoking…" : "Revoke"}
          </button>
        ) : null,
    },
  ];

  return (
    <>
      <Panel
        title="API keys"
        actions={
          <>
            <label className="inline">
              Status{" "}
              <select value={status} onChange={(e) => setStatus(e.target.value as KeyStatus | "")}>
                <option value="">All</option>
                <option value="active">Active</option>
                <option value="revoked">Revoked</option>
              </select>
            </label>
            <button type="button" onClick={() => setCreating(true)}>
              Create key
            </button>
          </>
        }
      >
        {actionError && <ErrorState error={actionError} />}
        {error ? (
          <ErrorState error={error} onRetry={() => setReloadNonce((n) => n + 1)} />
        ) : loading && keys.length === 0 ? (
          <Loading />
        ) : (
          <>
            <SortableTable caption="API keys" columns={columns} rows={keys} rowKey={(k) => k.id} initialSort={{ key: "id", dir: "desc" }} empty="No API keys match this filter." />
            {nextAfter !== null && (
              <div className="load-more">
                <button type="button" className="ghost" disabled={loading} onClick={() => void loadMore()}>
                  {loading ? "Loading…" : "Load more"}
                </button>
              </div>
            )}
            <p className="muted">Sorting applies to the {keys.length} loaded keys.</p>
          </>
        )}
      </Panel>

      <Dialog open={creating} title="Create API key" onClose={() => setCreating(false)}>
        <CreateKeyForm
          client={client}
          onClose={(created) => {
            setCreating(false);
            if (created) setReloadNonce((n) => n + 1);
          }}
        />
      </Dialog>
    </>
  );
}

function CreateKeyForm({ client, onClose }: { client: AdminClient; onClose: (created: boolean) => void }) {
  const plans = useAsync((signal) => client.listPlans(signal), [client]);
  const [email, setEmail] = useState("");
  const [application, setApplication] = useState("");
  const [plan, setPlan] = useState("free");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [created, setCreated] = useState<CreatedApiKey | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      setCreated(await client.createApiKey({ email: email.trim(), application: application.trim(), plan }));
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  }

  if (created) return <ShowKeyOnce created={created} onDone={() => onClose(true)} />;

  return (
    <form onSubmit={submit} className="form">
      <label>
        Owner email
        <input type="email" required maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} placeholder="dev@example.com" autoComplete="off" />
        <FieldError error={error} field="email" />
      </label>
      <label>
        Application name
        <input required maxLength={100} value={application} onChange={(e) => setApplication(e.target.value)} placeholder="Web App" />
        <span className="hint">An existing application with this name and owner is reused.</span>
        <FieldError error={error} field="application" />
      </label>
      <label>
        Plan
        <select value={plan} onChange={(e) => setPlan(e.target.value)} disabled={!plans.data}>
          {(plans.data ?? [{ id: 0, name: "free" } as Plan]).map((p) => (
            <option key={p.id} value={p.name}>
              {p.name}
              {p.requests_per_minute ? ` — ${p.requests_per_minute}/min` : ""}
            </option>
          ))}
        </select>
        <span className="hint">Applies only when a new application is created.</span>
        <FieldError error={error} field="plan" />
      </label>
      <FormError error={error} />
      <div className="form-actions">
        <button type="button" className="ghost" onClick={() => onClose(false)}>
          Cancel
        </button>
        <button type="submit" disabled={saving}>
          {saving ? "Creating…" : "Create key"}
        </button>
      </div>
    </form>
  );
}

function ShowKeyOnce({ created, onDone }: { created: CreatedApiKey; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(created.api_key);
      setCopied(true);
    } catch {
      setCopied(false); // clipboard blocked: the key is selectable below
    }
  }
  return (
    <div className="form">
      <p className="warn-box" role="alert">
        ⚠ {created.note} Key {created.key.id} for “{created.key.application}” on the {created.key.plan} plan.
      </p>
      <label>
        API key
        <input readOnly value={created.api_key} onFocus={(e) => e.currentTarget.select()} className="mono" aria-describedby="key-help" />
      </label>
      <span id="key-help" className="hint">
        Clients send it as <code>Authorization: Bearer &lt;key&gt;</code>.
      </span>
      <div className="form-actions">
        <button type="button" className="ghost" onClick={() => void copy()}>
          {copied ? "Copied ✓" : "Copy"}
        </button>
        <button type="button" onClick={onDone}>
          I’ve stored it
        </button>
      </div>
    </div>
  );
}
