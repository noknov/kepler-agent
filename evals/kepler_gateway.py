"""Resolve existing CLI authentication and verify the evaluated gateway model."""
from __future__ import annotations

import json
import os
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen


def prepare(expected_model: str | None = None) -> dict:
    credentials = {}
    path = Path.home() / ".config/kepler-agent/credentials.json"
    if not (os.environ.get("EVAL_KEPLER_API_URL") and os.environ.get("KEPLER_TOKEN")) and path.is_file():
        credentials = json.loads(path.read_text())
    api_url = (os.environ.get("EVAL_KEPLER_API_URL") or credentials.get("api_url", "")).rstrip("/")
    token = os.environ.get("KEPLER_TOKEN") or credentials.get("token", "")
    if not api_url or not token:
        raise ValueError("Kepler login required: run bin/kepler-agent login, or set EVAL_KEPLER_API_URL and KEPLER_TOKEN")
    parsed = urlsplit(api_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError("EVAL_KEPLER_API_URL must be a gateway origin without credentials or query parameters")
    request = Request(api_url + "/cli/bootstrap", headers={"Authorization": "Bearer " + token, "ngrok-skip-browser-warning": "true"})
    try:
        with urlopen(request, timeout=30) as response:
            bootstrap = json.load(response)
    except HTTPError as error:
        if error.code in {401, 403}:
            raise ValueError(f"Kepler gateway HTTP {error.code}: run bin/kepler-agent login --api-url {api_url} again") from None
        raise ValueError(f"Kepler gateway bootstrap HTTP {error.code}") from None
    except URLError as error:
        raise ValueError(f"Kepler gateway unreachable ({type(error.reason).__name__}); check EVAL_KEPLER_API_URL") from None
    model = bootstrap.get("model")
    if not isinstance(model, str) or not model or not bootstrap.get("protocol"):
        raise ValueError("Kepler bootstrap did not return a model and protocol")
    if expected_model and expected_model != model:
        raise ValueError(f"Requested model {expected_model!r} differs from gateway model {model!r}; CLI model selection is gateway-managed")
    # Secrets stay in the child environment, never in invocation/result files.
    os.environ["EVAL_KEPLER_API_URL"] = api_url
    os.environ["KEPLER_TOKEN"] = token
    return {"model": model, "protocol": bootstrap["protocol"], "api_url": api_url}
