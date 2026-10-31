"""r266 offline tests: one release version shared by the panel and the proxy dll.

Run from the repo root:  python -m unittest replay.test_version_sync -v
"""
import re
import subprocess
import sys
import unittest

import preview_config as pc


class VersionSync(unittest.TestCase):
    def test_version_h_matches(self):
        text = (pc.APP_ROOT / 'rome_proxy' / 'version.h').read_text(encoding='utf-8')
        found = re.findall(r'^\s*#define\s+BC2_VERSION_STRING\s+"([^"]*)"', text, re.M)
        self.assertEqual(found, [pc.VERSION])

    def test_preview_version_flag(self):
        out = subprocess.run([sys.executable, 'preview.py', '--version'], cwd=pc.APP_ROOT, capture_output=True,
                             text=True, timeout=60)
        self.assertEqual((out.returncode, out.stdout, out.stderr), (0, pc.VERSION + '\n', ''))


if __name__ == '__main__':
    unittest.main()
