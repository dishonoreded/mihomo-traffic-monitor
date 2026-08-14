import { act, render, screen } from "@testing-library/react";
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
    schemaVersion: 2,
    journalMode: "wal",
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

function mockAPI() {
	return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
		const path = String(input);
		const payload = path.startsWith("/api/v1/summary?") ? summaryResponse : statusResponse;
		return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
	});
}

afterEach(() => {
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
  expect(screen.getByRole("heading", { name: "No traffic history yet" })).toBeVisible();
  expect(window.location.pathname).toBe("/analyze");

  await user.click(screen.getByRole("link", { name: "Status" }));
  expect(screen.getByText("http://127.0.0.1:9090")).toBeVisible();
  expect(screen.getByText("WAL · schema 2")).toBeVisible();
  expect(screen.getByText(statusResponse.configuration.databasePath)).toBeVisible();
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
