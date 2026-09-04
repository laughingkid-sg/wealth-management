import { type FormEvent, useEffect, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import { Eye, EyeOff } from "lucide-react";
import { isSupabaseConfigured, supabase } from "./lib/supabase";
import { AccountsPage } from "./features/accounts/AccountsPage";
import { workspacePageFromLocation, type WorkspacePage } from "./workspace";
import "./App.css";

function LoginPage({ onSignedIn }: { onSignedIn: (session: Session) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    setIsSubmitting(true);
    const { data, error: authError } = await supabase.auth.signInWithPassword({
      email,
      password,
    });
    setIsSubmitting(false);
    if (authError) setError(authError.message);
    else if (data.session) onSignedIn(data.session);
  }
  return (
    <main className="auth-shell">
      <section className="auth-card" aria-labelledby="sign-in-title">
        <p className="eyebrow">WEALTH BUILDER</p>
        <h1 id="sign-in-title">Welcome back</h1>
        <p className="muted">
          Sign in to manage your private finances.
        </p>
        <form onSubmit={submit} className="form-stack">
          <label>
            Email
            <input
              autoComplete="email"
              type="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              required
            />
          </label>
          <label>
            Password
            <span className="password-field">
              <input
                autoComplete="current-password"
                type={showPassword ? "text" : "password"}
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
              />
              <button
                className="password-visibility"
                type="button"
                onClick={() => setShowPassword((visible) => !visible)}
                aria-label={showPassword ? "Hide password" : "Show password"}
                aria-pressed={showPassword}
              >
                {showPassword ? (
                  <EyeOff size={18} aria-hidden="true" />
                ) : (
                  <Eye size={18} aria-hidden="true" />
                )}
              </button>
            </span>
          </label>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <button
            className="button button-primary"
            disabled={isSubmitting}
            type="submit"
          >
            {isSubmitting ? "Signing in…" : "Sign in"}
          </button>
        </form>
        <p className="helper-text">
          Accounts are provisioned by the product owner. Registration is not
          available here.
        </p>
      </section>
    </main>
  );
}

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);
  const [activePage, setActivePage] = useState<WorkspacePage>(workspacePageFromLocation);
  useEffect(() => {
    void supabase.auth.getSession().then(({ data }) => {
      setSession(data.session);
      setLoading(false);
    });
    const { data: listener } = supabase.auth.onAuthStateChange(
      (_event, nextSession) => setSession(nextSession),
    );
    return () => listener.subscription.unsubscribe();
  }, []);
  useEffect(() => {
    const onPopState = () => setActivePage(workspacePageFromLocation());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  const navigate = (page: WorkspacePage) => {
    setActivePage(page);
    const url = new URL(window.location.href);
    url.searchParams.set("page", page);
    url.searchParams.delete("gmail");
    window.history.pushState({}, "", url);
  };
  if (!isSupabaseConfigured)
    return (
      <main className="auth-shell">
        <section className="auth-card">
          <p className="eyebrow">SETUP REQUIRED</p>
          <h1>Connect Supabase</h1>
          <p className="muted">
            Copy <code>.env.example</code> to <code>.env.local</code>, then
            provide the project URL and publishable key. No secret or service
            role key belongs in this frontend.
          </p>
        </section>
      </main>
    );
  if (loading)
    return (
      <main className="auth-shell">
        <p className="muted">Loading your secure session…</p>
      </main>
    );
  return session ? (
    <AccountsPage
      activePage={activePage}
      onNavigate={navigate}
      session={session}
      signOut={() => supabase.auth.signOut().then(() => undefined)}
    />
  ) : (
    <LoginPage onSignedIn={setSession} />
  );
}
