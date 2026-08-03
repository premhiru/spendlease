/** Call Anthropic through spendlease with a scoped lease. */
import Anthropic from "@anthropic-ai/sdk";
import { Lease } from "@spendlease/sdk";

const lease = Lease.fromEnv();
const client = new Anthropic(lease.anthropicOptions());

const message = await client.messages.create({
  model: "claude-sonnet-5",
  max_tokens: 128,
  messages: [{ role: "user", content: "Explain spend authorization in one sentence." }],
});

const first = message.content[0];
if (first?.type === "text") console.log(first.text);
