import contextlib
from http.server import BaseHTTPRequestHandler, HTTPServer
import importlib.util
import io
from pathlib import Path
import stat
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
            public = dev.read_env(destination / "public.env")
            self.assertTrue(all(k.startswith("VITE_") for k in public))
            self.assertEqual(
                public["VITE_COMMUNITY_KEYTR_GATEWAYS"], "custom.example=Custom"
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


if __name__ == "__main__":
    unittest.main()
