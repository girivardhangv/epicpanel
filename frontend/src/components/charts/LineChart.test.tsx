import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LineChart } from "./LineChart";

describe("LineChart", () => {
  it("renders an accessible chart with data points", () => {
    const points = [
      { t: "2026-08-29T10:00:00Z", value: 10 },
      { t: "2026-08-29T10:15:00Z", value: 40 },
      { t: "2026-08-29T10:30:00Z", value: 25 },
    ];
    render(
      <LineChart
        title="CPU usage"
        points={points}
        rangeSeconds={86400}
        yMax={100}
      />,
    );
    const svg = screen.getByRole("img", { name: "CPU usage" });
    expect(svg).toBeTruthy();
    expect(screen.getByText("CPU usage")).toBeTruthy();
    // threshold-free, no error state
    expect(screen.queryByText(/No data yet/)).toBeNull();
  });

  it("shows an honest empty state when there is no data (never fabricated)", () => {
    render(<LineChart title="Memory usage" points={[]} rangeSeconds={3600} />);
    expect(screen.getByText(/No data yet/)).toBeTruthy();
  });

  it("shows an error state with retry", () => {
    let retried = 0;
    render(
      <LineChart
        title="CPU"
        points={[]}
        rangeSeconds={3600}
        error={new Error("boom")}
        onRetry={() => retried++}
      />,
    );
    expect(screen.getByText(/not available right now/)).toBeTruthy();
  });
});
