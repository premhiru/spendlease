/** Call OpenAI through spendlease with a scoped lease. */
import OpenAI from "openai";
import { Lease } from "@spendlease/sdk";

const lease = Lease.fromEnv();
const client = new OpenAI(lease.openAIOptions());

const response = await client.chat.completions.create({
  model: "gpt-5.4-mini",
  max_completion_tokens: 512,
  messages: [{ role: "user", content: "Explain spend authorization in one sentence." }],
});
console.log(response.choices[0]?.message.content);
