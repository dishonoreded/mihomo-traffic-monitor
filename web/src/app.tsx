import { useEffect, useState } from "react";

type Route = "/" | "/analyze" | "/status";

interface StatusResponse {
  apiVersion: string;
  collector: {
    state: "unavailable" | "connecting" | "connected";
    reason: string;
    message: string;
    lastSample: string | null;
  };
  live: {
    uploadBytesPerSecond: number;
    downloadBytesPerSecond: number;
    activeConnections: number;
  };
  database: {
    healthy: boolean;
    sizeBytes: number;
    schemaVersion: number;
    journalMode: string;
    error: string | null;
  };
  configuration: {
    controllerUrl: string;
    controllerAuthentication: string;
    dashboardAddress: string;
    sampleInterval: string;
    databasePath: string;
  };
}

const routes: Array<{ path: Route; label: string }> = [
  { path: "/", label: "Overview" },
  { path: "/analyze", label: "Analyze" },
  { path: "/status", label: "Status" },
];

export function App() {
  const [route, setRoute] = useState<Route>(normalizeRoute(window.location.pathname));
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [requestError, setRequestError] = useState("");

  useEffect(() => {
    const abort = new AbortController();
    fetch("/api/v1/status", { signal: abort.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Status request failed (${response.status})`);
        return (await response.json()) as StatusResponse;
      })
      .then(setStatus)
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === "AbortError") return;
        setRequestError(error instanceof Error ? error.message : "Status is unavailable");
      });
    return () => abort.abort();
  }, []);

  useEffect(() => {
    const updateRoute = () => setRoute(normalizeRoute(window.location.pathname));
    window.addEventListener("popstate", updateRoute);
    return () => window.removeEventListener("popstate", updateRoute);
  }, []);

  const navigate = (path: Route) => {
    window.history.pushState({}, "", path);
    setRoute(path);
  };

  return (
    <div className="observatory-shell">
      <header className="masthead">
        <a className="brand" href="/" onClick={(event) => { event.preventDefault(); navigate("/"); }}>
          <span className="brand-mark" aria-hidden="true">M</span>
          <span><strong>mihomo</strong><small>traffic monitor</small></span>
        </a>
        <p className="locality"><span aria-hidden="true" /> private loopback observatory</p>
      </header>

      <section className="signal-rail" aria-label="Local observatory signal path">
        <Signal label="Controller" value={status?.collector.state === "connected" ? "Connected" : "Unavailable"} tone="unknown" />
        <span className="rail-line" aria-hidden="true" />
        <Signal label="Sample" value={status?.configuration.sampleInterval ?? "1s"} />
        <span className="rail-line" aria-hidden="true" />
        <Signal label="Database" value={status?.database.healthy ? "Ready" : "Checking"} tone={status?.database.healthy ? "download" : "unknown"} />
        <span className="rail-line" aria-hidden="true" />
        <Signal label="Panel" value={status?.configuration.dashboardAddress ?? "127.0.0.1:9091"} />
      </section>

      <nav className="primary-navigation" aria-label="Primary">
        {routes.map((item) => (
          <a
            key={item.path}
            href={item.path}
            aria-current={route === item.path ? "page" : undefined}
            onClick={(event) => { event.preventDefault(); navigate(item.path); }}
          >
            {item.label}
          </a>
        ))}
      </nav>

      {requestError ? <p className="request-error" role="alert">{requestError}. Check that the local process is still running.</p> : null}
      <main>{route === "/" ? <Overview status={status} /> : route === "/analyze" ? <Analyze /> : <Status status={status} />}</main>

      <footer>
        <span>Local only</span>
        <span>No telemetry</span>
        <span>Minute history begins after first connection</span>
      </footer>
    </div>
  );
}

function Signal({ label, value, tone = "neutral" }: { label: string; value: string; tone?: "neutral" | "download" | "unknown" }) {
  return <div className={`signal signal-${tone}`}><span>{label}</span><strong>{value}</strong></div>;
}

function Overview({ status }: { status: StatusResponse | null }) {
  return (
    <div className="page-grid overview-page">
      <section className="hero-panel">
        <div className="eyebrow">Live traffic / awaiting signal</div>
        <div className="trace-field" aria-label="Live traffic trace is waiting for Controller data">
          <span className="trace-axis" />
          <div className="trace-message"><strong>Signal pending</strong><span>Connect Mihomo to begin a live trace.</span></div>
          <span className="trace-label upload">Upload</span>
          <span className="trace-label download">Download</span>
        </div>
      </section>
      <aside className="readout-panel">
        <p className="eyebrow">Collector state</p>
        <h1>Controller unavailable</h1>
        <p>{status?.collector.message ?? "The local observatory is ready. Waiting for its first status reading."}</p>
        <dl className="readouts">
          <div><dt>Active connections</dt><dd>{status?.live.activeConnections ?? 0}</dd></div>
          <div><dt>Database</dt><dd>{status?.database.healthy ? "Database ready" : "Checking"}</dd></div>
          <div><dt>Stored history</dt><dd>None yet</dd></div>
        </dl>
      </aside>
      <section className="empty-strip">
        <span className="empty-index">00:00</span>
        <div><h2>Collection starts from a clean baseline</h2><p>Traffic from before the first Controller connection will not be imported.</p></div>
      </section>
    </div>
  );
}

function Analyze() {
  return (
    <section className="empty-page">
      <p className="eyebrow">Historical analysis</p>
      <h1>No traffic history yet</h1>
      <p>App, Host, and Registrable domain trends will appear after the collector stores its first complete minute.</p>
      <div className="empty-ruler" aria-hidden="true"><span /><span /><span /><span /><span /></div>
    </section>
  );
}

function Status({ status }: { status: StatusResponse | null }) {
  return (
    <section className="status-page">
      <div><p className="eyebrow">System diagnostics</p><h1>Local observatory</h1><p>Only non-sensitive configuration is shown.</p></div>
      <dl className="diagnostic-grid">
        <Diagnostic label="Controller" value={status?.configuration.controllerUrl ?? "Checking"} detail={status?.collector.message ?? "Reading local status"} />
        <Diagnostic label="Database" value={status?.database.healthy ? "Database ready" : "Checking"} detail={status ? `${status.database.journalMode.toUpperCase()} · schema ${status.database.schemaVersion}` : "Opening private storage"} />
        <Diagnostic label="Database size" value={formatBytes(status?.database.sizeBytes ?? 0)} detail="Permanent minute history" />
        <Diagnostic label="Database location" value={status?.configuration.databasePath ?? "Checking"} detail="Private local storage · permanent retention" />
        <Diagnostic label="Authentication" value={status?.configuration.controllerAuthentication === "configured" ? "Configured" : "Not configured"} detail="Secret values are never displayed" />
      </dl>
    </section>
  );
}

function Diagnostic({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <div className="diagnostic"><dt>{label}</dt><dd>{value}</dd><p>{detail}</p></div>;
}

function normalizeRoute(path: string): Route {
  return path === "/analyze" || path === "/status" ? path : "/";
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KB`;
}
