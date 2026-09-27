import { useCallback, useMemo, useState, type FormEvent } from "react";
import { AdminClient, ApiError } from "./api/client";
import { clearToken, loadToken, saveToken } from "./auth";
import { useHashRoute } from "./hooks";
import { Accounts } from "./pages/Accounts";
import { ApiKeys } from "./pages/ApiKeys";
import { Overview } from "./pages/Overview";
import { Plans } from "./pages/Plans";
import { Routes } from "./pages/Routes";

const PAGES = [
  { id: "overview", label: "Overview" },
  { id: "routes", label: "Routes" },
  { id: "api-keys", label: "API Keys" },
  { id: "plans", label: "Plans" },
  { id: "accounts", label: "Accounts" },
] as const;

type PageId = (typeof PAGES)[number]["id"];

const WINDOWS = [
  { value: "15m", label: "15 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "6h", label: "6 hours" },
  { value: "24h", label: "24 hours" },
  { value: "168h", label: "7 days" },
] as const;

export function App() {
  const [token, setToken] = useState<string | null>(loadToken);
  const [expired, setExpired] = useState(false);

  const signOut = useCallback((becauseRejected: boolean) => {
    clearToken();
    setToken(null);
    setExpired(becauseRejected);
  }, []);

  if (!token) {
    return (
      <SignIn
        expired={expired}
        onSignIn={(t) => {
          saveToken(t);
          setExpired(false);
          setToken(t);
        }}
      />
    );
  }
  return <Shell token={token} onSignOut={signOut} />;
}

function Shell({ token, onSignOut }: { token: string; onSignOut: (becauseRejected: boolean) => void }) {
  // A 401/403 from any call means the token was rotated or is wrong: back to sign-in.
  const client = useMemo(() => new AdminClient(token, () => onSignOut(true)), [token, onSignOut]);
  const route = useHashRoute();
  const page: PageId = PAGES.some((p) => p.id === route) ? (route as PageId) : "overview";
  const [window, setWindow] = useState<string>("1h");
  const title = PAGES.find((p) => p.id === page)?.label ?? "";
  const usesWindow = page === "overview" || page === "routes" || page === "accounts";

  return (
    <div className="app">
      <nav className="sidebar" aria-label="Main">
        <div className="brand">API Gateway</div>
        <ul>
          {PAGES.map((p) => (
            <li key={p.id}>
              <a href={`#/${p.id}`} aria-current={p.id === page ? "page" : undefined}>
                {p.label}
              </a>
            </li>
          ))}
        </ul>
        <button type="button" className="ghost signout" onClick={() => onSignOut(false)}>
          Sign out
        </button>
      </nav>

      <main className="content">
        <header className="page-head">
          <h1>{title}</h1>
          {usesWindow && (
            <label className="inline">
              Window{" "}
              <select value={window} onChange={(e) => setWindow(e.target.value)}>
                {WINDOWS.map((w) => (
                  <option key={w.value} value={w.value}>
                    Last {w.label}
                  </option>
                ))}
              </select>
            </label>
          )}
        </header>
        {page === "overview" && <Overview client={client} window={window} />}
        {page === "routes" && <Routes client={client} window={window} />}
        {page === "api-keys" && <ApiKeys client={client} />}
        {page === "plans" && <Plans client={client} />}
        {page === "accounts" && <Accounts client={client} window={window} />}
      </main>
    </div>
  );
}

function SignIn({ expired, onSignIn }: { expired: boolean; onSignIn: (token: string) => void }) {
  const [value, setValue] = useState("");
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(expired ? "Your admin token was rejected. Sign in again." : null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const candidate = value.trim();
    setChecking(true);
    setError(null);
    try {
      await new AdminClient(candidate).listPlans(); // cheapest authenticated call
      onSignIn(candidate);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setError("Enter the admin token.");
      else if (err instanceof ApiError && err.status === 403) setError("That admin token is not valid.");
      else if (err instanceof ApiError && err.status === 404)
        setError("The gateway has no admin API. Start it with ADMIN_TOKEN set (at least 32 characters).");
      else setError(err instanceof Error ? err.message : String(err));
      setChecking(false);
    }
  }

  return (
    <main className="signin">
      <form onSubmit={submit} className="panel form" aria-labelledby="signin-title">
        <h1 id="signin-title">API Gateway</h1>
        <p className="muted">Sign in with the gateway’s ADMIN_TOKEN. It is kept only for this browser tab.</p>
        <label>
          Admin token
          <input type="password" required autoFocus autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} />
        </label>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" disabled={checking}>
          {checking ? "Checking…" : "Sign in"}
        </button>
      </form>
    </main>
  );
}
