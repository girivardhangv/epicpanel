import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { Button } from "./Button";

describe("Button", () => {
  it("renders its children as accessible text", () => {
    render(<Button>Save changes</Button>);
    expect(screen.getByRole("button", { name: "Save changes" })).toBeTruthy();
  });

  it("is disabled and shows pending state while loading", () => {
    render(<Button loading>Save</Button>);
    const btn = screen.getByRole("button") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(screen.queryByText("Save")).not.toBeNull();
  });

  it("respects an explicit disabled prop", () => {
    const onClick = vi.fn();
    render(
      <Button disabled onClick={onClick}>
        Delete
      </Button>,
    );
    (screen.getByRole("button") as HTMLButtonElement).click();
    expect(onClick).not.toHaveBeenCalled();
  });
});
