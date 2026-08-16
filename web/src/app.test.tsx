import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { App } from "./app";

const statusResponse = {
  apiVersion: "v1",
  collector: {
    state: "unavailable",
    reason: "not_connected",
    message: "Waiting for Mihomo External Controller collection.",
    controllerVersion: null,
    lastSample: null,
  },
  live: {
    uploadBytesPerSecond: 0,
    downloadBytesPerSecond: 0,
    activeConnections: 0,
  },
  database: {
    healthy: true,
    sizeBytes: 12288,
    schemaVersion: 3,
    journalMode: "wal",
    error: null,
  },
  collection: {
    currentGap: {
      id: 2,
      startedAt: "2026-08-14T06:20:00Z",
      endedAt: null,
      open: true,
      reason: "authentication_failed",
      disposition: "open",
      recoveredUpload: 0,
      recoveredDownload: 0,
    },
    recentGaps: [{
      id: 1,
      startedAt: "2026-08-14T06:00:00Z",
      endedAt: "2026-08-14T06:10:00Z",
      open: false,
      reason: "disconnected",
      disposition: "recovered",
      recoveredUpload: 30,
      recoveredDownload: 60,
    }],
    error: null,
  },
  configuration: {
    controllerUrl: "http://127.0.0.1:9090",
    controllerAuthentication: "not_configured",
    dashboardAddress: "127.0.0.1:9091",
    sampleInterval: "1s",
    databasePath: "/Users/test/Library/Application Support/mihomo-traffic-monitor/traffic.db",
  },
};

const summaryResponse = {
	apiVersion: "v1",
	range: { start: "2026-08-14T00:00:00+08:00", end: "2026-08-14T15:00:00+08:00" },
	upload: { observed: 40000, residual: 10000, gapRecovered: 0, total: 50000 },
	download: { observed: 60000, residual: 15000, gapRecovered: 0, total: 75000 },
	total: { observed: 100000, residual: 25000, gapRecovered: 0, total: 125000 },
	coverage: 0.8,
	leaders: {
		apps: [
			{ name: "Safari", upload: 30000, download: 40000, total: 70000 },
			{ name: "curl", upload: 10000, download: 20000, total: 30000 },
		],
		hosts: [{ name: "example.com", upload: 25000, download: 35000, total: 60000 }],
	},
};

const emptySeriesResponse = {
  apiVersion: "v1",
  granularity: "minute",
  pointLimit: 400,
  timeZone: "UTC",
  range: { from: "2026-08-13T12:00:00Z", to: "2026-08-14T12:00:00Z" },
  points: [],
};

const seriesResponse = {
  ...emptySeriesResponse,
  granularity: "hour",
  points: [
    {
      start: "2026-08-14T10:00:00Z",
      upload: { observed: 1024, residual: 256, gapRecovered: 0, total: 1280 },
      download: { observed: 2048, residual: 0, gapRecovered: 512, total: 2560 },
      total: { observed: 3072, residual: 256, gapRecovered: 512, total: 3840 },
    },
    {
      start: "2026-08-14T11:00:00Z",
      upload: { observed: 2048, residual: 0, gapRecovered: 0, total: 2048 },
      download: { observed: 4096, residual: 512, gapRecovered: 0, total: 4608 },
      total: { observed: 6144, residual: 512, gapRecovered: 0, total: 6656 },
    },
  ],
};

function mockAPI() {
	return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
		const path = String(input);
		const payload = path.startsWith("/api/v1/summary?") ? summaryResponse : path.startsWith("/api/v1/series?") ? emptySeriesResponse : statusResponse;
		return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
	});
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState({}, "", "/");
});

test("user can inspect the unavailable local observatory and navigate its empty states", async () => {
  mockAPI();
  const user = userEvent.setup();

  render(<App />);

  expect(await screen.findByText("Controller unavailable")).toBeVisible();
  expect(screen.getByText("Database ready")).toBeVisible();
  expect(await screen.findByText("80.0%")).toBeVisible();
  expect(screen.getByRole("heading", { name: "Today" })).toBeVisible();
  expect(screen.getByText("Safari")).toBeVisible();
  expect(screen.getByText("example.com")).toBeVisible();
  expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");

  await user.click(screen.getByRole("link", { name: "Analyze" }));
  expect(await screen.findByRole("heading", { name: "No traffic in this range" })).toBeVisible();
  expect(window.location.pathname).toBe("/analyze");

  await user.click(screen.getByRole("link", { name: "Status" }));
  expect(screen.getByText("http://127.0.0.1:9090")).toBeVisible();
  expect(screen.getByText("WAL · schema 3")).toBeVisible();
  expect(screen.getByText(statusResponse.configuration.databasePath)).toBeVisible();
  expect(screen.getByText("Gap open")).toBeVisible();
  expect(screen.getByText(/Authentication failed/)).toBeVisible();
  expect(screen.getByText("Recovered 90 B")).toBeVisible();
  expect(screen.getByText("Upload 30 B · Download 60 B")).toBeVisible();
});

