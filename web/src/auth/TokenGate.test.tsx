// TokenGate tests: with no token the gate shows the input; after a valid token is
// submitted (verify resolves) it renders its children. verify is injected (no
// network); no real token is used.
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider } from "./AuthProvider.tsx";
import { TokenGate } from "./TokenGate.tsx";

function renderGate(verify: (token: string) => Promise<void>) {
  return render(
    <AuthProvider>
      <TokenGate verify={verify}>
        <div>SECRET DASHBOARD</div>
      </TokenGate>
    </AuthProvider>,
  );
}

describe("TokenGate", () => {
  it("shows the token input when no token is present", () => {
    renderGate(() => Promise.resolve());
    expect(screen.getByLabelText(/api token/i)).toBeInTheDocument();
    expect(screen.queryByText("SECRET DASHBOARD")).not.toBeInTheDocument();
  });

  it("renders children after a valid token is verified and submitted", async () => {
    const verify = vi.fn(() => Promise.resolve());
    renderGate(verify);
    const user = userEvent.setup({ delay: null });

    await user.type(screen.getByLabelText(/api token/i), "good-token");
    await user.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() =>
      expect(screen.getByText("SECRET DASHBOARD")).toBeInTheDocument(),
    );
    expect(verify).toHaveBeenCalledWith("good-token");
  });
});
