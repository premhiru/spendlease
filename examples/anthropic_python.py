"""Call Anthropic through spendlease with a scoped lease."""

from anthropic import Anthropic
from spendlease import Lease


lease = Lease.from_env()
client = Anthropic(**lease.anthropic_kwargs())

message = client.messages.create(
    model="claude-sonnet-5",
    max_tokens=128,
    messages=[{"role": "user", "content": "Explain spend authorization in one sentence."}],
)
print(message.content[0].text)
