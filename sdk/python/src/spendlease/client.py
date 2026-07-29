"""Public spendlease SDK types."""

from __future__ import annotations

from dataclasses import dataclass
import os
from typing import Dict
from urllib import error, parse, request


class SpendleaseError(RuntimeError):
    """An HTTP error returned by the spendlease control plane."""

    def __init__(self, status: int, message: str) -> None:
        super().__init__(f"spendlease returned HTTP {status}: {message}")
        self.status = status
        self.message = message


@dataclass(frozen=True)
class Lease:
    """A gateway URL and short-lived lease token for vendor SDK clients."""

    token: str
    base_url: str = "http://localhost:4000"

    def __post_init__(self) -> None:
        if not self.token.startswith("sll_"):
            raise ValueError("token must be a spendlease lease token beginning with sll_")
        object.__setattr__(self, "base_url", self.base_url.rstrip("/"))

    @classmethod
    def from_env(cls) -> "Lease":
        """Build a lease from SPENDLEASE_LEASE_TOKEN and SPENDLEASE_URL."""

        token = os.environ.get("SPENDLEASE_LEASE_TOKEN", "")
        if not token:
            raise ValueError("SPENDLEASE_LEASE_TOKEN is not set")
        return cls(token, os.environ.get("SPENDLEASE_URL", "http://localhost:4000"))

    def openai_kwargs(self) -> Dict[str, str]:
        """Return keyword arguments accepted by ``openai.OpenAI``."""

        return {"base_url": f"{self.base_url}/v1", "api_key": self.token}

    def anthropic_kwargs(self) -> Dict[str, str]:
        """Return keyword arguments accepted by ``anthropic.Anthropic``."""

        return {"base_url": self.base_url, "api_key": self.token}


class AdminClient:
    """A minimal client for the guarded spendlease admin endpoints."""

    def __init__(self, base_url: str = "http://localhost:4000", token: str = "") -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token

    def set_mode(self, principal_id: str, mode: str) -> str:
        """Set a principal to ``observe`` or ``enforce`` and return the table HTML."""

        if mode not in ("observe", "enforce"):
            raise ValueError("mode must be observe or enforce")
        return self._post(
            f"/admin/principals/{parse.quote(principal_id, safe='')}/mode",
            {"mode": mode},
        )

    def revoke(self, principal_id: str) -> str:
        """Activate the kill switch for a principal and return the table HTML."""

        return self._post(f"/admin/principals/{parse.quote(principal_id, safe='')}/revoke", {})

    def _post(self, path: str, fields: Dict[str, str]) -> str:
        body = parse.urlencode(fields).encode()
        req = request.Request(self.base_url + path, data=body, method="POST")
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with request.urlopen(req) as response:
                return response.read().decode()
        except error.HTTPError as exc:
            message = exc.read().decode(errors="replace").strip() or exc.reason
            raise SpendleaseError(exc.code, message) from exc
