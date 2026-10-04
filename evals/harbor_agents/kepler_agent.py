"""Harbor adapter for a pinned kepler-agent source revision."""

from __future__ import annotations

import os
import asyncio
import re
import shlex
import ssl
from hashlib import sha256
from pathlib import Path

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
        **kwargs,
    ):
        super().__init__(*args, **kwargs)
        if not self._COMMIT.fullmatch(source_ref):
            raise ValueError("source_ref must be a full 40-character Git commit SHA")
        if not source_repo.startswith("https://"):
            raise ValueError("source_repo must use an https URL")
        retired = {"provider", "protocol", "api_key_env", "base_url_env"}.intersection(kwargs)
        if retired:
            raise ValueError("Current Kepler CLI uses gateway login; remove direct-provider kwargs: " + ", ".join(sorted(retired)))

        self._source_ref = source_ref
        self._source_repo = source_repo
        self._binary_path = Path(binary_path).resolve() if binary_path else None
        if self._binary_path and not self._binary_path.is_file():
            raise ValueError(f"binary_path is not a file: {self._binary_path}")
        default_ca = ssl.get_default_verify_paths().cafile
        self._ca_bundle = Path(default_ca) if default_ca and Path(default_ca).is_file() else None
        self._binary_sha256 = (
            sha256(self._binary_path.read_bytes()).hexdigest()
            if self._binary_path
            else None
        )

    @staticmethod
    def name() -> str:
        return "kepler-agent"

    def version(self) -> str:
        if self._binary_sha256:
            return "binary-sha256:" + self._binary_sha256
        return self._source_ref

    def _env_value(self, key: str) -> str | None:
        return self.extra_env.get(key) or os.environ.get(key)

    async def setup(self, environment: BaseEnvironment) -> None:
        """Install an immutable local binary or build a pinned public revision."""
        if self._binary_path:
            await environment.upload_file(self._binary_path, self._BINARY)
            if self._ca_bundle:
                await environment.upload_file(self._ca_bundle, "/tmp/kepler-eval-ca.pem")
            result = await environment.exec(
                command=(
                    "set -eu; "
                    # Terminal-Bench task images are intentionally minimal and
                    # may omit the system root bundle.  The CLI performs HTTPS
                    # requests itself. Upload trusted roots when possible; otherwise
                    # use the distribution bundle without weakening TLS.
                    "if command -v rg >/dev/null 2>&1 && "
                    "(test -s /tmp/kepler-eval-ca.pem || test -s /etc/ssl/certs/ca-certificates.crt); then :; "
                    "elif command -v apt-get >/dev/null 2>&1; then "
                    "apt-get update; "
                    "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates ripgrep; "
                    "elif command -v apk >/dev/null 2>&1; then "
                    "apk add --no-cache ca-certificates ripgrep; "
                    "fi; "
                    f"chmod 0755 {self._BINARY}; {self._BINARY} --version; rg --version; mkdir -p {Path(self._LOG).parent}"
                ),
                user="root",
                timeout_sec=300,
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
                "ca-certificates git golang-go ripgrep; "
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

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        if not self.model_name:
            raise ValueError("Harbor requires --model for kepler-agent")

        token = self._env_value("KEPLER_TOKEN")
        api_url = self._env_value("EVAL_KEPLER_CONTAINER_API_URL") or self._env_value("EVAL_KEPLER_API_URL")
        if not token or not api_url:
            raise ValueError("Kepler requires KEPLER_TOKEN and EVAL_KEPLER_API_URL; use evals/run_harbor.py for login preflight")
        env = {"KEPLER_TOKEN": token, "KEPLER_API_URL": api_url}
        if self._binary_path and self._ca_bundle:
            env["SSL_CERT_FILE"] = "/tmp/kepler-eval-ca.pem"
        command = " ".join(
            [
                shlex.quote(self._BINARY),
                # Harbor preserves each task image's declared WORKDIR when cwd
                # is omitted. Passing that runtime directory keeps the CLI
                # workspace aligned with task images that use /app, /root, etc.
                '--cwd "$PWD"',
                "--approval project",
                "--unsafe-allow-no-sandbox",
                "--state-dir /tmp/kepler-eval-state",
                "--output jsonl",
                "--",
                shlex.quote(instruction),
            ]
        )
        context.metadata = {
            "source_repo": self._source_repo,
            "source_ref": self._source_ref,
            "binary_sha256": self._binary_sha256,
            "model": self.model_name,
            "transport": "kepler-gateway",
        }
        # Harbor cancels run() at the task deadline. Cancelling Docker's host
        # exec await does not terminate the process inside the task container.
        pid_file = "/tmp/kepler-eval-agent.pid"
        launch = f"echo $$ > {pid_file}; exec {command}"
        try:
            result = await environment.exec(
                command=f"set -o pipefail; sh -c {shlex.quote(launch)} 2>&1 </dev/null | tee {shlex.quote(self._LOG)}",
                env=env,
            )
        except BaseException:
            cleanup = (
                f"if test -s {pid_file}; then pid=$(cat {pid_file}); "
                "case $pid in ''|*[!0-9]*) exit 1;; esac; "
                "kill -INT $pid 2>/dev/null || true; "
                "for i in 1 2 3 4 5; do kill -0 $pid 2>/dev/null || exit 0; sleep 1; done; "
                "kill -KILL $pid 2>/dev/null || true; fi"
            )
            try:
                await asyncio.shield(environment.exec(command=cleanup, timeout_sec=10))
            except Exception:
                context.metadata["cleanup_failed"] = True
            raise
        from evals.run import kepler_metrics
        metrics = kepler_metrics(result.stdout or "")
        context.metadata["metrics"] = metrics
        usage = metrics.get("usage", {})
        for field, key in (("n_input_tokens", "input_tokens"), ("n_cache_tokens", "cache_read_tokens"), ("n_output_tokens", "output_tokens")):
            if key in usage:
                setattr(context, field, usage[key])
        observed = metrics.get("observed_models", [])
        if observed and observed != [self.model_name]:
            raise RuntimeError("Kepler gateway model changed after preflight: " + ", ".join(observed))
        if result.return_code != 0:
            raise RuntimeError(result.stderr or result.stdout or "kepler-agent failed")
