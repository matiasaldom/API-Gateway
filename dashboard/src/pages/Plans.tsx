import { useState, type FormEvent } from "react";
import type { AdminClient } from "../api/client";
import type { Plan } from "../api/types";
import { SortableTable, type Column } from "../components/SortableTable";
import { Async, FieldError, FormError, Panel } from "../components/ui";
import { fmtDate, fmtInt } from "../format";
import { useAsync } from "../hooks";

const MAX_LIMIT = 1_000_000;

export function Plans({ client }: { client: AdminClient }) {
  const plans = useAsync((signal) => client.listPlans(signal), [client]);
  const [editing, setEditing] = useState<number | null>(null);

  const columns: Column<Plan>[] = [
    { key: "name", header: "Plan", sortValue: (p) => p.name, render: (p) => <strong>{p.name}</strong> },
    {
      key: "limit",
      header: "Requests / minute",
      align: "right",
      sortValue: (p) => p.requests_per_minute,
      render: (p) =>
        editing === p.id ? (
          <LimitEditor
            client={client}
            plan={p}
            onDone={(changed) => {
              setEditing(null);
              if (changed) plans.reload();
            }}
          />
        ) : (
          fmtInt(p.requests_per_minute)
        ),
    },
    { key: "apps", header: "Applications", align: "right", sortValue: (p) => p.applications, render: (p) => fmtInt(p.applications) },
    { key: "created", header: "Created", sortValue: (p) => p.created_at, render: (p) => fmtDate(p.created_at) },
    {
      key: "actions",
      header: "",
      render: (p) =>
        editing === p.id ? null : (
          <button type="button" className="small" onClick={() => setEditing(p.id)}>
            Edit limit
          </button>
        ),
    },
  ];

  return (
    <Panel title="Plans" actions={<span className="muted">Limit changes apply to each key’s next request, on every gateway instance.</span>}>
      <Async state={plans}>
        {(list) => <SortableTable caption="Plans" columns={columns} rows={list} rowKey={(p) => p.id} initialSort={{ key: "limit", dir: "asc" }} empty="No plans." />}
      </Async>
    </Panel>
  );
}

function LimitEditor({ client, plan, onDone }: { client: AdminClient; plan: Plan; onDone: (changed: boolean) => void }) {
  const [value, setValue] = useState(String(plan.requests_per_minute));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const n = Number(value);
    if (!Number.isInteger(n) || n < 1 || n > MAX_LIMIT) {
      setError(new Error(`Enter a whole number from 1 to ${fmtInt(MAX_LIMIT)}.`));
      return;
    }
    if (n === plan.requests_per_minute) return onDone(false);
    setSaving(true);
    setError(null);
    try {
      await client.updatePlan(plan.id, n);
      onDone(true);
    } catch (err) {
      setError(err);
      setSaving(false);
    }
  }

  return (
    <form onSubmit={submit} className="inline-form" onKeyDown={(e) => e.key === "Escape" && onDone(false)}>
      <input
        type="number"
        min={1}
        max={MAX_LIMIT}
        step={1}
        required
        value={value}
        onChange={(e) => setValue(e.target.value)}
        aria-label={`Requests per minute for ${plan.name}`}
        autoFocus
      />
      <button type="submit" className="small" disabled={saving}>
        {saving ? "Saving…" : "Save"}
      </button>
      <button type="button" className="small ghost" onClick={() => onDone(false)}>
        Cancel
      </button>
      <FieldError error={error} field="requests_per_minute" />
      <FormError error={error} />
    </form>
  );
}
