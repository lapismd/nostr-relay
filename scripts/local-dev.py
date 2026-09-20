#!/usr/bin/env python3
"""Own the local Community stack. Never print resolved secret configuration."""

import argparse
import fcntl
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlparse, quote

ROOT = Path(__file__).resolve().parents[1]
DOCTOR_USER_AGENT = "Lapis-Local-Doctor/1.0"
COPIED = (
    "relay.env",
    "relay-secret",
    "relay-pubkey",
    "operator-secret",
    "operator-pubkey",
    "github-app-private-key.pem",
    "github-webhook-secret.pem",
    "github-bridge-secret",
    "github-bridge-pubkey",
    "walgit-relay-token",
    "walgit-webhook-secret",
    "s3-access-key-id",
    "s3-secret-access-key",
)
GENERATED = (
    "walgit-relay-token",
    "walgit-webhook-secret",
    "s3-access-key-id",
    "s3-secret-access-key",
    "auth-master-key",
    "agent-bunker-master-key",
    "postgres-password",
)
SECRET_PATHS = {
    "LAPIS_GITHUB_APP_KEY_FILE": "github-app-private-key.pem",
    "LAPIS_GITHUB_WEBHOOK_SECRET_FILE": "github-webhook-secret.pem",
    "LAPIS_GITHUB_BRIDGE_SECRET_FILE": "github-bridge-secret",
    "LAPIS_AUTH_MASTER_KEY_FILE": "auth-master-key",
    "LAPIS_AGENT_BUNKER_MASTER_KEY_FILE": "agent-bunker-master-key",
    "LAPIS_AUTH_GITHUB_CLIENT_SECRET_FILE": "github-oauth-client-secret",
    "LAPIS_AUTH_GOOGLE_CLIENT_SECRET_FILE": "google-oauth-client-secret",
}


