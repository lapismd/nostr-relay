#!/usr/bin/env python3
"""Relay entrypoint coverage that stays with the relay image."""

from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class EntrypointTest(unittest.TestCase):
    def test_entrypoint_adds_bunker_to_existing_generated_configuration_once(self):
        binary = shutil.which("true")
        self.assertIsNotNone(binary)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            config = directory / "config"
            media = directory / "media"
            config.mkdir()
            path = config / "relay.example"
            path.write_text(
                '# Auto-generated Zooid configuration\nhost = "relay.example"\n'
            )
            env = {
                **os.environ,
                "CONFIG": str(config),
                "MEDIA": str(media),
                "DATABASE_URL": "postgres://unused",
                "RELAY_HOST": "relay.example",
                "AGENTS_BUNKER_ENABLED": "true",
                "AGENTS_BUNKER_PUBLIC_URL": "https://relay.example",
                "AGENTS_BUNKER_ALLOWED_BROWSER_ORIGINS": '["https://community.example"]',
                "AGENTS_BUNKER_MASTER_KEY_REF": "file:lapis_agent_bunker_master_key",
                "LAPIS_PROCESS_BIN": binary,
            }
            for _ in range(2):
                subprocess.run(
                    [str(ROOT / "docker-entrypoint.sh")],
                    env=env,
                    check=True,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            content = path.read_text()
            self.assertEqual(content.count("[agents.bunker]"), 1)
            self.assertIn(
                'master_key_ref = "file:lapis_agent_bunker_master_key"', content
            )

    def test_incomplete_oauth_pair_is_disabled_and_complete_pair_is_kept(self):
        binary = shutil.which("true")
        self.assertIsNotNone(binary)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            config = directory / "config"
            media = directory / "media"
            config.mkdir()
            path = config / "relay.example"
            env = {
                **os.environ,
                "CONFIG": str(config),
                "MEDIA": str(media),
                "DATABASE_URL": "postgres://unused",
                "RELAY_HOST": "relay.example",
                "RELAY_SECRET": "relay-secret",
                "AUTH_ENABLED": "true",
                "AUTH_GITHUB_CLIENT_ID": "",
                "AUTH_GITHUB_CLIENT_SECRET_REF": "file:lapis_auth_github_client_secret",
                "AUTH_GOOGLE_CLIENT_ID": "google-client",
                "AUTH_GOOGLE_CLIENT_SECRET_REF": "file:lapis_auth_google_client_secret",
                "LAPIS_PROCESS_BIN": binary,
            }
            completed = subprocess.run(
                [str(ROOT / "docker-entrypoint.sh")],
                env=env,
                check=True,
                capture_output=True,
                text=True,
            )
            content = path.read_text()
            self.assertIn('client_id = ""\nclient_secret_ref = ""', content)
            self.assertIn('client_id = "google-client"', content)
            self.assertIn(
                'client_secret_ref = "file:lapis_auth_google_client_secret"',
                content,
            )
            self.assertIn("GitHub OAuth disabled", completed.stdout)
            self.assertNotIn("Google OAuth disabled", completed.stdout)

            existing = config / "relay.example"
            existing.write_text(
                '# Auto-generated Zooid configuration\nhost = "relay.example"\n'
            )
            appended = subprocess.run(
                [str(ROOT / "docker-entrypoint.sh")],
                env=env,
                check=True,
                capture_output=True,
                text=True,
            )
            appended_content = existing.read_text()
            github = appended_content.split("[auth.github]", 1)[1].split("[auth.google]", 1)[0]
            google = appended_content.split("[auth.google]", 1)[1].split("[auth.testing]", 1)[0]
            self.assertIn('client_id = ""', github)
            self.assertIn('client_secret_ref = ""', github)
            self.assertIn('client_id = "google-client"', google)
            self.assertIn(
                'client_secret_ref = "file:lapis_auth_google_client_secret"',
                google,
            )
            self.assertIn("GitHub OAuth disabled", appended.stdout)


if __name__ == "__main__":
    unittest.main()
