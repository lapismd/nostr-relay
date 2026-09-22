import contextlib
from http.server import BaseHTTPRequestHandler, HTTPServer
import importlib.util
import io
from pathlib import Path
import stat
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "local_dev", Path(__file__).with_name("local-dev.py")
)
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)


class CredentialsTest(unittest.TestCase):
    def test_wait_url_rejects_disconnected_tunnel_status(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(530)
                self.end_headers()

            def log_message(self, _format, *_args):
                pass

        server = HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with self.assertRaisesRegex(RuntimeError, "readiness timed out"):
                dev.wait_url(f"http://127.0.0.1:{server.server_port}", timeout=1)
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_wait_url_can_preserve_a_public_virtual_host_over_loopback(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(
                    200 if self.headers["Host"] == "community.example" else 404
                )
                self.end_headers()

            def log_message(self, _format, *_args):
                pass

        server = HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            dev.wait_url(
                f"http://127.0.0.1:{server.server_port}",
                timeout=1,
                host="community.example",
            )
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_render_tunnel_forces_http2_and_keeps_named_routes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            tunnel_id = "00000000-0000-4000-8000-000000000001"
            dev.private_write(
                directory / "tunnel-credentials.json",
                '{"AccountTag":"unused","TunnelID":"%s","TunnelSecret":"unused"}\n'
                % tunnel_id,
            )
            dev.render_tunnel(
                directory,
                {
                    "LAPIS_RELAY_HOST": "local-relay.lapis.md",
                    "LAPIS_COMMUNITY_PUBLIC_URL": "https://local-community.lapis.md",
                },
            )
            config = dev.json.loads((directory / "tunnel.json").read_text())
            self.assertEqual(config["protocol"], "http2")
            self.assertEqual(config["tunnel"], tunnel_id)
            self.assertEqual(
                config["credentials-file"], "/run/secrets/tunnel_credentials"
            )
            self.assertEqual(
                [rule.get("hostname") for rule in config["ingress"]],
                ["local-relay.lapis.md", "local-community.lapis.md", None],
            )
            self.assertEqual(config["ingress"][0]["service"], "http://relay:3334")
            self.assertEqual(
                config["ingress"][1]["service"],
                "http://host.docker.internal:7036",
            )
            self.assertEqual(config["ingress"][2]["service"], "http_status:404")
            self.assertNotIn("unused", (directory / "tunnel.json").read_text())

    def test_wait_public_community_requires_loopback_then_tunnel(self):
        calls = []

        def record(url, timeout=90, host=None):
            calls.append((url, timeout, host))

        with patch.object(dev, "wait_url", side_effect=record):
            dev.wait_public_community(
                {
                    "LAPIS_COMMUNITY_PUBLIC_URL": "https://local-community.lapis.md",
                    "LAPIS_AUTH_PUBLIC_URL": "https://local-relay.lapis.md",
                },
                timeout=15,
            )
        self.assertEqual(
            calls,
            [
                ("http://127.0.0.1:7036", 15, "local-community.lapis.md"),
                ("https://local-community.lapis.md", 15, None),
                ("https://local-relay.lapis.md/health", 15, None),
            ],
        )

    def test_copy_is_private_independent_and_repeatable(self):
        with tempfile.TemporaryDirectory() as temporary:
            source, destination = (
                Path(temporary) / "relay",
                Path(temporary) / "community",
            )
            source.mkdir(mode=0o700)
            dev.admin(source, "init-keys")
            (source / "github-webhook-secret.pem").write_bytes(
                b"preserve\r\noriginal\r\n"
            )
            source.joinpath("relay.env").write_text(
                "LAPIS_RELAY_HOST=old.example\nLAPIS_RELAY_PORT=8381\nLAPIS_GITHUB_APP_KEY_FILE=/old/path/key.pem\n"
            )
            before = {
                p.name: (p.read_bytes(), p.stat().st_mode, p.stat().st_mtime_ns)
                for p in source.iterdir()
                if p.is_file()
            }
            with contextlib.redirect_stdout(io.StringIO()) as output:
                dev.initialize(destination, source)
            self.assertEqual(
                before,
                {
                    p.name: (p.read_bytes(), p.stat().st_mode, p.stat().st_mtime_ns)
                    for p in source.iterdir()
                    if p.is_file()
                },
            )
            for name in (
                "operator-secret",
                "relay-secret",
                "github-bridge-secret",
                "github-webhook-secret.pem",
            ):
                self.assertEqual(
                    source.joinpath(name).read_bytes(),
                    destination.joinpath(name).read_bytes(),
                )
                self.assertNotEqual(
                    source.joinpath(name).stat().st_ino,
                    destination.joinpath(name).stat().st_ino,
                )
                self.assertNotIn(
                    destination.joinpath(name).read_text().strip(), output.getvalue()
                )
            for p in destination.iterdir():
                self.assertEqual(stat.S_IMODE(p.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(destination.stat().st_mode), 0o700)
            env = dev.read_env(destination / "relay.env")
            self.assertEqual(env["LAPIS_RELAY_HOST"], "local-relay.lapis.md")
            self.assertEqual(
                env["LAPIS_GITHUB_APP_KEY_FILE"],
                str(destination / "github-app-private-key.pem"),
            )
            self.assertEqual(
                env["LAPIS_AGENT_BUNKER_PUBLIC_URL"],
                "https://local-relay.lapis.md",
            )
            self.assertEqual(
                env["LAPIS_DESKTOP_RENDERER_ALLOWED_ORIGIN"],
                "http://127.0.0.1:*",
            )
            self.assertEqual(
                env["LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS"],
                '["https://local-community.lapis.md","http://127.0.0.1:*"]',
            )
            self.assertEqual(
                env["LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS"],
                '["https://local-community.lapis.md","http://127.0.0.1:*"]',
            )
            self.assertEqual(
                env["LAPIS_AGENT_BUNKER_MASTER_KEY_FILE"],
                str(destination / "agent-bunker-master-key"),
            )
            bunker_key = destination.joinpath("agent-bunker-master-key").read_bytes()
            env["LAPIS_AUTH_GOOGLE_CLIENT_ID"] = "preserved-client"
            dev.save_env(destination / "relay.env", env)
            with (destination / "relay.env").open("a") as stream:
                stream.write("# Keep operator comments intact\n")
            dev.private_write(
                destination / "public.env",
                "VITE_COMMUNITY_KEYTR_GATEWAYS=custom.example=Custom\n",
            )
            dev.public_configuration(destination, env)
            content = {p.name: p.read_bytes() for p in destination.iterdir()}
            with contextlib.redirect_stdout(io.StringIO()):
                dev.initialize(destination, source)
            self.assertEqual(
                content, {p.name: p.read_bytes() for p in destination.iterdir()}
            )
            self.assertEqual(
                bunker_key,
                destination.joinpath("agent-bunker-master-key").read_bytes(),
            )
            public = dev.read_env(destination / "public.env")
            self.assertTrue(all(k.startswith("VITE_") for k in public))
            self.assertEqual(
                public["VITE_COMMUNITY_KEYTR_GATEWAYS"], "custom.example=Custom"
            )
            self.assertFalse(
                any("BUNKER" in key or "MASTER_KEY" in key for key in public)
            )

    def test_freshness_clock_renews_without_changing_fixture_clock(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            dev.save_env(
                directory / "relay.env", {"REGISTRY_FIXTURE_CREATED_AT": "1000"}
            )
            with patch.object(dev.time, "time", return_value=2000):
                dev.prepare_fixture_heads(directory)
            initial = (directory / "fixture-heads.json").read_bytes()
            with patch.object(dev.time, "time", return_value=3000):
                dev.prepare_fixture_heads(directory)
            self.assertEqual(initial, (directory / "fixture-heads.json").read_bytes())
            with patch.object(dev.time, "time", return_value=30 * 86400):
                dev.prepare_fixture_heads(directory)
            self.assertEqual(
                dev.json.loads((directory / "fixture-heads.json").read_text())[
                    "revision"
                ],
                2,
            )
            self.assertEqual(
                dev.read_env(directory / "relay.env")["REGISTRY_FIXTURE_CREATED_AT"],
                "1000",
            )

    def test_symlink_destination_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "actual"
            target.mkdir()
            link = root / "link"
            link.symlink_to(target)
            with self.assertRaises(RuntimeError):
                dev.initialize(link, root / "source")

    def test_env_is_parsed_without_shell_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "example.env"
            dev.save_env(
                path,
                {
                    "SAFE": "$(touch should-not-exist)",
                    "JSON": '["https://community.example"]',
                },
            )
            self.assertEqual(dev.read_env(path)["SAFE"], "$(touch should-not-exist)")

    def test_environment_migrates_existing_desktop_browser_origins(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            dev.save_env(
                directory / "relay.env",
                {
                    "LAPIS_RELAY_HOST": "local-relay.lapis.md",
                    "LAPIS_RELAY_PORT": "8481",
                    "LAPIS_COMMUNITY_PUBLIC_URL": "https://local-community.lapis.md",
                    "LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS": '["https://local-community.lapis.md"]',
                    "LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS": '["https://local-community.lapis.md"]',
                },
            )
            dev.private_write(directory / "postgres-password", "password\n")
            dev.private_write(
                directory / "fixture-public.json",
                '{"curator":"c","publisher":"p","operator":"o"}\n',
            )
            env = dev.environment(directory)
            self.assertEqual(
                env["LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS"],
                '["https://local-community.lapis.md","http://127.0.0.1:*"]',
            )
            self.assertEqual(
                dev.read_env(directory / "relay.env")[
                    "LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS"
                ],
                '["https://local-community.lapis.md","http://127.0.0.1:*"]',
            )

    def test_public_configuration_discards_bunker_and_master_key_values(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            dev.private_write(
                directory / "fixture-public.json",
                '{"curator":"c","publisher":"p"}\n',
            )
            dev.private_write(
                directory / "public.env",
                "VITE_CUSTOM=preserved\nVITE_AGENT_BUNKER_SECRET=remove\nMASTER_KEY=remove\n",
            )
            dev.public_configuration(
                directory,
                {
                    "LAPIS_RELAY_HOST": "relay.example",
                    "LAPIS_COMMUNITY_PUBLIC_URL": "https://community.example",
                    "LAPIS_AUTH_PUBLIC_URL": "https://relay.example",
                    "LAPIS_AUTH_WEBAUTHN_RP_ID": "community.example",
                },
            )
            with (directory / "public.env").open("a") as stream:
                stream.write("VITE_AGENT_BUNKER_SECRET=remove-again\n")
            dev.public_configuration(
                directory,
                {
                    "LAPIS_RELAY_HOST": "relay.example",
                    "LAPIS_COMMUNITY_PUBLIC_URL": "https://community.example",
                    "LAPIS_AUTH_PUBLIC_URL": "https://relay.example",
                    "LAPIS_AUTH_WEBAUTHN_RP_ID": "community.example",
                },
            )
            public = dev.read_env(directory / "public.env")
            self.assertEqual(public["VITE_CUSTOM"], "preserved")
            self.assertFalse(any("BUNKER" in key or "MASTER_KEY" in key for key in public))

    def test_local_compose_mounts_bunker_key_without_rendering_its_value(self):
        if not __import__("shutil").which("docker"):
            self.skipTest("Docker is unavailable")
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "community"
            with contextlib.redirect_stdout(io.StringIO()):
                dev.initialize(directory, Path(temporary) / "absent-source")
            rendered = dev.compose(
                directory, ["config", "--format", "json"], capture=True
            )
            data = dev.json.loads(rendered)
            relay = data["services"]["relay"]
            self.assertEqual(relay["environment"]["AGENTS_BUNKER_ENABLED"], "true")
            self.assertIn(
                "lapis_agent_bunker_master_key",
                [secret["source"] for secret in relay["secrets"]],
            )
            self.assertEqual(
                data["secrets"]["lapis_agent_bunker_master_key"]["file"],
                str(directory / "agent-bunker-master-key"),
            )
            secret = (directory / "agent-bunker-master-key").read_text().strip()
            if secret in rendered:
                self.fail("Rendered Compose configuration exposed the bunker key")
            self.assertEqual(
                data["services"]["cloudflared"]["command"],
                [
                    "tunnel",
                    "--no-autoupdate",
                    "--protocol",
                    "http2",
                    "--config",
                    "/etc/cloudflared/config.json",
                    "run",
                ],
            )

    def test_entrypoint_adds_bunker_to_existing_generated_configuration_once(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            config = directory / "config"
            media = directory / "media"
            config.mkdir()
            path = config / "relay.example"
            path.write_text("# Auto-generated Zooid configuration\nhost = \"relay.example\"\n")
            env = {
                **dev.os.environ,
                "CONFIG": str(config),
                "MEDIA": str(media),
                "DATABASE_URL": "postgres://unused",
                "RELAY_HOST": "relay.example",
                "AGENTS_BUNKER_ENABLED": "true",
                "AGENTS_BUNKER_PUBLIC_URL": "https://relay.example",
                "AGENTS_BUNKER_ALLOWED_BROWSER_ORIGINS": '["https://community.example"]',
                "AGENTS_BUNKER_MASTER_KEY_REF": "file:lapis_agent_bunker_master_key",
                "LAPIS_PROCESS_BIN": __import__("shutil").which("true"),
            }
            for _ in range(2):
                subprocess.run(
                    [str(dev.ROOT / "docker-entrypoint.sh")],
                    env=env,
                    check=True,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            content = path.read_text()
            self.assertEqual(content.count("[agents.bunker]"), 1)
            self.assertIn('master_key_ref = "file:lapis_agent_bunker_master_key"', content)


class StackImageTest(unittest.TestCase):
    def test_stale_stack_services_detects_moved_tag(self):
        containers = {"relay": "relaycid", "walgit": "walgitcid"}
        inspect_ids = {
            "relaycid": "sha256:old-relay",
            "walgitcid": "sha256:current-walgit",
            "lapis-relay:community-local": "sha256:new-relay",
            "lapis-walgit:local": "sha256:current-walgit",
        }

        def compose(_directory, args, **kwargs):
            self.assertEqual(args[:2], ["ps", "-aq"])
            return containers[args[2]] + "\n"

        def invoke(args, **kwargs):
            if args[:3] == ["docker", "inspect", "-f"] or args[:4] == [
                "docker",
                "image",
                "inspect",
                "-f",
            ]:
                return inspect_ids[args[-1]] + "\n"
            raise AssertionError(args)

        with (
            patch.object(dev, "compose", side_effect=compose),
            patch.object(dev, "invoke", side_effect=invoke),
        ):
            stale = dev.stale_stack_services(
                Path("/tmp/community"),
                {
                    "LAPIS_RELAY_IMAGE": "lapis-relay:community-local",
                    "LAPIS_WALGIT_IMAGE": "lapis-walgit:local",
                },
            )
        self.assertEqual(stale, ["relay"])

    def test_stale_stack_services_skips_absent_containers(self):
        with (
            patch.object(dev, "compose", return_value=""),
            patch.object(dev, "invoke") as invoke,
        ):
            stale = dev.stale_stack_services(
                Path("/tmp/community"),
                {
                    "LAPIS_RELAY_IMAGE": "lapis-relay:community-local",
                    "LAPIS_WALGIT_IMAGE": "lapis-walgit:local",
                },
            )
        self.assertEqual(stale, [])
        invoke.assert_not_called()

    def test_start_stack_recreates_stale_services(self):
        with (
            patch.object(dev, "stale_stack_services", return_value=["relay", "walgit"]),
            patch.object(dev, "compose") as compose,
        ):
            stale = dev.start_stack(Path("/tmp/community"), {})
        self.assertEqual(stale, ["relay", "walgit"])
        compose.assert_called_once_with(
            Path("/tmp/community"),
            [
                "up",
                "-d",
                "--no-build",
                "--wait",
                "--force-recreate",
                "relay",
                "cloudflared",
                "walgit",
            ],
        )

    def test_start_stack_leaves_current_images_in_place(self):
        with (
            patch.object(dev, "stale_stack_services", return_value=[]),
            patch.object(dev, "compose") as compose,
        ):
            stale = dev.start_stack(Path("/tmp/community"), {})
        self.assertEqual(stale, [])
        compose.assert_called_once_with(
            Path("/tmp/community"),
            ["up", "-d", "--no-build", "--wait", "relay", "cloudflared"],
        )


if __name__ == "__main__":
    unittest.main()
