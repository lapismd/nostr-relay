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


if __name__ == "__main__":
    unittest.main()
