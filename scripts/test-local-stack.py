#!/usr/bin/env python3
"""Exercise the canonical topology and static fixture in a disposable project.

OAuth and GitHub are explicitly disabled only in this test overlay. The normal
local:up command never disables required providers or substitutes mock services.
"""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time

spec = importlib.util.spec_from_file_location(
    "local_dev", Path(__file__).with_name("local-dev.py")
)
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)

with tempfile.TemporaryDirectory(prefix="lapis-unified-acceptance-") as temporary:
    directory = Path(temporary)
    with contextlib.redirect_stdout(io.StringIO()):
        dev.initialize(directory, directory / "absent-legacy-source")
    for name in (
        "github-app-private-key.pem",
        "github-webhook-secret.pem",
        "github-oauth-client-secret",
        "google-oauth-client-secret",
    ):
        dev.private_write(directory / name, "unused-isolated-test-credential\n")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    # Keep fixture traffic on Docker networking, like the real tunnel route.
    # Only the host-side health and recovery tools use the published port.
    origin = "http://registry.localhost:3334"
    values = dev.read_env(directory / "relay.env")
    values["REGISTRY_FIXTURE_CREATED_AT"] = str(int(time.time()) - 28 * 86400)
    values.update(
        LAPIS_RELAY_PORT=str(port),
        LAPIS_RELAY_HOST="registry.localhost:3334",
        LAPIS_GIT_PUBLIC_URL=origin,
        LAPIS_AUTH_PUBLIC_URL=origin,
    )
    dev.save_env(directory / "relay.env", values)
    dev.prepare_fixture_heads(directory)
    override = directory / "test.compose.json"
    dev.private_write(
        override,
        json.dumps(
            {
                "services": {
                    "relay": {
                        "networks": {"default": {"aliases": ["registry.localhost"]}},
                        "environment": {
                            "AUTH_ENABLED": "false",
                            "GITHUB_ENABLED": "false",
                        },
                    },
                    "fixture": {
                        "environment": {
                            "REGISTRY_RELAY": origin.replace("http:", "ws:")
                        },
                    },
                }
            }
        ),
    )
    command = [
        "docker",
        "compose",
        "--project-name",
        f"lapis-unified-acceptance-{os.getpid()}",
        "--env-file",
        str(directory / "relay.env"),
        "-f",
        str(dev.ROOT / "docker-compose.local.yml"),
        "-f",
        str(override),
    ]

    def compose(*args, capture=False):
        result = subprocess.run(
            [*command, *args],
            env=dev.environment(directory),
            cwd=dev.ROOT,
            text=True,
            capture_output=capture,
        )
        if result.returncode:
            if capture:
                # This isolated test never loads operator or provider credentials.
                print(result.stderr)
                print(result.stdout)
            raise RuntimeError("Isolated Compose operation failed")
        return result.stdout if capture else ""

    role_contract = """
import { readFile } from 'node:fs/promises';
import { finalizeEvent, getPublicKey } from 'nostr-tools/pure';
import { WebSocketRegistryRelay } from './dist/nostr/index.js';
const keys=JSON.parse(await readFile(process.env.REGISTRY_FIXTURE_SECRETS_FILE,'utf8')).keys;
const secret=Buffer.from(keys.operator,'hex');
const curator=getPublicKey(Buffer.from(keys.curator,'hex'));
const relay=new WebSocketRegistryRelay(process.env.REGISTRY_RELAY,{authenticationRelayUrl:process.env.REGISTRY_RELAY,connectTimeoutMs:15000,eoseTimeoutMs:15000});
await relay.authenticate(async template=>finalizeEvent(template,secret));
const group='d792e601-8c97-5ac6-9446-d8215835de3c';
const state=await relay.query({kinds:[39002],'#d':[group]});
const roles=state.flatMap(event=>event.tags.filter(tag=>tag[0]==='p'&&tag[1]===curator).flatMap(tag=>tag.slice(2)));
if(process.argv[1]==='grant') {
 await relay.publish(finalizeEvent({kind:9000,created_at:Math.floor(Date.now()/1000),content:'',tags:[['h',group],['p',curator,...new Set([...roles,'test-extra-role'])]]},secret));
} else if(!roles.includes('test-extra-role')) {throw new Error('Rehydration removed an existing role');}
relay.close();
"""
    keytr_contract = """
import { readFile } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import { finalizeEvent } from 'nostr-tools/pure';
import { WebSocketRegistryRelay } from './dist/nostr/index.js';
const keys=JSON.parse(await readFile(process.env.REGISTRY_FIXTURE_SECRETS_FILE,'utf8')).keys;
const relay=new WebSocketRegistryRelay(process.env.REGISTRY_RELAY,{authenticationRelayUrl:process.env.REGISTRY_RELAY,connectTimeoutMs:15000,eoseTimeoutMs:15000});
const event=finalizeEvent({kind:31777,created_at:Math.floor(Date.now()/1000),content:randomBytes(60).toString('base64'),tags:[['d','Y3JlZGVudGlhbA'],['rp','community.example'],['algo','aes-256-gcm'],['kdf','hkdf-sha256'],['v','1']]},Buffer.from(keys.communityTwo,'hex'));
await relay.publish(event);
relay.close();
"""
    try:
        compose("up", "-d", "--no-build", "--wait", "relay")
        first = json.loads(
            compose("run", "--rm", "--no-deps", "fixture", capture=True)
            .strip()
            .splitlines()[-1]
        )
        compose(
            "run",
            "--rm",
            "--no-deps",
            "fixture",
            "node",
            "--input-type=module",
            "-e",
            role_contract,
            "grant",
            capture=True,
        )
        second = json.loads(
            compose("run", "--rm", "--no-deps", "fixture", capture=True)
            .strip()
            .splitlines()[-1]
        )
        assert first == second, "Repeated hydration changed the baseline"
        compose(
            "run",
            "--rm",
            "--no-deps",
            "fixture",
            "node",
            "--input-type=module",
            "-e",
            role_contract,
            "verify",
            capture=True,
        )
        assert first["pluginCount"] == 13 and first["projectCount"] == 1
        compose("restart", "relay")
        dev.wait_url(f"http://127.0.0.1:{port}/health")
        verified = json.loads(
            compose(
                "run",
                "--rm",
                "--no-deps",
                "fixture",
                "node",
                "dist/fixture/seed.js",
                "--verify",
                capture=True,
            )
            .strip()
            .splitlines()[-1]
        )
        assert verified == first, "Restart lost baseline data"
        compose(
            "run",
            "--rm",
            "--no-deps",
            "fixture",
            "node",
            "--input-type=module",
            "-e",
            keytr_contract,
            capture=True,
        )
        # Exercise the production export/verification function against this isolated project.
        dev.compose = lambda _directory, args, capture=False: compose(
            *args, capture=capture
        )
        dev.backup_keytr(directory)
        before = json.loads((directory / "backups/keytr.json").read_text())
        assert len(before) == 1, "Keytr export missed a signed record"
        compose("down", "--volumes")
        compose("up", "-d", "--no-build", "--wait", "relay")
        dev.admin(
            directory,
            "restore",
            {
                **dev.environment(directory),
                "LAPIS_LOCAL_RELAY_URL": f"ws://127.0.0.1:{port}",
                "LAPIS_LOCAL_RELAY_HOST": "registry.localhost:3334",
            },
        )
        assert (
            directory / "backups/keytr.json"
        ).exists(), "Reset removed recovery backups"
        restored = json.loads(
            compose("run", "--rm", "--no-deps", "fixture", capture=True)
            .strip()
            .splitlines()[-1]
        )
        assert restored == first, "Reset changed baseline fixture identity"
        print(
            "Unified Docker topology: 13 plugins, repeated hydration, additive roles, persistence, Keytr reset and verification passed."
        )
    except Exception:
        compose("logs", "--tail", "25", "relay")
        raise
    finally:
        compose("down", "--volumes", "--remove-orphans")
