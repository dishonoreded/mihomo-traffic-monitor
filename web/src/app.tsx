import { useEffect, useRef, useState } from "react";

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
  collection: {
    currentGap: CollectionGap | null;
    recentGaps: CollectionGap[];
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

interface CollectionGap {
  id: number;
  startedAt: string;
  endedAt: string | null;
  open: boolean;
  reason: string;
  disposition: "open" | "recovered" | "no_growth" | "reset";
  recoveredUpload: number;
  recoveredDownload: number;
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
  scope: "all" | "observed";
  range: { start: string; end: string };
  upload: AttributionTotals;
  download: AttributionTotals;
  total: AttributionTotals;
  coverage: number;
  leaders: { apps: Leader[]; hosts: Leader[] };
}

type Direction = "upload" | "download" | "total";
type Granularity = "minute" | "hour" | "day" | "auto";

interface SeriesPoint {
  start: string;
  upload: AttributionTotals;
  download: AttributionTotals;
  total: AttributionTotals;
}

interface SeriesResponse {
  apiVersion: string;
  scope: "all" | "observed";
  granularity: Exclude<Granularity, "auto">;
  pointLimit: number;
  timeZone: string;
  range: { from: string; to: string };
  points: SeriesPoint[];
}

interface AnalyzeQuery {
  from: string;
  to: string;
  timeZone: string;
  direction: Direction;
  granularity: Granularity;
  apps: string[];
  hosts: string[];
  domains: string[];
}

type FilterDimension = "app" | "host" | "domain";

interface DimensionsResponse {
  apiVersion: string;
  query: string;
  limit: number;
  apps: string[];
  hosts: string[];
  domains: string[];
}

interface RankingsResponse {
  apiVersion: string;
  scope: "observed";
  range: { from: string; to: string };
  dimension: FilterDimension;
  direction: Direction;
  limit: number;
  items: Leader[];
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
    if (path === route) return;
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
  const [query, setQuery] = useState<AnalyzeQuery>(() => readAnalyzeQuery());
  const [draftFrom, setDraftFrom] = useState(() => toDateTimeInZone(query.from, query.timeZone));
  const [draftTo, setDraftTo] = useState(() => toDateTimeInZone(query.to, query.timeZone));
  const [series, setSeries] = useState<SeriesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [formError, setFormError] = useState("");
  const [dimensions, setDimensions] = useState<DimensionsResponse>({ apiVersion: "v1", query: "", limit: 100, apps: [], hosts: [], domains: [] });
  const [dimensionsError, setDimensionsError] = useState("");
  const [rankings, setRankings] = useState<Record<FilterDimension, RankingsResponse | null>>({ app: null, host: null, domain: null });
  const [rankingsLoading, setRankingsLoading] = useState(true);
  const [rankingsError, setRankingsError] = useState("");
  const [filterDrafts, setFilterDrafts] = useState<Record<FilterDimension, string>>({ app: "", host: "", domain: "" });
  const [focusAfterDrill, setFocusAfterDrill] = useState(false);
  const trendHeading = useRef<HTMLHeadingElement>(null);

  const commitQuery = (next: AnalyzeQuery, mode: "push" | "replace" = "push") => {
    const url = `/analyze?${serializeAnalyzeQuery(next).toString()}`;
    window.history[mode === "push" ? "pushState" : "replaceState"]({}, "", url);
    setQuery(next);
  };

  useEffect(() => {
    commitQuery(query, "replace");
  }, []);

  useEffect(() => {
    const restore = () => setQuery(readAnalyzeQuery());
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);

  useEffect(() => {
    setDraftFrom(toDateTimeInZone(query.from, query.timeZone));
    setDraftTo(toDateTimeInZone(query.to, query.timeZone));
  }, [query.from, query.to, query.timeZone]);

  useEffect(() => {
    const abort = new AbortController();
    fetch("/api/v1/dimensions?limit=100", { signal: abort.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Dimensions request failed (${response.status})`);
        return (await response.json()) as DimensionsResponse;
      })
      .then((result) => {
        setDimensions(result);
        setDimensionsError("");
      })
      .catch((reason: unknown) => {
        if (!(reason instanceof DOMException && reason.name === "AbortError")) setDimensionsError(reason instanceof Error ? reason.message : "Traffic dimensions are unavailable");
      });
    return () => abort.abort();
  }, []);

  useEffect(() => {
    const abort = new AbortController();
    const apiQuery = new URLSearchParams({
      from: query.from,
      to: query.to,
      timeZone: query.timeZone,
      granularity: query.granularity,
    });
    appendTrafficFilters(apiQuery, query);
    setLoading(true);
    setError("");
    fetch(`/api/v1/series?${apiQuery.toString()}`, { signal: abort.signal })
      .then(async (response) => {
        if (response.ok) return (await response.json()) as SeriesResponse;
        const payload = await response.json().catch(() => null) as { error?: { message?: string } } | null;
        throw new Error(payload?.error?.message ?? `History request failed (${response.status})`);
      })
      .then((nextSeries) => setSeries(nextSeries))
      .catch((reason: unknown) => {
        if (reason instanceof DOMException && reason.name === "AbortError") return;
        setSeries(null);
        setError(reason instanceof Error ? reason.message : "Traffic history is unavailable");
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
  }, [query.from, query.to, query.timeZone, query.granularity, query.apps, query.hosts, query.domains]);

  useEffect(() => {
    const abort = new AbortController();
    const base = new URLSearchParams({ from: query.from, to: query.to, direction: query.direction, limit: "10" });
    appendTrafficFilters(base, query);
    setRankings({ app: null, host: null, domain: null });
    setRankingsLoading(true);
    setRankingsError("");
    Promise.all((["app", "host", "domain"] as const).map(async (dimension) => {
      const parameters = new URLSearchParams(base);
      parameters.set("dimension", dimension);
      const response = await fetch(`/api/v1/rankings?${parameters.toString()}`, { signal: abort.signal });
      if (!response.ok) throw new Error(`Rankings request failed (${response.status})`);
      return (await response.json()) as RankingsResponse;
    }))
      .then((results) => setRankings({ app: results[0], host: results[1], domain: results[2] }))
      .catch((reason: unknown) => {
        if (!(reason instanceof DOMException && reason.name === "AbortError")) setRankingsError(reason instanceof Error ? reason.message : "Traffic rankings are unavailable");
      })
      .finally(() => {
        if (!abort.signal.aborted) setRankingsLoading(false);
      });
    return () => abort.abort();
  }, [query.from, query.to, query.direction, query.apps, query.hosts, query.domains]);

  useEffect(() => {
    if (!loading && focusAfterDrill && series) {
      trendHeading.current?.focus();
      setFocusAfterDrill(false);
    }
  }, [focusAfterDrill, loading, series]);

  const applyRange = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const from = draftFrom === toDateTimeInZone(query.from, query.timeZone) ? new Date(query.from) : zonedDateTimeToInstant(draftFrom, query.timeZone);
    const to = draftTo === toDateTimeInZone(query.to, query.timeZone) ? new Date(query.to) : zonedDateTimeToInstant(draftTo, query.timeZone);
    if (from === null || to === null || to <= from) {
      setFormError("Choose a valid range with To later than From.");
      return;
    }
    setFormError("");
    commitQuery({ ...query, from: from.toISOString(), to: to.toISOString() });
  };

  const setDirection = (direction: Direction) => commitQuery({ ...query, direction });
  const setGranularity = (granularity: Granularity) => commitQuery({ ...query, granularity });
  const addFilter = (dimension: FilterDimension, value = filterDrafts[dimension]) => {
    const exact = value.trim();
    if (!exact) return;
    const key = analyzeFilterKey(dimension);
    if (query[key].includes(exact)) return;
    commitQuery({ ...query, [key]: [...query[key], exact] });
    setFilterDrafts((current) => ({ ...current, [dimension]: "" }));
  };
  const removeFilter = (dimension: FilterDimension, value: string) => {
    const key = analyzeFilterKey(dimension);
    commitQuery({ ...query, [key]: query[key].filter((item) => item !== value) });
  };
  const drillDown = (dimension: FilterDimension, value: string) => {
    const key = analyzeFilterKey(dimension);
    if (query[key].includes(value)) return;
    setLoading(true);
    setFocusAfterDrill(true);
    commitQuery({ ...query, [key]: [...query[key], value] });
  };

  return (
    <section className="analyze-page">
      <header className="analyze-heading">
        <div>
          <p className="eyebrow">Permanent minute history</p>
          <h1>Traffic history</h1>
        </div>
        <p>{query.timeZone}<span>{series ? `${series.granularity} buckets / ${series.points.length} points` : "Local calendar buckets"}</span></p>
      </header>

      <form className="analyze-controls" onSubmit={applyRange}>
        <label htmlFor="analyze-from">From<input id="analyze-from" name="from" type="datetime-local" value={draftFrom} onChange={(event) => setDraftFrom(event.target.value)} /></label>
        <label htmlFor="analyze-to">To<input id="analyze-to" name="to" type="datetime-local" value={draftTo} onChange={(event) => setDraftTo(event.target.value)} /></label>
        <label htmlFor="analyze-granularity">Granularity
          <select id="analyze-granularity" name="granularity" value={query.granularity} onChange={(event) => setGranularity(event.target.value as Granularity)}>
            <option value="auto">Auto / max 400</option>
            <option value="minute">Minute</option>
            <option value="hour">Hour</option>
            <option value="day">Day</option>
          </select>
        </label>
        <fieldset className="direction-control">
          <legend>Direction</legend>
          {(["upload", "download", "total"] as const).map((direction) => (
            <label key={direction} className={`direction-${direction}`}>
              <input type="radio" name="direction" value={direction} checked={query.direction === direction} onChange={() => setDirection(direction)} />
              <span>{direction[0].toUpperCase() + direction.slice(1)}</span>
            </label>
          ))}
        </fieldset>
        <button type="submit">Apply range</button>
        {formError ? <p className="control-error" role="alert">{formError}</p> : null}
      </form>

      <TrafficFilters
        query={query}
        dimensions={dimensions}
        error={dimensionsError}
        drafts={filterDrafts}
        setDraft={(dimension, value) => setFilterDrafts((current) => ({ ...current, [dimension]: value }))}
        add={addFilter}
        remove={removeFilter}
      />

      {hasTrafficFilters(query) ? <p className="observed-scope">Filtered results contain matching Observed traffic only.</p> : null}

      <div className="analysis-stage">
        {loading ? <AnalysisState heading="Reading traffic history" detail="Querying the permanent local minute store." /> : error ? <AnalysisState heading="History could not be read" detail={error} alert /> : series && series.points.length > 0 ? (
          <>
            <HistoricalTrend series={series} direction={query.direction} headingRef={trendHeading} />
            <TrafficPointTable series={series} direction={query.direction} />
          </>
        ) : <AnalysisState heading="No traffic in this range" detail="Choose a wider range or wait until the collector stores a complete minute." />}
      </div>
      <TrafficRankings rankings={rankings} loading={rankingsLoading} direction={query.direction} error={rankingsError} selected={query} drillDown={drillDown} />
    </section>
  );
}

function TrafficFilters({ query, dimensions, error, drafts, setDraft, add, remove }: {
  query: AnalyzeQuery;
  dimensions: DimensionsResponse;
  error: string;
  drafts: Record<FilterDimension, string>;
  setDraft: (dimension: FilterDimension, value: string) => void;
  add: (dimension: FilterDimension) => void;
  remove: (dimension: FilterDimension, value: string) => void;
}) {
  return (
    <section className="traffic-filters" aria-labelledby="traffic-filters-title">
      <div className="filter-heading"><h2 id="traffic-filters-title">Filters</h2><span>OR within / AND across</span></div>
      <div className="filter-inputs">
        {(["app", "host", "domain"] as const).map((dimension) => {
          const label = dimensionLabel(dimension);
          const list = dimensions[analyzeFilterKey(dimension)] ?? [];
          return (
            <div className="filter-input" key={dimension}>
              <label htmlFor={`filter-${dimension}`}>{label} filter</label>
              <div><input id={`filter-${dimension}`} list={`filter-${dimension}-values`} value={drafts[dimension]} onChange={(event) => setDraft(dimension, event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); add(dimension); } }} />
                <button type="button" onClick={() => add(dimension)} aria-label={`Add ${label} filter`}>Add</button></div>
              <datalist id={`filter-${dimension}-values`}>{list.map((value) => <option value={value} key={value} />)}</datalist>
            </div>
          );
        })}
      </div>
      <div className="filter-chips">
        {(["app", "host", "domain"] as const).flatMap((dimension) => query[analyzeFilterKey(dimension)].map((value) => (
          <button type="button" key={`${dimension}:${value}`} onClick={() => remove(dimension, value)} aria-label={`Remove ${dimensionLabel(dimension)} ${value} filter`}>
            <span>{dimensionLabel(dimension)}</span>{value}<b aria-hidden="true">x</b>
          </button>
        )))}
      </div>
      {error ? <p className="filter-error" role="alert">{error}</p> : null}
    </section>
  );
}

function TrafficRankings({ rankings, loading, direction, error, selected, drillDown }: {
  rankings: Record<FilterDimension, RankingsResponse | null>;
  loading: boolean;
  direction: Direction;
  error: string;
  selected: AnalyzeQuery;
  drillDown: (dimension: FilterDimension, value: string) => void;
}) {
  return (
    <section className="traffic-rankings" aria-labelledby="traffic-rankings-title">
      <header><div><p className="eyebrow">Observed traffic</p><h2 id="traffic-rankings-title">Rankings</h2></div><span>{humanizeReason(direction)} order</span></header>
      {error ? <p className="ranking-error" role="alert">{error}</p> : null}
      <div className="ranking-columns">
        {(["app", "host", "domain"] as const).map((dimension) => {
          const items = rankings[dimension]?.items ?? [];
          return <section className="ranking-list" aria-label={`${dimensionLabel(dimension)} rankings`} key={dimension}>
            <h3>{dimensionLabel(dimension)}s</h3>
            {loading ? <p>Reading observed traffic</p> : rankings[dimension] && items.length === 0 ? <p>No matching observed traffic</p> : (
              <ol>{items.map((item, index) => {
                const active = selected[analyzeFilterKey(dimension)].includes(item.name);
                return <li key={item.name}><button type="button" disabled={active} onClick={() => drillDown(dimension, item.name)} aria-label={`Filter ${dimensionLabel(dimension)} ${item.name}, ${formatBytes(item[direction])}`}><span>{String(index + 1).padStart(2, "0")}</span><strong>{item.name}</strong><b>{formatBytes(item[direction])}</b></button></li>;
              })}</ol>
            )}
          </section>;
        })}
      </div>
    </section>
  );
}

function HistoricalTrend({ series, direction, headingRef }: { series: SeriesResponse; direction: Direction; headingRef: React.RefObject<HTMLHeadingElement | null> }) {
  const values = series.points.map((point) => point[direction].total);
  const peak = Math.max(1, ...values);
  const x = (index: number) => series.points.length === 1 ? 450 : 30 + (index / (series.points.length - 1)) * 840;
  const y = (value: number) => 250 - (value / peak) * 205;
  const polyline = values.map((value, index) => `${x(index)},${y(value)}`).join(" ");
  const selectedTotal = values.reduce((total, value) => total + value, 0);
  const label = `${humanizeReason(direction)} traffic trend, ${series.points.length} points, ${formatBytes(selectedTotal)} in the selected range`;
  return (
    <section className={`historical-trend trend-${direction}`}>
      <h2 className="trend-focus-heading" ref={headingRef} tabIndex={-1}>{humanizeReason(direction)} traffic trend</h2>
      <div className="trend-readout"><span>{humanizeReason(direction)} / selected range</span><strong>{formatBytes(selectedTotal)}</strong><small>Peak {formatBytes(peak)} per {series.granularity}</small></div>
      <svg viewBox="0 0 900 280" preserveAspectRatio="none" role="img" aria-label={label}>
        <line x1="0" y1="250" x2="900" y2="250" className="history-axis" />
        <line x1="0" y1="148" x2="900" y2="148" className="history-gridline" />
        <line x1="0" y1="45" x2="900" y2="45" className="history-gridline" />
        <polyline points={polyline} className="history-line" />
        {values.map((value, index) => <circle key={series.points[index].start} cx={x(index)} cy={y(value)} r="3.5" className="history-point" />)}
      </svg>
      <div className="trend-range"><span>{formatSeriesTime(series.range.from, series.timeZone)}</span><span>{formatSeriesTime(series.range.to, series.timeZone)}</span></div>
    </section>
  );
}

function TrafficPointTable({ series, direction }: { series: SeriesResponse; direction: Direction }) {
  return (
    <div className="point-table-wrap">
      <table className="point-table">
        <caption>{humanizeReason(direction)} attribution by {series.granularity}</caption>
        <thead><tr><th scope="col">Bucket</th><th scope="col">Observed</th><th scope="col">Residual</th><th scope="col">Gap-recovered</th><th scope="col">Total</th></tr></thead>
        <tbody>
          {series.points.map((point) => {
            const totals = point[direction];
            return <tr key={point.start}><th scope="row">{formatSeriesTime(point.start, series.timeZone)}</th><td>{formatBytes(totals.observed)}</td><td>{formatBytes(totals.residual)}</td><td>{formatBytes(totals.gapRecovered)}</td><td>{formatBytes(totals.total)}</td></tr>;
          })}
        </tbody>
      </table>
    </div>
  );
}

function AnalysisState({ heading, detail, alert = false }: { heading: string; detail: string; alert?: boolean }) {
  return <div className="analysis-state" role={alert ? "alert" : "status"}><span aria-hidden="true" /><h2>{heading}</h2><p>{detail}</p></div>;
}

function Status({ status }: { status: StatusResponse | null }) {
  const currentGap = status?.collection.currentGap;
  return (
    <section className="status-page">
      <div><p className="eyebrow">System diagnostics</p><h1>Local observatory</h1><p>Only non-sensitive configuration is shown.</p></div>
      <dl className="diagnostic-grid">
        <Diagnostic label="Controller" value={status?.configuration.controllerUrl ?? "Checking"} detail={controllerDetail(status)} />
        <Diagnostic label="Database" value={status?.database.healthy ? "Database ready" : "Checking"} detail={status ? `${status.database.journalMode.toUpperCase()} · schema ${status.database.schemaVersion}` : "Opening private storage"} />
        <Diagnostic label="Database size" value={formatBytes(status?.database.sizeBytes ?? 0)} detail="Permanent minute history" />
        <Diagnostic label="Database location" value={status?.configuration.databasePath ?? "Checking"} detail="Private local storage · permanent retention" />
        <Diagnostic label="Authentication" value={status?.configuration.controllerAuthentication === "configured" ? "Configured" : "Not configured"} detail="Secret values are never displayed" />
        <Diagnostic
          label="Current Collection gap"
          value={currentGap ? "Gap open" : status ? "No open gap" : "Checking"}
          detail={currentGap ? `${humanizeReason(currentGap.reason)} · Since ${formatTimestamp(currentGap.startedAt)}` : status?.collection.error ?? "Collection is continuous"}
        />
        <GapHistory gaps={status?.collection.recentGaps ?? []} />
      </dl>
    </section>
  );
}

function GapHistory({ gaps }: { gaps: CollectionGap[] }) {
  return (
    <div className="diagnostic gap-history">
      <dt>Recent Collection gaps</dt>
      <dd>
        {gaps.length === 0 ? <p>No closed gaps in the last 24 hours</p> : (
          <ul>
            {gaps.map((gap) => (
              <li key={gap.id}>
                <strong>{gap.disposition === "recovered" ? `Recovered ${formatBytes(gap.recoveredUpload + gap.recoveredDownload)}` : humanizeReason(gap.disposition)}</strong>
                <span>{humanizeReason(gap.reason)}</span>
                <span>{`Upload ${formatBytes(gap.recoveredUpload)} · Download ${formatBytes(gap.recoveredDownload)}`}</span>
              </li>
            ))}
          </ul>
        )}
      </dd>
    </div>
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

function humanizeReason(value: string): string {
  const words = value.replaceAll("_", " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function formatTimestamp(value: string): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
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

function readAnalyzeQuery(): AnalyzeQuery {
  const params = new URLSearchParams(window.location.search);
  const now = new Date();
  const defaultFrom = new Date(now.getTime() - 24 * 60 * 60 * 1000);
  const parsedFrom = new Date(params.get("from") ?? "");
  const parsedTo = new Date(params.get("to") ?? "");
  const validRange = Number.isFinite(parsedFrom.getTime()) && Number.isFinite(parsedTo.getTime()) && parsedTo > parsedFrom;
  const requestedZone = params.get("timeZone") ?? browserTimeZone();
  const direction = params.get("direction");
  const granularity = params.get("granularity");
  return {
    from: (validRange ? parsedFrom : defaultFrom).toISOString(),
    to: (validRange ? parsedTo : now).toISOString(),
    timeZone: isTimeZone(requestedZone) ? requestedZone : browserTimeZone(),
    direction: direction === "upload" || direction === "download" || direction === "total" ? direction : "total",
    granularity: granularity === "minute" || granularity === "hour" || granularity === "day" || granularity === "auto" ? granularity : "auto",
    apps: params.getAll("app"),
    hosts: params.getAll("host"),
    domains: params.getAll("domain"),
  };
}

function serializeAnalyzeQuery(query: AnalyzeQuery): URLSearchParams {
  const parameters = new URLSearchParams({
    from: query.from,
    to: query.to,
    timeZone: query.timeZone,
    direction: query.direction,
    granularity: query.granularity,
  });
  appendTrafficFilters(parameters, query);
  return parameters;
}

function appendTrafficFilters(parameters: URLSearchParams, query: AnalyzeQuery) {
  for (const value of query.apps) parameters.append("app", value);
  for (const value of query.hosts) parameters.append("host", value);
  for (const value of query.domains) parameters.append("domain", value);
}

function analyzeFilterKey(dimension: FilterDimension): "apps" | "hosts" | "domains" {
  if (dimension === "app") return "apps";
  if (dimension === "host") return "hosts";
  return "domains";
}

function dimensionLabel(dimension: FilterDimension): string {
  if (dimension === "app") return "App";
  if (dimension === "host") return "Host";
  return "domain";
}

function hasTrafficFilters(query: AnalyzeQuery): boolean {
  return query.apps.length > 0 || query.hosts.length > 0 || query.domains.length > 0;
}

function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

function isTimeZone(value: string): boolean {
  try {
    new Intl.DateTimeFormat("en", { timeZone: value }).format();
    return true;
  } catch {
    return false;
  }
}

function toDateTimeInZone(value: string, timeZone: string): string {
  const parts = zonedParts(new Date(value), timeZone);
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
}

function zonedDateTimeToInstant(value: string, timeZone: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  if (!match) return null;
  const requested = {
    year: match[1], month: match[2], day: match[3], hour: match[4], minute: match[5],
  };
  const wallClockUTC = Date.UTC(Number(requested.year), Number(requested.month) - 1, Number(requested.day), Number(requested.hour), Number(requested.minute));
  let candidate = new Date(wallClockUTC);
  for (let iteration = 0; iteration < 2; iteration++) {
    const parts = zonedParts(candidate, timeZone);
    const representedUTC = Date.UTC(Number(parts.year), Number(parts.month) - 1, Number(parts.day), Number(parts.hour), Number(parts.minute));
    candidate = new Date(candidate.getTime() + wallClockUTC - representedUTC);
  }
  const resolved = zonedParts(candidate, timeZone);
  return Object.keys(requested).every((key) => requested[key as keyof typeof requested] === resolved[key as keyof typeof resolved]) ? candidate : null;
}

function zonedParts(date: Date, timeZone: string): Record<"year" | "month" | "day" | "hour" | "minute", string> {
  const result = {} as Record<"year" | "month" | "day" | "hour" | "minute", string>;
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  for (const part of parts) {
    if (part.type === "year" || part.type === "month" || part.type === "day" || part.type === "hour" || part.type === "minute") result[part.type] = part.value;
  }
  return result;
}

function formatSeriesTime(value: string, timeZone: string): string {
  return new Intl.DateTimeFormat(undefined, {
    timeZone,
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  }).format(new Date(value));
}

function todaySummaryURL(): string {
  const end = new Date();
  const start = new Date(end.getFullYear(), end.getMonth(), end.getDate());
  const query = new URLSearchParams({ start: start.toISOString(), end: end.toISOString() });
  return `/api/v1/summary?${query.toString()}`;
}
