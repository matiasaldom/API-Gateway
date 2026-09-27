import { useEffect, useRef, type ReactNode } from "react";
import { ApiError } from "../api/client";

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="loading" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      {label}
    </div>
  );
}

/** Error panel with the gateway's request ID (for log lookup) and a retry button. */
export function ErrorState({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const requestId = error instanceof ApiError ? error.requestId : undefined;
  return (
    <div className="error-state" role="alert">
      <strong>Something went wrong.</strong>
      <p>{error.message}</p>
      {requestId && (
        <p className="muted">
          Request ID: <code>{requestId}</code>
        </p>
      )}
      {onRetry && (
        <button type="button" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  );
}

/**
 * Loading/error/data switch for one async resource. Keeps showing stale data
 * while a reload is in flight, with a small refreshing hint.
 */
export function Async<T>({
  state,
  children,
}: {
  state: { data: T | undefined; error: Error | undefined; loading: boolean; reload: () => void };
  children: (data: T) => ReactNode;
}) {
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  if (state.data === undefined) return <Loading />;
  return (
    <div aria-busy={state.loading} className={state.loading ? "stack refreshing" : "stack"}>
      {children(state.data)}
    </div>
  );
}

export function StatCard({ label, value, hint, tone }: { label: string; value: string; hint?: string; tone?: "warn" }) {
  return (
    <div className={tone === "warn" ? "stat warn" : "stat"}>
      <div className="stat-label">{label}</div>
      <div className="stat-value">
        {tone === "warn" && <span aria-label="Warning">⚠ </span>}
        {value}
      </div>
      {hint && <div className="stat-hint">{hint}</div>}
    </div>
  );
}

export function Panel({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="panel" aria-label={title}>
      <header className="panel-head">
        <h2>{title}</h2>
        {actions && <div className="panel-actions">{actions}</div>}
      </header>
      {children}
    </section>
  );
}

/** Modal built on the native <dialog>, which provides focus trapping and Escape to close. */
export function Dialog({ open, title, onClose, children }: { open: boolean; title: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog ref={ref} onClose={onClose} aria-labelledby="dialog-title">
      <header className="dialog-head">
        <h2 id="dialog-title">{title}</h2>
        <button type="button" className="ghost" aria-label="Close" onClick={onClose}>
          ✕
        </button>
      </header>
      {open && children}
    </dialog>
  );
}

/** Shows a field's validation message from a gateway 400, if any. */
export function FieldError({ error, field }: { error: unknown; field: string }) {
  const msg = error instanceof ApiError ? error.fields[field] : undefined;
  return msg ? (
    <span className="field-error" role="alert">
      {msg}
    </span>
  ) : null;
}

/** A form-level error: the message, unless every problem is already shown on a field. */
export function FormError({ error }: { error: unknown }) {
  if (!error) return null;
  if (error instanceof ApiError && Object.keys(error.fields).length > 0) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <p className="form-error" role="alert">
      {msg}
    </p>
  );
}
