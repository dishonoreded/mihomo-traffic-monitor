import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { App } from "./app";

const statusResponse = {
  apiVersion: "v1",
  collector: {
    state: "unavailable",
    reason: "not_connected",
    message: "Waiting for Mihomo External Controller collection.",
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
    schemaVersion: 1,
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

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
});

test("user can inspect the unavailable local observatory and navigate its empty states", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(statusResponse), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  const user = userEvent.setup();

  render(<App />);

  expect(await screen.findByText("Controller unavailable")).toBeVisible();
  expect(screen.getByText("Database ready")).toBeVisible();
  expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute("aria-current", "page");

  await user.click(screen.getByRole("link", { name: "Analyze" }));
  expect(screen.getByRole("heading", { name: "No traffic history yet" })).toBeVisible();
  expect(window.location.pathname).toBe("/analyze");

  await user.click(screen.getByRole("link", { name: "Status" }));
  expect(screen.getByText("http://127.0.0.1:9090")).toBeVisible();
  expect(screen.getByText("WAL · schema 1")).toBeVisible();
  expect(screen.getByText(statusResponse.configuration.databasePath)).toBeVisible();
});
