import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";

export interface AsyncState<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  reload: () => void;
}

/**
 * Runs fn whenever deps change (or reload() is called), aborting the previous
 * run. Previous data is kept while reloading so tables don't flash empty.
 */
export function useAsync<T>(fn: (signal: AbortSignal) => Promise<T>, deps: readonly unknown[]): AsyncState<T> {
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<Error | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setError(undefined);
    fnRef.current(ctrl.signal).then(
      (result) => {
        if (ctrl.signal.aborted) return;
        setData(result);
        setLoading(false);
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return;
        setError(err instanceof Error ? err : new Error(String(err)));
        setLoading(false);
      },
    );
    return () => ctrl.abort();
    // deps are the caller's; fn is read through a ref so inline lambdas don't loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  return { data, error, loading, reload };
}

/** Calls fn every ms milliseconds while the tab is visible. */
export function useInterval(fn: () => void, ms: number): void {
  const fnRef = useRef(fn);
  fnRef.current = fn;
  useEffect(() => {
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") fnRef.current();
    }, ms);
    return () => window.clearInterval(id);
  }, [ms]);
}

function subscribeHash(onChange: () => void): () => void {
  window.addEventListener("hashchange", onChange);
  return () => window.removeEventListener("hashchange", onChange);
}

/** The current "#/page" route, without the leading "#/". */
export function useHashRoute(): string {
  return useSyncExternalStore(subscribeHash, () => window.location.hash.replace(/^#\/?/, ""));
}

const darkQuery = "(prefers-color-scheme: dark)";

function subscribeScheme(onChange: () => void): () => void {
  const mql = window.matchMedia(darkQuery);
  mql.addEventListener("change", onChange);
  return () => mql.removeEventListener("change", onChange);
}

export function useDarkMode(): boolean {
  return useSyncExternalStore(subscribeScheme, () => window.matchMedia(darkQuery).matches);
}
