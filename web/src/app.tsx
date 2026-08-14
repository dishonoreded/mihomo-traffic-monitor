import { useEffect, useState } from "react";

type Route = "/" | "/analyze" | "/status";

interface StatusResponse {
  apiVersion: string;
  timestamp: string;
  collector: {
    state: "unavailable" | "connecting" | "connected";
    reason: string;
    message: string;
    controllerVersion: string | null;
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

interface RatePoint {
  upload: number;
  download: number;
}

interface AttributionTotals {
  observed: number;
  residual: number;
  gapRecovered: number;
  total: number;
}

interface Leader {
  name: string;
  upload: number;
  download: number;
  total: number;
}

interface SummaryResponse {
  apiVersion: string;
  range: { start: string; end: string };
  upload: AttributionTotals;
  download: AttributionTotals;
  total: AttributionTotals;
  coverage: number;
  leaders: { apps: Leader[]; hosts: Leader[] };
}

const routes: Array<{ path: Route; label: string }> = [
  { path: "/", label: "Overview" },
  { path: "/analyze", label: "Analyze" },
  { path: "/status", label: "Status" },
];

export function App() {
  const [route, setRoute] = useState<Route>(normalizeRoute(window.location.pathname));
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [rateHistory, setRateHistory] = useState<RatePoint[]>([]);
  const [requestError, setRequestError] = useState("");
  const [summary, setSummary] = useState<SummaryResponse | null>(null);
  const [summaryError, setSummaryError] = useState("");

  useEffect(() => {
    const abort = new AbortController();
    let receivedLiveEvent = false;
    const acceptStatus = (nextStatus: StatusResponse) => {
      setStatus(nextStatus);
      setRateHistory((current) => [
        ...current,
        {
          upload: nextStatus.live.uploadBytesPerSecond,
          download: nextStatus.live.downloadBytesPerSecond,
        },
      ].slice(-36));
      setRequestError("");
    };
    fetch("/api/v1/status", { signal: abort.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Status request failed (${response.status})`);
        return (await response.json()) as StatusResponse;
      })
      .then((initialStatus) => {
        if (!receivedLiveEvent) acceptStatus(initialStatus);
      })
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === "AbortError") return;
        setRequestError(error instanceof Error ? error.message : "Status is unavailable");
      });

    let events: EventSource | null = null;
    if (typeof EventSource !== "undefined") {
      events = new EventSource("/api/v1/live/events");
      events.addEventListener("status", (event) => {
        receivedLiveEvent = true;
        try {
          acceptStatus(JSON.parse((event as MessageEvent<string>).data) as StatusResponse);
        } catch {
          setRequestError("Live status contained an invalid response");
        }
      });
    }
    return () => {
      abort.abort();
      events?.close();
    };
  }, []);

  useEffect(() => {
    const abort = new AbortController();
    const loadSummary = () => {
      fetch(todaySummaryURL(), { signal: abort.signal })
        .then(async (response) => {
          if (!response.ok) throw new Error(`History request failed (${response.status})`);
          return (await response.json()) as SummaryResponse;
        })
        .then((nextSummary) => {
          setSummary(nextSummary);
          setSummaryError("");
        })
        .catch((error: unknown) => {
          if (error instanceof DOMException && error.name === "AbortError") return;
          setSummaryError(error instanceof Error ? error.message : "Today's history is unavailable");
        });
    };
    loadSummary();
    const refresh = window.setInterval(loadSummary, 15_000);
    return () => {
      window.clearInterval(refresh);
      abort.abort();
    };
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
        <Signal
          label="Controller"
          value={collectorLabel(status?.collector.state)}
          tone={status?.collector.state === "connected" ? "download" : "unknown"}
        />
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
      <main>{route === "/" ? <Overview status={status} history={rateHistory} summary={summary} summaryError={summaryError} /> : route === "/analyze" ? <Analyze /> : <Status status={status} />}</main>

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

function Overview({ status, history, summary, summaryError }: { status: StatusResponse | null; history: RatePoint[]; summary: SummaryResponse | null; summaryError: string }) {
  const connected = status?.collector.state === "connected";
  const heading = connected ? "Traffic is live" : status?.collector.state === "connecting" ? "Connecting" : "Controller unavailable";
  const upload = status?.live.uploadBytesPerSecond ?? 0;
  const download = status?.live.downloadBytesPerSecond ?? 0;
  return (
    <div className="page-grid overview-page">
      <section className="hero-panel">
        <div className="eyebrow">Live traffic / {connected ? "streaming" : "awaiting signal"}</div>
        <div className="trace-field">
          <span className="trace-axis" />
          {connected ? <TrafficTrace history={history} upload={upload} download={download} /> : (
            <div className="trace-message" role="status"><strong>Signal pending</strong><span>Connect Mihomo to begin a live trace.</span></div>
          )}
          <span className="trace-label upload">Upload</span>
          <span className="trace-label download">Download</span>
        </div>
      </section>
      <aside className="readout-panel">
        <p className="eyebrow">Collector state</p>
        <h1>{heading}</h1>
        <p>{status?.collector.message ?? "The local observatory is ready. Waiting for its first status reading."}</p>
        <dl className="readouts">
          <div className="readout-upload"><dt>Upload now</dt><dd>{formatRate(upload)}</dd></div>
          <div className="readout-download"><dt>Download now</dt><dd>{formatRate(download)}</dd></div>
          <div><dt>Active connections</dt><dd>{status?.live.activeConnections ?? 0}</dd></div>
          <div><dt>Database</dt><dd>{status?.database.healthy ? "Database ready" : "Checking"}</dd></div>
        </dl>
      </aside>
      <section className="history-band" aria-labelledby="today-heading">
        <div className="today-summary">
          <div className="history-heading">
            <p className="eyebrow">Permanent minute history</p>
            <h2 id="today-heading">Today</h2>
          </div>
          {summaryError ? <p className="history-error" role="status">{summaryError}</p> : (
            <dl className="daily-totals">
              <div><dt>Total</dt><dd>{formatBytes(summary?.total.total ?? 0)}</dd></div>
              <div className="daily-upload"><dt>Upload</dt><dd>{formatBytes(summary?.upload.total ?? 0)}</dd></div>
              <div className="daily-download"><dt>Download</dt><dd>{formatBytes(summary?.download.total ?? 0)}</dd></div>
              <div className="daily-coverage"><dt>Coverage</dt><dd>{formatCoverage(summary?.coverage ?? 0)}</dd></div>
            </dl>
          )}
        </div>
        <div className="leader-columns">
          <LeaderList title="Apps" leaders={summary?.leaders.apps ?? []} />
          <LeaderList title="Hosts" leaders={summary?.leaders.hosts ?? []} />
        </div>
      </section>
    </div>
  );
}

function LeaderList({ title, leaders }: { title: string; leaders: Leader[] }) {
  return (
    <section className="leader-list" aria-label={`${title} leaders`}>
      <h3>{title}</h3>
      {leaders.length === 0 ? <p>No observed traffic yet</p> : (
        <ol>
          {leaders.map((leader) => (
            <li key={leader.name}>
              <span className="leader-rank" aria-hidden="true">{String(leaders.indexOf(leader) + 1).padStart(2, "0")}</span>
              <strong>{leader.name}</strong>
              <span>{formatBytes(leader.total)}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function TrafficTrace({ history, upload, download }: { history: RatePoint[]; upload: number; download: number }) {
  const points = history.length > 0 ? history : [{ upload, download }];
  const peak = Math.max(1, ...points.flatMap((point) => [point.upload, point.download]));
  const x = (index: number) => points.length === 1 ? 360 : (index / (points.length - 1)) * 720;
  const uploadPoints = points.map((point, index) => `${x(index)},${150 - (point.upload / peak) * 112}`).join(" ");
  const downloadPoints = points.map((point, index) => `${x(index)},${150 + (point.download / peak) * 112}`).join(" ");
  const label = `Upload ${formatRate(upload)} above baseline; download ${formatRate(download)} below baseline`;
  return (
    <svg className="traffic-trace" viewBox="0 0 720 300" preserveAspectRatio="none" role="img" aria-label={label}>
      <polyline className="trace-line trace-upload" points={uploadPoints} />
      <polyline className="trace-line trace-download" points={downloadPoints} />
    </svg>
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
        <Diagnostic label="Controller" value={status?.configuration.controllerUrl ?? "Checking"} detail={controllerDetail(status)} />
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

function collectorLabel(state: StatusResponse["collector"]["state"] | undefined): string {
  if (state === "connected") return "Connected";
  if (state === "connecting") return "Connecting";
  return "Unavailable";
}

function controllerDetail(status: StatusResponse | null): string {
  if (!status) return "Reading local status";
  if (status.collector.controllerVersion) return `${status.collector.message} · Mihomo ${status.collector.controllerVersion}`;
  return status.collector.message;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`;
}

function formatCoverage(coverage: number): string {
  return `${(coverage * 100).toFixed(1)}%`;
}

function todaySummaryURL(): string {
  const end = new Date();
  const start = new Date(end.getFullYear(), end.getMonth(), end.getDate());
  const query = new URLSearchParams({ start: start.toISOString(), end: end.toISOString() });
  return `/api/v1/summary?${query.toString()}`;
}
