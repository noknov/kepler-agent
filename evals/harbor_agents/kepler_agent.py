"""Harbor adapter for a pinned kepler-agent source revision."""

from __future__ import annotations

import os
import re
import shlex
from hashlib import sha256
from pathlib import Path
from typing import override

from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext


class KeplerAgent(BaseAgent):
    """Run the local kepler-agent CLI inside Harbor's task environment.

    The adapter deliberately builds from an immutable Git commit in each task
    environment.  This makes a benchmark result attributable to a product
    revision rather than to whichever local binary happened to be on PATH.
    """

    _COMMIT = re.compile(r"^[0-9a-f]{40}$")
    _DEFAULT_SOURCE_REPO = "https://github.com/noknov/kepler-agent.git"
    _BINARY = "/usr/local/bin/kepler-agent"
    _LOG = "/logs/agent/kepler-agent.txt"

    def __init__(
        self,
        *args,
        source_ref: str,
        source_repo: str = _DEFAULT_SOURCE_REPO,
        binary_path: str | None = None,
        provider: str = "openai",
        protocol: str = "responses",
        api_key_env: str = "OPENAI_API_KEY",
        base_url_env: str = "OPENAI_BASE_URL",
        max_steps: int | str | None = None,
        max_output_tokens: int | str | None = None,
        max_context_tokens: int | str | None = None,
        autocompact_buffer: int | str | None = None,
        reasoning_effort: str | None = None,
        **kwargs,
    ):
        super().__init__(*args, **kwargs)
        if not self._COMMIT.fullmatch(source_ref):
            raise ValueError("source_ref must be a full 40-character Git commit SHA")
        if not source_repo.startswith("https://"):
            raise ValueError("source_repo must use an https URL")
        if protocol not in {"openai", "responses", "anthropic"}:
            raise ValueError("protocol must be openai, responses, or anthropic")

        self._source_ref = source_ref
        self._source_repo = source_repo
        self._binary_path = Path(binary_path).resolve() if binary_path else None
        if self._binary_path and not self._binary_path.is_file():
            raise ValueError(f"binary_path is not a file: {self._binary_path}")
        self._binary_sha256 = (
            sha256(self._binary_path.read_bytes()).hexdigest()
            if self._binary_path
            else None
        )
        self._provider = provider
        self._protocol = protocol
        self._api_key_env = api_key_env
        self._base_url_env = base_url_env
        self._max_steps = positive_int("max_steps", max_steps)
        self._max_output_tokens = positive_int("max_output_tokens", max_output_tokens)
        self._max_context_tokens = positive_int("max_context_tokens", max_context_tokens)
        self._autocompact_buffer = nonnegative_int("autocompact_buffer", autocompact_buffer)
        if self._max_context_tokens and self._autocompact_buffer >= self._max_context_tokens:
            raise ValueError("autocompact_buffer must be smaller than max_context_tokens")
        self._reasoning_effort = reasoning_effort.strip() if reasoning_effort else None

    @staticmethod
    @override
    def name() -> str:
        return "kepler-agent"

    @override
    def version(self) -> str:
        return self._source_ref

    def _env_value(self, key: str) -> str | None:
        return self.extra_env.get(key) or os.environ.get(key)

    @override
    async def setup(self, environment: BaseEnvironment) -> None:
        """Install an immutable local binary or build a pinned public revision."""
        if self._binary_path:
            await environment.upload_file(self._binary_path, self._BINARY)
            result = await environment.exec(
                command=(
                    "set -eu; "
                    # Terminal-Bench task images are intentionally minimal and
                    # may omit the system root bundle.  The CLI performs HTTPS
                    # requests itself, so install the distribution bundle before
                    # invoking it rather than weakening TLS verification.
                    "if command -v apt-get >/dev/null 2>&1; then "
                    "apt-get update; "
                    "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates; "
                    "elif command -v apk >/dev/null 2>&1; then "
                    "apk add --no-cache ca-certificates; "
                    "fi; "
                    f"chmod 0755 {self._BINARY}; mkdir -p {Path(self._LOG).parent}"
                ),
                user="root",
                timeout_sec=120,
            )
            if result.return_code != 0:
                raise RuntimeError(
                    result.stderr or result.stdout or "kepler-agent binary install failed"
                )
            return

        source = "/opt/kepler-agent"
        quoted_repo = shlex.quote(self._source_repo)
        quoted_ref = shlex.quote(self._source_ref)
        result = await environment.exec(
            command=(
                "set -eu; "
                "apt-get update; "
                "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "
                "ca-certificates git golang-go; "
                f"git clone {quoted_repo} {source}; "
                f"git -C {source} checkout --detach {quoted_ref}; "
                f'test "$(git -C {source} rev-parse HEAD)" = {quoted_ref}; '
                f"cd {source}; "
                "GOCACHE=/tmp/kepler-agent-go-build "
                f"go build -trimpath -o {self._BINARY} ./cli/cmd/kepler-agent; "
                f"mkdir -p {Path(self._LOG).parent}"
            ),
            user="root",
            timeout_sec=900,
        )
        if result.return_code != 0:
            raise RuntimeError(result.stderr or result.stdout or "kepler-agent setup failed")

    @override
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        if not self.model_name:
            raise ValueError("Harbor requires --model for kepler-agent")

        api_key = self._env_value(self._api_key_env)
        base_url = self._env_value(self._base_url_env)
        if not api_key:
            raise ValueError(f"missing model credential: {self._api_key_env}")
        if not base_url:
            raise ValueError(f"missing model endpoint: {self._base_url_env}")

        env = {
            self._api_key_env: api_key,
            self._base_url_env: base_url,
        }
        command = " ".join(
            [
                shlex.quote(self._BINARY),
                # Harbor preserves each task image's declared WORKDIR when cwd
                # is omitted. Passing that runtime directory keeps the CLI
                # workspace aligned with task images that use /app, /root, etc.
                '--cwd "$PWD"',
                f"--provider {shlex.quote(self._provider)}",
                f"--protocol {shlex.quote(self._protocol)}",
                f"--model {shlex.quote(self.model_name)}",
                f"--base-url {shlex.quote(base_url)}",
                f"--api-key-env {shlex.quote(self._api_key_env)}",
                "--approval project",
                "--unsafe-allow-no-sandbox",
                "--output text",
                "--",
                shlex.quote(instruction),
            ]
        )
        runtime_flags = []
        for name, value in (
            ("max-steps", self._max_steps),
            ("max-output-tokens", self._max_output_tokens),
            ("max-context-tokens", self._max_context_tokens),
            ("autocompact-buffer", self._autocompact_buffer),
        ):
            if value is not None:
                runtime_flags.append(f"--{name} {value}")
        if self._reasoning_effort:
            runtime_flags.append(f"--reasoning-effort {shlex.quote(self._reasoning_effort)}")
        if runtime_flags:
            command = command.replace(" --approval project", " " + " ".join(runtime_flags) + " --approval project", 1)
        result = await environment.exec(
            command=(
                "set -o pipefail; "
                f"{command} 2>&1 </dev/null | tee {shlex.quote(self._LOG)}"
            ),
            env=env,
        )
        context.metadata = {
            "source_repo": self._source_repo,
            "source_ref": self._source_ref,
            "binary_sha256": self._binary_sha256,
            "provider": self._provider,
            "protocol": self._protocol,
            "max_steps": self._max_steps,
            "max_output_tokens": self._max_output_tokens,
            "max_context_tokens": self._max_context_tokens,
            "autocompact_buffer": self._autocompact_buffer,
            "reasoning_effort": self._reasoning_effort,
        }
        if result.return_code != 0:
            raise RuntimeError(result.stderr or result.stdout or "kepler-agent failed")


def positive_int(name: str, value: int | str | None) -> int | None:
    parsed = nonnegative_int(name, value)
    if parsed is not None and parsed <= 0:
        raise ValueError(f"{name} must be positive")
    return parsed


def nonnegative_int(name: str, value: int | str | None) -> int | None:
    if value is None:
        return None
    if isinstance(value, bool):
        raise ValueError(f"{name} must be a positive integer")
    try:
        parsed = int(value)
    except (TypeError, ValueError) as error:
        raise ValueError(f"{name} must be a positive integer") from error
    if parsed < 0:
        raise ValueError(f"{name} must not be negative")
    return parsed
