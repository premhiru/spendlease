"""Public spendlease SDK types."""

from __future__ import annotations

from dataclasses import dataclass
import json
import os
from typing import Any, Dict, List, Optional
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

    def create_run(
        self, principal_id: str, budget_usd: str, parent_run_id: str = ""
    ) -> Dict[str, Any]:
        """Create a budgeted run for a principal."""

        return self._json(
            "POST",
            f"/api/v1/principals/{parse.quote(principal_id, safe='')}/runs",
            {"budget_usd": budget_usd, "parent_run_id": parent_run_id},
        )

    def list_runs(self, principal_id: str) -> List[Dict[str, Any]]:
        """List a principal's runs, newest first."""

        result = self._json(
            "GET", f"/api/v1/principals/{parse.quote(principal_id, safe='')}/runs"
        )
        return result["runs"]

    def get_run(self, run_id: str) -> Dict[str, Any]:
        """Read one run by ID."""

        return self._json("GET", f"/api/v1/runs/{parse.quote(run_id, safe='')}")

    def close_run(self, run_id: str) -> Dict[str, Any]:
        """Close a run so it can no longer issue leases or spend."""

        return self._json("POST", f"/api/v1/runs/{parse.quote(run_id, safe='')}/close", {})

    def remaining_budget(self, run_id: str) -> Dict[str, Any]:
        """Return the run's effective remaining budget and limiting ancestor."""

        return self._json("GET", f"/api/v1/runs/{parse.quote(run_id, safe='')}/budget")

    def issue_lease(
        self,
        run_id: str,
        ttl_seconds: int = 900,
        providers: Optional[List[str]] = None,
        ceiling_usd: str = "0",
    ) -> Dict[str, Any]:
        """Issue a lease; its token is returned once in this response."""

        return self._json(
            "POST",
            f"/api/v1/runs/{parse.quote(run_id, safe='')}/leases",
            {
                "ttl_seconds": ttl_seconds,
                "providers": providers or [],
                "ceiling_usd": ceiling_usd,
            },
        )

    def list_leases(self, run_id: str) -> List[Dict[str, Any]]:
        """List a run's leases without returning token material."""

        result = self._json("GET", f"/api/v1/runs/{parse.quote(run_id, safe='')}/leases")
        return result["leases"]

    def revoke_lease(self, lease_id: str) -> Dict[str, Any]:
        """Revoke one lease immediately."""

        return self._json(
            "POST", f"/api/v1/leases/{parse.quote(lease_id, safe='')}/revoke", {}
        )

    def verify_ledger(self) -> Dict[str, Any]:
        """Verify the complete hash chain and return its head."""

        return self._json("GET", "/api/v1/ledger/verify")

    def export_ledger(
        self,
        format: str = "json",
        run_id: str = "",
        principal_id: str = "",
        since: str = "",
    ) -> str:
        """Export ledger rows as JSON or CSV text."""

        if format not in ("json", "csv"):
            raise ValueError("format must be json or csv")
        query = parse.urlencode(
            {
                key: value
                for key, value in {
                    "format": format,
                    "run_id": run_id,
                    "principal_id": principal_id,
                    "since": since,
                }.items()
                if value
            }
        )
        return self._request("GET", "/api/v1/ledger/export?" + query).decode()

    def _post(self, path: str, fields: Dict[str, str]) -> str:
        body = parse.urlencode(fields).encode()
        req = request.Request(self.base_url + path, data=body, method="POST")
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        req.add_header("X-Spendlease-Admin", "1")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with request.urlopen(req) as response:
                return response.read().decode()
        except error.HTTPError as exc:
            message = exc.read().decode(errors="replace").strip() or exc.reason
            raise SpendleaseError(exc.code, message) from exc

    def _json(
        self, method: str, path: str, body: Optional[Dict[str, Any]] = None
    ) -> Dict[str, Any]:
        raw = self._request(
            method,
            path,
            None if body is None else json.dumps(body, separators=(",", ":")).encode(),
        )
        return json.loads(raw.decode())

    def _request(self, method: str, path: str, body: Optional[bytes] = None) -> bytes:
        req = request.Request(self.base_url + path, data=body, method=method)
        if body is not None:
            req.add_header("Content-Type", "application/json")
        if method not in ("GET", "HEAD", "OPTIONS"):
            req.add_header("X-Spendlease-Admin", "1")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with request.urlopen(req) as response:
                return response.read()
        except error.HTTPError as exc:
            message = exc.read().decode(errors="replace").strip() or exc.reason
            raise SpendleaseError(exc.code, message) from exc
