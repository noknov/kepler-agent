#!/usr/bin/env python3
"""Cache a verified, static upstream ripgrep binary before starting trials."""
import fnmatch
import json
import subprocess
import time
import tomllib
import hashlib
import io
from pathlib import Path
import tarfile
from urllib.request import urlopen

VERSION = "14.1.1"
ARCHIVE = f"ripgrep-{VERSION}-x86_64-unknown-linux-musl"
SHA256 = "4cf9f2741e6c465ffdb7c26f38056a59e2a2544b51f7cc128ef28337eeae4d8e"
URL = f"https://github.com/BurntSushi/ripgrep/releases/download/{VERSION}/{ARCHIVE}.tar.gz"
ROOT = Path(__file__).resolve().parents[1]

def prepare() -> Path:
    cache = ROOT / "bin" / f"{ARCHIVE}.tar.gz"
    cache.parent.mkdir(parents=True, exist_ok=True)
    data = cache.read_bytes() if cache.exists() else None
    if data is None:
        with urlopen(URL, timeout=60) as response:
            data = response.read(10 * 1024 * 1024)
    if hashlib.sha256(data).hexdigest() != SHA256:
        raise ValueError("ripgrep archive checksum mismatch")
    cache.write_bytes(data)
    binary = ROOT / "bin" / "rg-linux-musl"
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        source = archive.extractfile(f"{ARCHIVE}/rg")
        if source is None:
            raise ValueError("ripgrep binary missing from archive")
        binary.write_bytes(source.read())
    binary.chmod(0o755)
    return binary

def prepare_images(dataset: str, includes: list[str], harbor: str = "harbor") -> list[dict]:
    # Go ignores dot-directories when expanding ./...; downloaded benchmark
    # workspaces and grader files must not become application packages.
    root = ROOT / ".cache" / "harbor-datasets" / dataset.replace("/", "-")
    # Registry resolution happens before timed trials, so cold image pulls and
    # registry outages do not consume task execution budgets.
    subprocess.run([harbor, "datasets", "download", dataset, "--output-dir", str(root)], check=True, timeout=600)
    images = set()
    matched_tasks = 0
    for path in root.rglob("task.toml"):
        task = tomllib.loads(path.read_text())
        name = task.get("task", {}).get("name", "")
        if includes and not any(fnmatch.fnmatchcase(name, pattern) for pattern in includes):
            continue
        matched_tasks += 1
        image = task.get("environment", {}).get("docker_image")
        if image:
            images.add(image)
    if not matched_tasks:
        raise ValueError("No task images matched preflight; check dataset and task filters")
    prepared = []
    for image in sorted(images):
        cached = subprocess.run(["docker", "image", "inspect", "--format", "{{.Id}}", image], capture_output=True, text=True)
        if cached.returncode:
            for attempt in range(3):
                try:
                    # Image warming is outside official task budgets. Docker owns
                    # registry/network timeouts; a fixed wall-clock cap here
                    # repeatedly aborts legitimate large-layer downloads.
                    subprocess.run(["docker", "pull", image], check=True)
                    break
                except subprocess.CalledProcessError:
                    if attempt == 2:
                        raise RuntimeError(f"Image preflight failed: {image}; no model trials started") from None
                    time.sleep(2 ** attempt)
        info = subprocess.check_output(["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image], text=True)
        prepared.append({"image": image, "digests": json.loads(info)})
    return prepared

if __name__ == "__main__":
    print(prepare())
