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
        LAPIS_RELAY_HOST="registry.localhost",
        LAPIS_GIT_PUBLIC_URL=origin,
        LAPIS_AUTH_PUBLIC_URL=origin,
        LAPIS_AGENT_BUNKER_PUBLIC_URL=origin,
        LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS='["http://community.localhost:7036"]',
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
                        "environment": {
                            "AUTH_ENABLED": "false",
                            "GITHUB_ENABLED": "false",
                        },
                    },
                    "fixture": {
                        "environment": {
                            "REGISTRY_RELAY": origin.replace("http:", "ws:"),
                            "MANAGED_AGENT_API": origin
                            + "/api/agents/managed",
                            "MANAGED_AGENT_BUNKER_RELAY": origin.replace(
                                "http:", "ws:"
                            )
                            + "/nip46",
                            "MANAGED_AGENT_CREATED_AT": str(int(time.time())),
                            "MANAGED_AGENT_ORIGIN": "http://community.localhost:7036",
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
    managed_agent_contract = """
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import WebSocket from 'ws';
import { BunkerSigner, parseBunkerInput } from 'nostr-tools/nip46';
import { finalizeEvent, getPublicKey, verifyEvent } from 'nostr-tools/pure';
globalThis.WebSocket = WebSocket;
const keys=JSON.parse(await readFile(process.env.REGISTRY_FIXTURE_SECRETS_FILE,'utf8')).keys;
const ownerSecret=Buffer.from(keys.operator,'hex');
const clientSecret=Buffer.from(keys.communityTwo,'hex');
const ownerPubkey=getPublicKey(ownerSecret);
const clientPubkey=getPublicKey(clientSecret);
const phase=process.argv[1];
const expected=process.argv[2] ?? '';
const api=process.env.MANAGED_AGENT_API;
const requestOrigin=process.env.MANAGED_AGENT_ORIGIN;
async function managedRequest(url,method,input) {
 const body=input===undefined?'':JSON.stringify(input);
 const tags=[['u',url],['method',method],['nonce',crypto.randomUUID()]];
 if(method!=='GET') tags.push(['payload',createHash('sha256').update(body).digest('hex')]);
 const auth=finalizeEvent({kind:27235,created_at:Math.floor(Date.now()/1000),tags,content:''},ownerSecret);
 const response=await fetch(url,{method,headers:{Authorization:`Nostr ${Buffer.from(JSON.stringify(auth)).toString('base64')}`,Origin:requestOrigin,...(input===undefined?{}:{'Content-Type':'application/json'})},...(method==='GET'?{}:{body})});
 if(!response.ok) throw new Error(`managed request failed (${response.status})`);
 return response.json();
}
let bunkerUrl;
let agentPubkey;
if(phase==='bootstrap') {
 const created=await managedRequest(api,'POST',{client_pubkey:clientPubkey,profile_content:JSON.stringify({name:'Disposable managed agent'}),profile_created_at:Number(process.env.MANAGED_AGENT_CREATED_AT),idempotency_key:'disposable-managed-agent'});
 bunkerUrl=created.bunker_url;
 agentPubkey=created.agent.pubkey;
 const pointer=await parseBunkerInput(bunkerUrl);
 if(!pointer) throw new Error('invalid bunker pointer');
 const signer=BunkerSigner.fromBunker(clientSecret,pointer);
 try {
  await signer.connect({name:'Lapis Nostr Agent Controller'});
  if(await signer.getPublicKey()!==agentPubkey) throw new Error('managed signer pubkey mismatch');
  const profile=await signer.signEvent(created.profile_template);
  if(!verifyEvent(profile)||profile.pubkey!==agentPubkey) throw new Error('invalid managed profile signature');
  const ownerEvent=finalizeEvent({kind:30177,created_at:Math.floor(Date.now()/1000),tags:[['d',agentPubkey]],content:JSON.stringify({persona_id:'disposable',name_mode:'instance',name:'Disposable managed agent',respond_to:'members',parallelism:1})},ownerSecret);
  await managedRequest(`${api}/${agentPubkey}/activate`,'POST',{owner_event:ownerEvent,profile_event:profile});
  const proof=await signer.signEvent({kind:22242,created_at:Math.floor(Date.now()/1000),tags:[['relay',new URL(api).origin],['challenge','bootstrap']],content:''});
  if(!verifyEvent(proof)||proof.pubkey!==agentPubkey) throw new Error('managed signer activation failed');
 } finally { await signer.close(); }
} else if(phase==='reconnect') {
 agentPubkey=expected;
 const bunkerUrl=`bunker://${agentPubkey}?relay=${encodeURIComponent(process.env.MANAGED_AGENT_BUNKER_RELAY)}`;
 const pointer=await parseBunkerInput(bunkerUrl);
 if(!pointer) throw new Error('invalid reconnect pointer');
 const signer=BunkerSigner.fromBunker(clientSecret,pointer);
 try {
  await signer.connect({name:'Lapis Nostr Agent Controller'});
  if(await signer.getPublicKey()!==agentPubkey) throw new Error('managed reconnect changed pubkey');
  const proof=await signer.signEvent({kind:22242,created_at:Math.floor(Date.now()/1000),tags:[['relay',new URL(api).origin],['challenge','restart']],content:''});
  if(!verifyEvent(proof)||proof.pubkey!==agentPubkey) throw new Error('managed restart signing failed');
 } finally { await signer.close(); }
} else { throw new Error('unknown managed-agent phase'); }
if(ownerPubkey===agentPubkey||clientPubkey===agentPubkey) throw new Error('managed identities are not distinct');
console.log(agentPubkey);
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
        managed_pubkey = (
            compose(
                "run",
                "--rm",
                "--no-deps",
                "fixture",
                "node",
                "--input-type=module",
                "-e",
                managed_agent_contract,
                "bootstrap",
                capture=True,
            )
            .strip()
            .splitlines()[-1]
        )
        assert len(managed_pubkey) == 64
        compose("restart", "relay")
        dev.wait_url(f"http://127.0.0.1:{port}/health")
        reconnected_pubkey = (
            compose(
                "run",
                "--rm",
                "--no-deps",
                "fixture",
                "node",
                "--input-type=module",
                "-e",
                managed_agent_contract,
                "reconnect",
                managed_pubkey,
                capture=True,
            )
            .strip()
            .splitlines()[-1]
        )
        assert reconnected_pubkey == managed_pubkey, "Restart changed managed agent identity"
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
                "LAPIS_LOCAL_RELAY_PUBLIC_URL": "ws://registry.localhost:3334",
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
            "Unified Docker topology: 13 plugins, repeated hydration, additive roles, managed-agent restart reconnect, persistence, Keytr reset and verification passed."
        )
    except Exception:
        compose("logs", "--tail", "25", "relay")
        raise
    finally:
        compose("down", "--volumes", "--remove-orphans")
