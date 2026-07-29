import assert from "node:assert/strict";
import test from "node:test";

import { AdminClient, Lease, SpendleaseError } from "../dist/index.js";

test("Lease configures official vendor SDKs", () => {
  const lease = new Lease("sll_test", "https://gateway.example/");
  assert.deepEqual(lease.openAIOptions(), {
    baseURL: "https://gateway.example/v1",
    apiKey: "sll_test",
  });
  assert.deepEqual(lease.anthropicOptions(), {
    baseURL: "https://gateway.example",
    apiKey: "sll_test",
  });
});

test("Lease refuses a principal key", () => {
  assert.throws(() => new Lease("slk_principal"), /sll_/);
});

test("Lease reads explicit environment values", () => {
  const lease = Lease.fromEnv({
    SPENDLEASE_LEASE_TOKEN: "sll_env",
    SPENDLEASE_URL: "https://lease.test",
  });
  assert.equal(lease.baseURL, "https://lease.test");
});

test("AdminClient uses the guarded form endpoint", async () => {
  let captured;
  globalThis.fetch = async (url, options) => {
    captured = { url, options };
    return { ok: true, status: 200, statusText: "OK", text: async () => "table" };
  };
  const body = await new AdminClient("https://gateway.example/", "admin-secret").setMode(
    "prn_test",
    "enforce",
  );
  assert.equal(body, "table");
  assert.equal(captured.url, "https://gateway.example/admin/principals/prn_test/mode");
  assert.equal(captured.options.headers.authorization, "Bearer admin-secret");
  assert.equal(captured.options.body.toString(), "mode=enforce");
});

test("AdminClient reports HTTP failures", async () => {
  globalThis.fetch = async () => ({
    ok: false,
    status: 403,
    statusText: "Forbidden",
    text: async () => "denied",
  });
  await assert.rejects(new AdminClient().revoke("prn_test"), (error) => {
    assert.ok(error instanceof SpendleaseError);
    assert.equal(error.status, 403);
    return true;
  });
});
