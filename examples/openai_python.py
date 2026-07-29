"""Call OpenAI through spendlease with a scoped lease."""

from openai import OpenAI
from spendlease import Lease


lease = Lease.from_env()
client = OpenAI(**lease.openai_kwargs())

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Explain spend authorization in one sentence."}],
)
print(response.choices[0].message.content)