test("Analyze restores its URL and requests the exact local-zone series", async () => {
  const query = "from=2026-08-14T10%3A00%3A00.000Z&to=2026-08-14T12%3A00%3A00.000Z&timeZone=UTC&direction=download&granularity=hour";
  window.history.replaceState({}, "", `/analyze?${query}`);
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    const payload = path.startsWith("/api/v1/series?") ? seriesResponse : path.startsWith("/api/v1/summary?") ? summaryResponse : statusResponse;
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  });

  render(<App />);

  expect(await screen.findByRole("heading", { name: "Traffic history" })).toBeVisible();
  expect(screen.getByRole("img", { name: /Download traffic trend.*2 points/i })).toBeVisible();
  expect(screen.getByRole("radio", { name: "Download" })).toBeChecked();
  expect(screen.getByLabelText("From")).toHaveValue("2026-08-14T10:00");
  expect(screen.getByLabelText("To")).toHaveValue("2026-08-14T12:00");
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/series?from=2026-08-14T10%3A00%3A00.000Z&to=2026-08-14T12%3A00%3A00.000Z&timeZone=UTC&granularity=hour", expect.objectContaining({ signal: expect.any(AbortSignal) }));

  await userEvent.click(screen.getByRole("link", { name: "Analyze" }));
  expect(window.location.search).toBe(`?${query}`);
});

test("Analyze switches direction and applies a reproducible range", async () => {
  window.history.replaceState({}, "", "/analyze?from=2026-08-14T10%3A00%3A00.000Z&to=2026-08-14T12%3A00%3A00.000Z&timeZone=UTC&direction=upload&granularity=hour");
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    const payload = path.startsWith("/api/v1/series?") ? seriesResponse : path.startsWith("/api/v1/summary?") ? summaryResponse : statusResponse;
    return new Response(JSON.stringify(payload), { status: 200 });
  });
  const user = userEvent.setup();
  render(<App />);
  expect(await screen.findByRole("img", { name: /Upload traffic trend/i })).toBeVisible();

  await user.click(screen.getByRole("radio", { name: "Total" }));
  expect(screen.getByRole("img", { name: /Total traffic trend/i })).toBeVisible();
  expect(new URLSearchParams(window.location.search).get("direction")).toBe("total");

  fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-08-14T09:00" } });
  await user.click(screen.getByRole("button", { name: "Apply range" }));
  const params = new URLSearchParams(window.location.search);
  expect(params.get("from")).toBe("2026-08-14T09:00:00.000Z");
  expect(params.get("to")).toBe("2026-08-14T12:00:00.000Z");
  expect(params.get("timeZone")).toBe("UTC");
  expect(params.get("direction")).toBe("total");
  expect(params.get("granularity")).toBe("hour");
});

test("Analyze preserves the selected DST-fold instant when the wall time is unchanged", async () => {
  window.history.replaceState({}, "", "/analyze?from=2026-11-01T06%3A30%3A00.000Z&to=2026-11-01T08%3A00%3A00.000Z&timeZone=America%2FNew_York&direction=total&granularity=hour");
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    const payload = path.startsWith("/api/v1/series?") ? { ...emptySeriesResponse, timeZone: "America/New_York", range: { from: "2026-11-01T06:30:00Z", to: "2026-11-01T08:00:00Z" } } : path.startsWith("/api/v1/summary?") ? summaryResponse : statusResponse;
    return new Response(JSON.stringify(payload), { status: 200 });
  });
  const user = userEvent.setup();
  render(<App />);

  expect(await screen.findByLabelText("From")).toHaveValue("2026-11-01T01:30");
  await user.click(screen.getByRole("button", { name: "Apply range" }));

  const params = new URLSearchParams(window.location.search);
  expect(params.get("from")).toBe("2026-11-01T06:30:00.000Z");
  expect(params.get("to")).toBe("2026-11-01T08:00:00.000Z");
});

test("live events drive the directional trace and current connection readouts", async () => {
  class FakeEventSource {
    static instances: FakeEventSource[] = [];
    readonly url: string;
    private listeners = new Map<string, EventListener>();

    constructor(url: string | URL) {
      this.url = String(url);
      FakeEventSource.instances.push(this);
    }

    addEventListener(type: string, listener: EventListener) {
      this.listeners.set(type, listener);
    }

    close() {}

    emitStatus(payload: unknown) {
      this.listeners.get("status")?.(new MessageEvent("status", { data: JSON.stringify(payload) }));
    }
  }
  vi.stubGlobal("EventSource", FakeEventSource);
  mockAPI();

  render(<App />);
  await screen.findByText("Controller unavailable");
  expect(FakeEventSource.instances).toHaveLength(1);
  expect(FakeEventSource.instances[0].url).toBe("/api/v1/live/events");

  const connected = {
    ...statusResponse,
    collector: {
      state: "connected" as const,
      reason: "connected",
      message: "Live traffic collection is active.",
      controllerVersion: "v1.19.0",
      lastSample: "2026-08-14T06:15:00Z",
    },
    live: {
      uploadBytesPerSecond: 2048,
      downloadBytesPerSecond: 4096,
      activeConnections: 7,
    },
  };
  act(() => FakeEventSource.instances[0].emitStatus(connected));

  expect(screen.getByRole("heading", { name: "Traffic is live" })).toBeVisible();
  expect(screen.getByRole("img", { name: "Upload 2.0 KB/s above baseline; download 4.0 KB/s below baseline" })).toBeVisible();
  expect(screen.getByText("7")).toBeVisible();
});