def private_write(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink():
        raise RuntimeError(f"Refusing symlink: {path.name}")
    temporary = path.with_name(path.name + ".tmp")
    # Do not follow an existing temporary symlink.
    fd = os.open(
        temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600
    )
    with os.fdopen(fd, "wb") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(value.encode("utf8") if isinstance(value, str) else value)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def read_env(path):
    result = {}
    if not path.exists():
        return result
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        name, separator, raw = line.partition("=")
        if not separator or not re.fullmatch(r"[A-Z][A-Z0-9_]*", name):
            raise RuntimeError("Invalid environment assignment")
        values = shlex.split(raw, comments=True)
        result[name] = " ".join(values)
    return result


def save_env(path, values):
    # Compose understands single-quoted literal dotenv values; no shell evaluation.
    for value in values.values():
        if "'" in str(value) or "\n" in str(value) or "\r" in str(value):
            raise RuntimeError("Environment value contains unsupported quoting")
    private_write(
        path, "".join(f"{key}='{value}'\n" for key, value in sorted(values.items()))
    )


DEFAULT_COMMUNITY_BROWSER_ORIGIN = "https://local-community.lapis.md"
DEFAULT_DESKTOP_RENDERER_ALLOWED_ORIGIN = "http://127.0.0.1:*"


def json_string_array(values):
    return json.dumps(values, separators=(",", ":"))


def ensure_json_string_array_entry(value, entry):
    if not entry:
        return value
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError as error:
        raise RuntimeError("Browser origin list must be a JSON array") from error
    if not isinstance(parsed, list) or not all(
        isinstance(item, str) for item in parsed
    ):
        raise RuntimeError("Browser origin list must be a JSON string array")
    if entry not in parsed:
        parsed.append(entry)
    return json_string_array(parsed)


def ensure_desktop_browser_origins(values):
    values.setdefault(
        "LAPIS_DESKTOP_RENDERER_ALLOWED_ORIGIN",
        DEFAULT_DESKTOP_RENDERER_ALLOWED_ORIGIN,
    )
    desktop_origin = values.get("LAPIS_DESKTOP_RENDERER_ALLOWED_ORIGIN", "")
    for key in (
        "LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS",
        "LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS",
    ):
        values.setdefault(
            key,
            json_string_array([DEFAULT_COMMUNITY_BROWSER_ORIGIN, desktop_origin]),
        )
        values[key] = ensure_json_string_array_entry(values[key], desktop_origin)
    return values


def browser_origin_probes(values):
    origins = [values["LAPIS_COMMUNITY_PUBLIC_URL"]]
    desktop = values.get("LAPIS_DESKTOP_RENDERER_ALLOWED_ORIGIN", "").strip()
    if desktop:
        if desktop.endswith(":*"):
            desktop = desktop[: -len(":*")] + ":1422"
        origins.append(desktop)
    result = []
    for origin in origins:
        if origin and origin not in result:
            result.append(origin)
    return result

def invoke(args, *, env=None, capture=False, cwd=ROOT):
    result = subprocess.run(
        [str(a) for a in args],
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    if result.returncode:
        # Captured commands may contain sensitive server/config output.
        raise RuntimeError(
            f"{Path(str(args[0])).name} operation failed (exit {result.returncode})"
        )
    return result.stdout if capture else ""


def admin(directory, action, env=None):
    binary = ROOT / "bin/local-admin"
    sources = list((ROOT / "cmd/local-admin").glob("*.go")) + [
        ROOT / "go.mod",
        ROOT / "go.sum",
    ]
    if not binary.exists() or any(
        p.stat().st_mtime > binary.stat().st_mtime for p in sources
    ):
        invoke(["go", "build", "-o", binary, "./cmd/local-admin"])
    invoke([binary, action, directory], env=env)


def initialize(directory, source):
    if directory.resolve() == source.resolve():
        raise RuntimeError("Source and destination must differ")
    if directory.is_symlink():
        raise RuntimeError("Credential directory must not be a symlink")
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.chmod(0o700)
    if any(path.is_symlink() for path in directory.rglob("*")):
        raise RuntimeError("Credential symlinks are not allowed")
    new_configuration = not (directory / "relay.env").exists()
    copied = 0
    for name in COPIED:
        incoming, destination = source / name, directory / name
        if destination.is_symlink():
            raise RuntimeError(f"Refusing destination symlink: {name}")
        if not destination.exists() and incoming.exists():
            if not incoming.is_file() or incoming.is_symlink():
                raise RuntimeError(f"Source must be a regular file: {name}")
            private_write(destination, incoming.read_bytes())
            copied += 1
    for name in GENERATED:
        if not (directory / name).exists():
            private_write(directory / name, secrets.token_hex(32) + "\n")
    admin(directory, "init-keys")
    password = (directory / "postgres-password").read_text().strip()
    database_url = f"postgres://lapis_relay:{quote(password,safe='')}@postgres:5432/lapis_relay?sslmode=disable"
    if not (directory / "postgres-url").exists():
        private_write(directory / "postgres-url", database_url + "\n")
    elif (directory / "postgres-url").read_text().strip() != database_url:
        raise RuntimeError("PostgreSQL URL and password files do not match")
    values = read_env(directory / "relay.env")
    defaults = {
        "LAPIS_RELAY_HOST": "local-relay.lapis.md",
        "LAPIS_RELAY_PORT": "8481",
        "LAPIS_GIT_PUBLIC_URL": "https://local-relay.lapis.md",
        "LAPIS_AUTH_PUBLIC_URL": "https://local-relay.lapis.md",
        "LAPIS_COMMUNITY_PUBLIC_URL": DEFAULT_COMMUNITY_BROWSER_ORIGIN,
        "LAPIS_DESKTOP_RENDERER_ALLOWED_ORIGIN": DEFAULT_DESKTOP_RENDERER_ALLOWED_ORIGIN,
        "LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS": json_string_array([
            DEFAULT_COMMUNITY_BROWSER_ORIGIN,
            DEFAULT_DESKTOP_RENDERER_ALLOWED_ORIGIN,
        ]),
        "LAPIS_AUTH_WEBAUTHN_RP_ID": "local-community.lapis.md",
        "LAPIS_AUTH_WEBAUTHN_ORIGINS": '["https://local-community.lapis.md"]',
        "LAPIS_AUTH_GITHUB_CLIENT_ID": "",
        "LAPIS_AUTH_GOOGLE_CLIENT_ID": "",
        "LAPIS_AUTH_MASTER_KEY_VERSION": "v1",
        "LAPIS_AGENT_BUNKER_PUBLIC_URL": "https://local-relay.lapis.md",
        "LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS": json_string_array([
            DEFAULT_COMMUNITY_BROWSER_ORIGIN,
            DEFAULT_DESKTOP_RENDERER_ALLOWED_ORIGIN,
        ]),
        "LAPIS_AGENT_BUNKER_MASTER_KEY_VERSION": "v1",
        "LAPIS_TUNNEL_NAME": "lapis-local-dev",
        "LAPIS_RELAY_IMAGE": "lapis-relay:community-local",
        "LAPIS_WALGIT_IMAGE": "lapis-walgit:local",
        "LAPIS_FIXTURE_IMAGE": "lapis-community-fixture:local",
        "LAPIS_COMMUNITY_PATH": str(ROOT.parent / "lapis-community"),
        "LAPIS_REGISTRY_SITE_PATH": str(ROOT.parent / "nostr-plugin-registry"),
        "REGISTRY_FIXTURE_CREATED_AT": str(int(time.time()) - 3600),
    }
    if new_configuration:
        for key in (
            "LAPIS_RELAY_HOST",
            "LAPIS_RELAY_PORT",
            "LAPIS_GIT_PUBLIC_URL",
            "LAPIS_AUTH_PUBLIC_URL",
            "LAPIS_COMMUNITY_PUBLIC_URL",
            "LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS",
            "LAPIS_DESKTOP_RENDERER_ALLOWED_ORIGIN",
            "LAPIS_AUTH_WEBAUTHN_RP_ID",
            "LAPIS_AUTH_WEBAUTHN_ORIGINS",
        ):
            values[key] = defaults[key]
        for key, value in list(values.items()):
            if value.startswith(str(source) + "/"):
                values[key] = str(directory) + value[len(str(source)) :]
        for key, filename in SECRET_PATHS.items():
            values[key] = str(directory / filename)
    for key, value in defaults.items():
        values.setdefault(key, value)
    ensure_desktop_browser_origins(values)
    for key, filename in SECRET_PATHS.items():
        values.setdefault(key, str(directory / filename))
    bindings = {
        "LAPIS_RELAY_SECRET": "relay-secret",
        "LAPIS_WALGIT_RELAY_TOKEN": "walgit-relay-token",
        "LAPIS_WALGIT_WEBHOOK_SECRET": "walgit-webhook-secret",
        "LAPIS_S3_ACCESS_KEY_ID": "s3-access-key-id",
        "LAPIS_S3_SECRET_ACCESS_KEY": "s3-secret-access-key",
    }
    for key, filename in bindings.items():
        value = (directory / filename).read_text().strip()
        if key in values and values[key] != value:
            raise RuntimeError(f"{key} does not match its credential file")
        values[key] = value
    operator = (directory / "operator-pubkey").read_text().strip()
    values.setdefault("LAPIS_RELAY_OWNER_PUBKEY", operator)
    if values["LAPIS_RELAY_OWNER_PUBKEY"] not in (
        operator,
        (directory / "relay-pubkey").read_text().strip(),
    ):
        raise RuntimeError(
            "Relay contact pubkey does not match a copied relay or operator identity"
        )
    admins = values.get("LAPIS_RELAY_ADMIN_PUBKEYS", "")
    if operator not in admins:
        values["LAPIS_RELAY_ADMIN_PUBKEYS"] = ",".join(
            filter(None, [admins, f'"{operator}"'])
        )
    values["LAPIS_LOCAL_DEV_SCHEMA"] = "1"
    if new_configuration or values != read_env(directory / "relay.env"):
        save_env(directory / "relay.env", values)
    for path in directory.iterdir():
        if path.is_file() and not path.is_symlink():
            path.chmod(0o600)
    public_configuration(directory, values)
    print(
        f"Prepared {directory}; copied {copied} missing files. Source files were not modified."
    )
    missing = missing_configuration(directory, values, tunnel=False)
    if missing:
        print("Still required: " + ", ".join(missing))


def public_configuration(directory, values):
    keys = json.loads((directory / "fixture-public.json").read_text())
    relay = "wss://" + values["LAPIS_RELAY_HOST"]
    browser = values["LAPIS_COMMUNITY_PUBLIC_URL"]
    public = {
        "VITE_COMMUNITY_PUBLIC_URL": browser,
        "VITE_COMMUNITY_LIVE_RELAY_URL": relay,
        "VITE_COMMUNITY_RELAY_AUTH_URL": values["LAPIS_AUTH_PUBLIC_URL"],
        "VITE_COMMUNITY_KEYTR_RELAYS": relay,
        "VITE_COMMUNITY_KEYTR_GATEWAYS": values["LAPIS_AUTH_WEBAUTHN_RP_ID"]
        + "=Local Community",
        "VITE_REGISTRY_CURATOR_PUBKEYS": keys["curator"],
        "VITE_REGISTRY_PUBLISHER_PUBKEYS": keys["publisher"],
        "VITE_REGISTRY_AUTHORITY_EPOCH": "local-fixture-v1",
    }
    path = directory / "public.env"
    raw_existing = read_env(path)
    existing = {
        key: value
        for key, value in raw_existing.items()
        if key.startswith("VITE_")
        and "BUNKER" not in key
        and "MASTER_KEY" not in key
    }
    merged = {**public, **existing}
    if not path.exists() or merged != raw_existing:
        save_env(path, merged)


def missing_configuration(directory, values, tunnel=True):
    missing = []
    if directory.is_symlink() or directory.stat().st_mode & 0o077:
        raise RuntimeError("Credential directory must be private and not a symlink")
    for path in directory.rglob("*"):
        if path.is_symlink():
            raise RuntimeError(f"Credential symlink is not allowed: {path.name}")
        if path.stat().st_mode & 0o077:
            raise RuntimeError(f"Private permissions required: {path.name}")
    for key, value in values.items():
        if str(Path.home() / ".lapis/relay") in value:
            raise RuntimeError(f"Legacy credential reference is not allowed: {key}")
    required_files = (
        "relay-secret",
        "relay-pubkey",
        "operator-secret",
        "operator-pubkey",
        "github-bridge-pubkey",
        "fixture-secrets.json",
        "fixture-public.json",
        "materializer-reader.key",
        "postgres-password",
        "postgres-url",
        "walgit-relay-token",
        "walgit-webhook-secret",
        "s3-access-key-id",
        "s3-secret-access-key",
    )
    for filename in required_files:
        path = directory / filename
        if not path.is_file() or not path.stat().st_size:
            missing.append(filename)
    for key, filename in SECRET_PATHS.items():
        path = Path(values.get(key, str(directory / filename)))
        if path.parent.resolve() != directory.resolve() or path.is_symlink():
            raise RuntimeError(
                f"{key} must reference a regular file in the canonical credential directory"
            )
        if not path.is_file() or not path.read_bytes().strip():
            missing.append(filename)
        elif path.stat().st_mode & 0o077:
            raise RuntimeError(f"{filename} must have permissions 0600")
    for key in ("LAPIS_AUTH_GITHUB_CLIENT_ID", "LAPIS_AUTH_GOOGLE_CLIENT_ID"):
        if not values.get(key):
            missing.append(key)
    if not (directory / "github-bootstrap.json").is_file():
        missing.append("github-bootstrap.json (local:capture-github)")
    if tunnel and not (directory / "tunnel-credentials.json").is_file():
        missing.append("tunnel-credentials.json (local:tunnel:setup)")
    return missing


def environment(directory):
    for filename in ("relay.env", "postgres-password", "fixture-public.json"):
        if not (directory / filename).is_file():
            raise RuntimeError(f"Missing {filename}; run local:init")
    if not (directory / "relay.env").is_file():
        raise RuntimeError("Run local:init first")
    relay_env = directory / "relay.env"
    values = read_env(relay_env)
    migrated = dict(values)
    ensure_desktop_browser_origins(migrated)
    if migrated != values:
        save_env(relay_env, migrated)
    values = migrated
    values["LAPIS_COMMUNITY_DIR"] = str(directory)
    values["LAPIS_RELAY_PATH"] = str(ROOT)
    values["LAPIS_LOCAL_RELAY_URL"] = "ws://127.0.0.1:" + values.get(
        "LAPIS_RELAY_PORT", "8481"
    )
    values["LAPIS_LOCAL_RELAY_PUBLIC_URL"] = "wss://" + values["LAPIS_RELAY_HOST"]
    values["LAPIS_LOCAL_RELAY_HOST"] = values["LAPIS_RELAY_HOST"]
    values["LAPIS_POSTGRES_PASSWORD"] = (
        (directory / "postgres-password").read_text().strip()
    )
    keys = json.loads((directory / "fixture-public.json").read_text())
    for name, field in (
        ("REGISTRY_CURATOR_PUBKEYS", "curator"),
        ("REGISTRY_PUBLISHER_PUBKEYS", "publisher"),
        ("REGISTRY_OPERATOR_PUBKEYS", "operator"),
    ):
        values[name] = keys[field]
    values["REGISTRY_AUTHORITY_EPOCH"] = "local-fixture-v1"
    head_file = directory / "fixture-heads.json"
    if head_file.exists():
        heads = json.loads(head_file.read_text())
        values["REGISTRY_FIXTURE_HEAD_HISTORY"] = json.dumps(heads["history"])
    # Only configuration from the central directory enters Compose, not stray shell overrides.
    return {**os.environ, **values}


def compose(directory, args, capture=False):
    return invoke(
        [
            "docker",
            "compose",
            "--project-name",
            "lapis-local-dev",
            "--env-file",
            directory / "relay.env",
            "-f",
            ROOT / "docker-compose.local.yml",
            *args,
        ],
        env=environment(directory),
        capture=capture,
    )


STACK_IMAGE_KEYS = (("relay", "LAPIS_RELAY_IMAGE"), ("walgit", "LAPIS_WALGIT_IMAGE"))


def stale_stack_services(directory, env):
    stale = []
    for service, key in STACK_IMAGE_KEYS:
        image = env.get(key)
        container = compose(directory, ["ps", "-aq", service], capture=True).strip()
        if not image or not container:
            continue
        running = invoke(
            ["docker", "inspect", "-f", "{{.Image}}", container], capture=True
        ).strip()
        tagged = invoke(
            ["docker", "image", "inspect", "-f", "{{.Id}}", image], capture=True
        ).strip()
        if running != tagged:
            stale.append(service)
    return stale


def start_stack(directory, env):
    stale = stale_stack_services(directory, env)
    services = ["relay", "cloudflared"]
    if "walgit" in stale:
        services.append("walgit")
    args = ["up", "-d", "--no-build", "--wait"]
    if stale:
        args.append("--force-recreate")
        print(
            "Recreating stale local services onto current tagged images: "
            + ", ".join(stale),
            file=sys.stderr,
        )
    compose(directory, [*args, *services])
    return stale


def wait_url(url, timeout=90, host=None):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            request = urllib.request.Request(
                url,
                headers={
                    "User-Agent": DOCTOR_USER_AGENT,
                    **({} if host is None else {"Host": host}),
                },
            )
            with urllib.request.urlopen(request, timeout=5) as response:
                if response.status == 200:
                    return
        except Exception:
            pass
        time.sleep(1)
    raise RuntimeError("Public service readiness timed out")


def websocket_probe(url, origin):
    parsed = urlparse(url)
    connection = http.client.HTTPSConnection(parsed.hostname, parsed.port or 443, timeout=10)
    key = __import__("base64").b64encode(secrets.token_bytes(16)).decode("ascii")
    connection.request(
        "GET",
        parsed.path or "/",
        headers={
            "Connection": "Upgrade",
            "Upgrade": "websocket",
            "Sec-WebSocket-Key": key,
            "Sec-WebSocket-Version": "13",
            "Origin": origin,
            "User-Agent": DOCTOR_USER_AGENT,
        },
    )
    response = connection.getresponse()
    try:
        if response.status != 101:
            raise RuntimeError("Managed-agent WebSocket upgrade failed")
    finally:
        connection.close()


def capture_github(directory):
    path = directory / "github-bootstrap.json"
    if path.exists():
        print("Existing GitHub bootstrap configuration preserved.")
        return
    query = """SELECT json_build_object('connection',json_build_object('app_id',c.app_id,'installation_id',c.installation_id::bigint,'bridge_pubkey',c.bridge_pubkey), 'owner',i.owner,'repo',i.repo,'external_repository_id',i.external_repository_id,'announcement',json_build_object('id',e.id,'pubkey',e.pubkey,'created_at',e.created_at,'kind',e.kind,'tags',e.tags::json,'content',e.content,'sig',e.sig)) FROM lapis_registry__repository_integrations i JOIN lapis_registry__forge_connections c ON c.id=i.connection_id JOIN lapis_registry__events e ON e.id=i.announcement_id WHERE c.provider='github';"""
    lines = invoke(
        [
            "docker",
            "exec",
            "lapis-github-local-postgres-1",
            "psql",
            "-U",
            "lapis_relay",
            "-d",
            "lapis_relay",
            "-At",
            "-c",
            query,
        ],
        capture=True,
    ).splitlines()
    if len(lines) != 1:
        raise RuntimeError(
            "Expected exactly one existing GitHub binding; prepare github-bootstrap.json explicitly"
        )
    data = json.loads(lines[0])
    data["connection"].update(
        credential_ref="file:github_app_private_key",
        webhook_secret_ref="file:github_webhook_secret",
        bridge_credential_ref="file:github_bridge_secret",
    )
    data["integration"] = {
        "git_authority_backend": "github",
        "content_backend": "github",
        "collaboration_backend": "github",
        "external_repository_id": data.pop("external_repository_id"),
    }
    if data["owner"] != (directory / "operator-pubkey").read_text().strip():
        raise RuntimeError("Copied operator does not own the existing repository")
    private_write(path, json.dumps(data, indent=2) + "\n")
    print(
        "Captured existing GitHub binding without copying history or credentials from its database."
    )


def tunnel_setup(directory, values):
    binary = shutil.which("cloudflared")
    if not binary:
        raise RuntimeError("Install cloudflared, then rerun local:tunnel:setup")
    cert = directory / "cloudflare-cert.pem"
    if not cert.exists():
        # Reuse a certificate by independent copy; never change its original.
        default = Path.home() / ".cloudflared/cert.pem"
        if default.exists():
            private_write(cert, default.read_bytes())
        else:
            invoke(
                [binary, "tunnel", "login"],
                env={**os.environ, "TUNNEL_ORIGIN_CERT": str(cert)},
            )
        if not cert.exists():
            if not default.exists():
                raise RuntimeError("Cloudflare login did not produce a certificate")
            private_write(cert, default.read_bytes())
    cert.chmod(0o600)
    credentials = directory / "tunnel-credentials.json"
    common = [binary, "tunnel", "--origincert", cert]
    if not credentials.exists():
        existing = json.loads(
            invoke([*common, "list", "--output", "json"], capture=True)
        )
        if any(x.get("name") == values["LAPIS_TUNNEL_NAME"] for x in existing):
            raise RuntimeError(
                "Named tunnel already exists; copy its credentials into tunnel-credentials.json"
            )
        invoke(
            [
                *common,
                "create",
                "--credentials-file",
                credentials,
                values["LAPIS_TUNNEL_NAME"],
            ],
            capture=True,
        )
    credentials.chmod(0o600)
    tunnel_id = json.loads(credentials.read_text())["TunnelID"]
    for hostname in (
        values["LAPIS_RELAY_HOST"],
        urlparse(values["LAPIS_COMMUNITY_PUBLIC_URL"]).hostname,
    ):
        invoke([*common, "route", "dns", tunnel_id, hostname], capture=True)
    render_tunnel(directory, values)
    print("Named tunnel routes prepared.")


def render_tunnel(directory, values):
    credentials = directory / "tunnel-credentials.json"
    if not credentials.exists():
        return
    tunnel_id = json.loads(credentials.read_text())["TunnelID"]
    config = {
        "tunnel": tunnel_id,
        "credentials-file": "/run/secrets/tunnel_credentials",
        "ingress": [
            {"hostname": values["LAPIS_RELAY_HOST"], "service": "http://relay:3334"},
            {
                "hostname": urlparse(values["LAPIS_COMMUNITY_PUBLIC_URL"]).hostname,
                "service": "http://host.docker.internal:7036",
            },
            {"service": "http_status:404"},
        ],
    }
    private_write(directory / "tunnel.json", json.dumps(config, indent=2) + "\n")


def build(directory):
    env = environment(directory)
    community = Path(env["LAPIS_COMMUNITY_PATH"])
    invoke(["docker", "build", "-t", env["LAPIS_RELAY_IMAGE"], ROOT])
    invoke(
        [
            "docker",
            "build",
            "-t",
            env["LAPIS_WALGIT_IMAGE"],
            "-f",
            ROOT / "third_party/walgit/Containerfile",
            ROOT,
        ]
    )
    invoke(
        [
            "docker",
            "build",
            "--build-context",
            f"design-core-packages={community.parent / 'design-core/.local/packages'}",
            "-t",
            env["LAPIS_FIXTURE_IMAGE"],
            "-f",
            community / "deploy/fixture.Dockerfile",
            community,
        ]
    )


def prepare_fixture_heads(directory):
    values = read_env(directory / "relay.env")
    path = directory / "fixture-heads.json"
    base = int(values["REGISTRY_FIXTURE_CREATED_AT"])
    heads = (
        json.loads(path.read_text())
        if path.exists()
        else {"created_at": base, "revision": 1}
    )
    history = heads.get("history")
    if history is None:
        if heads["revision"] != 1:
            raise RuntimeError(
                "Fixture head history is missing; refusing to invent prior signed revisions"
            )
        history = [{"created_at": heads["created_at"], "revision": 1}]
    if (
        not history
        or history[0]["created_at"] != base
        or any(point["revision"] != index + 1 for index, point in enumerate(history))
    ):
        raise RuntimeError("Invalid fixture head history")
    if int(time.time()) - history[-1]["created_at"] > 27 * 86400:
        history.append(
            {"created_at": int(time.time()) - 3600, "revision": len(history) + 1}
        )
    heads = {**history[-1], "history": history}
    serialized = json.dumps(heads) + "\n"
    if not path.exists() or path.read_text() != serialized:
        private_write(path, serialized)


def rehydrate(directory):
    prepare_fixture_heads(directory)
    env = environment(directory)
    compose(directory, ["run", "--rm", "--no-deps", "fixture"])
    if not github_matches(directory):
        admin(directory, "github", env)


def github_matches(directory):
    config = json.loads((directory / "github-bootstrap.json").read_text())
    # Read only the operational attachment; no provider tokens leave their encrypted database storage.
    query = "SELECT COALESCE(json_agg(json_build_object('owner',i.owner,'repo',i.repo,'announcement',i.announcement_id,'app_id',c.app_id,'installation',c.installation_id,'authority',i.git_authority_backend,'content',i.content_backend,'collaboration',i.collaboration_backend,'ci',i.ci_backend,'external',i.external_repository_id)), '[]'::json) FROM lapis_registry__repository_integrations i LEFT JOIN lapis_registry__forge_connections c ON c.id=i.connection_id;"
    rows = json.loads(
        compose(
            directory,
            [
                "exec",
                "-T",
                "postgres",
                "psql",
                "-U",
                "lapis_relay",
                "-d",
                "lapis_relay",
                "-At",
                "-c",
                query,
            ],
            capture=True,
        )
    )
    return any(
        row["owner"] == config["owner"]
        and row["repo"] == config["repo"]
        and row["announcement"] == config["announcement"]["id"]
        and str(row["app_id"]) == str(config["connection"]["app_id"])
        and str(row["installation"]) == str(config["connection"]["installation_id"])
        and row["authority"] == row["content"] == row["collaboration"] == "github"
        and not row["ci"]
        and row["external"] == config["integration"]["external_repository_id"]
        for row in rows
    )


def backup_keytr(directory):
    # Quiesce writers before taking one complete database snapshot; no network pagination.
    compose(directory, ["stop", "cloudflared", "relay"])
    try:
        query = "SELECT COALESCE(json_agg(json_build_object('id',id,'pubkey',pubkey,'created_at',created_at,'kind',kind,'tags',tags::json,'content',content,'sig',sig)), '[]'::json) FROM lapis_registry__events WHERE kind=31777;"
        exported = compose(
            directory,
            [
                "exec",
                "-T",
                "postgres",
                "psql",
                "-U",
                "lapis_relay",
                "-d",
                "lapis_relay",
                "-At",
                "-c",
                query,
            ],
            capture=True,
        )
        private_write(directory / "backups/keytr.pending.json", exported)
        admin(directory, "backup", environment(directory))
        (directory / "backups/keytr.pending.json").unlink()
    except Exception:
        compose(directory, ["start", "relay", "cloudflared"])
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "command",
        choices=[
            "init",
            "capture-github",
            "tunnel:setup",
            "build",
            "up",
            "rehydrate",
            "reset",
            "down",
            "doctor",
            "logs",
            "ps",
            "site",
        ],
    )
    parser.add_argument(
        "--directory", type=Path, default=Path.home() / ".lapis/community"
    )
    parser.add_argument("--source", type=Path, default=Path.home() / ".lapis/relay")
    parser.add_argument("--yes", action="store_true")
    args = parser.parse_args()
    directory = args.directory.expanduser().absolute()
    if args.command == "init" and not directory.exists():
        directory.mkdir(parents=True, mode=0o700)
    if directory.exists() and not directory.is_symlink():
        lock_path = directory / "operation.lock"
        descriptor = os.open(lock_path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("Another local environment operation is running")
    if args.command == "init":
        initialize(directory, args.source.expanduser())
        return
    env = environment(directory)
    if args.command == "capture-github":
        capture_github(directory)
        return
    if args.command == "build":
        build(directory)
        return
    if args.command == "tunnel:setup":
        tunnel_setup(directory, env)
        return
    if args.command in ("logs", "ps"):
        compose(directory, [args.command])
        return
    if args.command == "down":
        invoke(["pnpm", "dev:community:stop"], cwd=Path(env["LAPIS_COMMUNITY_PATH"]))
        compose(directory, ["down"])
        return
    missing = missing_configuration(directory, env)
    if missing:
        raise RuntimeError("Missing configuration: " + ", ".join(missing))
    admin(directory, "validate-keys", env)
    public_configuration(directory, env)
    render_tunnel(directory, env)
    for key in ("LAPIS_RELAY_IMAGE", "LAPIS_WALGIT_IMAGE", "LAPIS_FIXTURE_IMAGE"):
        try:
            invoke(["docker", "image", "inspect", env[key]], capture=True)
        except RuntimeError:
            raise RuntimeError(
                f"Local image {env[key]} is unavailable; run local:build"
            ) from None
    if args.command == "doctor":
        compose(directory, ["ps"])
        stale = stale_stack_services(directory, env)
        if stale:
            raise RuntimeError(
                "Running containers must use the current tagged images: "
                + ", ".join(stale)
            )
        wait_url(env["LAPIS_AUTH_PUBLIC_URL"] + "/health", 10)
        config = None
        for origin in browser_origin_probes(env):
            request = urllib.request.Request(
                env["LAPIS_AUTH_PUBLIC_URL"] + "/api/auth/config",
                headers={
                    "Origin": origin,
                    "User-Agent": DOCTOR_USER_AGENT,
                },
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                probed = json.load(response)
                if (
                    response.headers.get("Access-Control-Allow-Origin") != origin
                    or response.headers.get("Access-Control-Allow-Credentials") != "true"
                ):
                    raise RuntimeError("Credentialed CORS configuration is incorrect")
                if origin == env["LAPIS_COMMUNITY_PUBLIC_URL"]:
                    config = probed
            preflight = urllib.request.Request(
                env["LAPIS_AUTH_PUBLIC_URL"] + "/api/auth/nostr/login",
                method="OPTIONS",
                headers={
                    "Origin": origin,
                    "Access-Control-Request-Method": "POST",
                    "Access-Control-Request-Headers": "Authorization, Content-Type",
                    "User-Agent": DOCTOR_USER_AGENT,
                },
            )
            with urllib.request.urlopen(preflight, timeout=10) as response:
                if (
                    response.headers.get("Access-Control-Allow-Origin") != origin
                    or response.headers.get("Access-Control-Allow-Credentials") != "true"
                ):
                    raise RuntimeError("Credentialed auth preflight is incorrect")
        if config is None:
            raise RuntimeError("Community auth configuration probe did not run")
        if set(config.get("providers", [])) != {"google", "github"}:
            raise RuntimeError("Both real OAuth providers must be configured")
        for capability in (
            "nostr_login",
            "managed_signing",
            "nip44",
            "nip46",
            "passkey_registration",
            "github_linking",
            "github_disconnect",
            "custody_transfer",
        ):
            if not config.get("capabilities", {}).get(capability):
                raise RuntimeError(f"Missing auth capability: {capability}")
        relay_info_request = urllib.request.Request(
            env["LAPIS_AGENT_BUNKER_PUBLIC_URL"],
            headers={
                "Accept": "application/nostr+json",
                "User-Agent": DOCTOR_USER_AGENT,
            },
        )
        with urllib.request.urlopen(relay_info_request, timeout=10) as response:
            relay_info = json.load(response)
        managed = relay_info.get("lapis_managed_agents", {})
        if 46 not in relay_info.get("supported_nips", []) or managed.get("version") != 1:
            raise RuntimeError("Managed-agent NIP-11 capability is missing")
        if managed.get("provisioning_url") != env["LAPIS_AGENT_BUNKER_PUBLIC_URL"].rstrip("/") + "/api/agents/managed" or managed.get("bunker_url") != env["LAPIS_AGENT_BUNKER_PUBLIC_URL"].replace("https://", "wss://").rstrip("/") + "/nip46":
            raise RuntimeError("Managed-agent NIP-11 URLs are incorrect")
        for origin in browser_origin_probes(env):
            preflight = urllib.request.Request(
                env["LAPIS_AGENT_BUNKER_PUBLIC_URL"] + "/api/agents/managed",
                method="OPTIONS",
                headers={
                    "Origin": origin,
                    "Access-Control-Request-Method": "POST",
                    "Access-Control-Request-Headers": "Authorization, Content-Type",
                    "User-Agent": DOCTOR_USER_AGENT,
                },
            )
            with urllib.request.urlopen(preflight, timeout=10) as response:
                allowed_headers = response.headers.get("Access-Control-Allow-Headers", "")
                allowed_methods = response.headers.get("Access-Control-Allow-Methods", "")
                if (
                    response.headers.get("Access-Control-Allow-Origin") != origin
                    or not {"authorization", "content-type"}.issubset(
                        {value.strip().lower() for value in allowed_headers.split(",")}
                    )
                    or not {"post", "delete"}.issubset(
                        {value.strip().lower() for value in allowed_methods.split(",")}
                    )
                ):
                    raise RuntimeError("Managed-agent CORS configuration is incorrect")
        denied = urllib.request.Request(
            env["LAPIS_AUTH_PUBLIC_URL"] + "/api/auth/config",
            headers={
                "Origin": "https://unapproved.invalid",
                "User-Agent": DOCTOR_USER_AGENT,
            },
        )
        try:
            with urllib.request.urlopen(denied, timeout=10) as response:
                if response.headers.get("Access-Control-Allow-Origin"):
                    raise RuntimeError(
                        "Unapproved browser origin received CORS permission"
                    )
        except urllib.error.HTTPError as error:
            if error.code != 403:
                raise RuntimeError("Unexpected unapproved-origin response")
        managed_denied = urllib.request.Request(
            env["LAPIS_AGENT_BUNKER_PUBLIC_URL"] + "/api/agents/managed",
            method="OPTIONS",
            headers={
                "Origin": "https://unapproved.invalid",
                "Access-Control-Request-Method": "POST",
                "Access-Control-Request-Headers": "Authorization, Content-Type",
                "User-Agent": DOCTOR_USER_AGENT,
            },
        )
        try:
            urllib.request.urlopen(managed_denied, timeout=10)
            raise RuntimeError("Unapproved origin reached managed-agent provisioning")
        except urllib.error.HTTPError as error:
            if error.code != 403 or error.headers.get("Access-Control-Allow-Origin"):
                raise RuntimeError("Unexpected managed-agent origin response")
        websocket_probe(
            env["LAPIS_AGENT_BUNKER_PUBLIC_URL"].replace("https://", "wss://") + "/nip46",
            env["LAPIS_COMMUNITY_PUBLIC_URL"],
        )
        probe = 'test "$KEYTR_STORAGE_ENABLED" = true && test "$GROUPS_ADMIN_CREATE_ONLY" = true && test "$AGENTS_BUNKER_ENABLED" = true && test -s /run/secrets/lapis_agent_bunker_master_key && test -z "$AUTH_TEST_GITHUB_TOKEN_URL$AUTH_TEST_GOOGLE_ISSUER_URL"'
        compose(directory, ["exec", "-T", "relay", "sh", "-c", probe], capture=True)
        compose(
            directory,
            [
                "run",
                "--rm",
                "--no-deps",
                "fixture",
                "node",
                "dist/fixture/seed.js",
                "--verify",
            ],
        )
        if not github_matches(directory):
            raise RuntimeError(
                "GitHub binding does not match the captured configuration"
            )
        admin(directory, "doctor", env)
        wait_url(env["LAPIS_COMMUNITY_PUBLIC_URL"], 10)
        print(
            "Credentials, images, tunnel routes, auth/CORS, fixture and GitHub readiness verified."
        )
        return
    if args.command == "reset":
        if not args.yes:
            raise RuntimeError(
                "Reset requires --yes; it deletes only lapis-local-dev data volumes"
            )
        backup_keytr(directory)
        compose(directory, ["down", "--volumes"])
    if args.command in ("up", "reset"):
        # Never activate the replacement while the old installation worker runs.
        old = invoke(
            [
                "docker",
                "ps",
                "--filter",
                "label=com.docker.compose.project=lapis-github-local",
                "--filter",
                "label=com.docker.compose.service=relay",
                "--format",
                "{{.ID}}",
            ],
            capture=True,
        ).strip()
        if old:
            invoke(["docker", "stop", *old.splitlines()], capture=True)
        start_stack(directory, env)
        wait_url("http://127.0.0.1:" + env["LAPIS_RELAY_PORT"] + "/health")
        if args.command == "reset":
            admin(directory, "restore", env)
    if args.command in ("up", "reset", "rehydrate"):
        rehydrate(directory)
    if args.command == "site":
        compose(directory, ["--profile", "site", "up", "-d", "registry-site"])
        return
    if args.command in ("up", "reset"):
        if not (directory / "vite.log").exists():
            private_write(directory / "vite.log", "")
        log = open(directory / "vite.log", "a")
        subprocess.Popen(
            ["node", "scripts/community-dev-run.mjs", "--open"],
            cwd=env["LAPIS_COMMUNITY_PATH"],
            env={
                **os.environ,
                "LAPIS_COMMUNITY_DIR": str(directory),
                "COMMUNITY_DEV_REPLACE": "1",
            },
            stdout=log,
            stderr=log,
            start_new_session=True,
        )
        wait_url(
            "http://127.0.0.1:7036",
            host=urlparse(env["LAPIS_COMMUNITY_PUBLIC_URL"]).netloc,
        )
        print("Community ready: " + env["LAPIS_COMMUNITY_PUBLIC_URL"])


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError) as error:
        # No exception repr: subprocess/config objects can contain secret values.
        print(
            (
                "local-dev: " + str(error)
                if isinstance(error, RuntimeError)
                else "local-dev: configuration or filesystem operation failed"
            ),
            file=sys.stderr,
        )
        sys.exit(1)
