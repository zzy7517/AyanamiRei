// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

function respond(body: unknown, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("createTerminalTicket", () => {
  it("posts to the ticket endpoint with the client's usual headers and returns the ticket", async () => {
    const fetchMock = respond({ ticket: "tk_1", expiresInSeconds: 120 });
    const client = new ApiClient("https://api.example.test");

    expect(await client.createTerminalTicket()).toEqual({ ticket: "tk_1", expiresInSeconds: 120 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0]! as [string, RequestInit];
    expect(url).toBe("https://api.example.test/api/terminal/ticket");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("include");
  });

  it("fills in the contract's lifetime when the server sends none", async () => {
    respond({ ticket: "tk_1" });
    expect(await new ApiClient("https://api.example.test").createTerminalTicket()).toEqual({
      ticket: "tk_1",
      expiresInSeconds: 120,
    });
  });

  it("answers null, not an empty ticket, when the response carries none", async () => {
    for (const body of [{}, { ticket: "" }, { ticket: 1 }, { enabled: false }, null]) {
      respond(body);
      expect(await new ApiClient("https://api.example.test").createTerminalTicket()).toBeNull();
    }
  });

  it("rejects with the status when the endpoint is refused, as it is while the flag is off", async () => {
    respond({ message: "not found" }, 404);
    await expect(new ApiClient("https://api.example.test").createTerminalTicket()).rejects.toMatchObject({
      name: "ApiError",
      status: 404,
    });
    respond({ message: "no" }, 429);
    const error = await new ApiClient("https://api.example.test")
      .createTerminalTicket()
      .catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(ApiError);
  });
});
