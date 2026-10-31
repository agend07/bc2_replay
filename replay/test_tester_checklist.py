"""Offline test: every verifiable text quoted in docs/tester_checklist.md must exist in the UI/DLL sources.

Convention: a checklist line containing 'Expected:' lists backticked spans after the marker; each span
must occur verbatim in the concatenated source text below.

Run from the repo root:  python -m unittest replay.test_tester_checklist -v
"""
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHECKLIST = ROOT / 'docs' / 'tester_checklist.md'
MARKER = 'Expected:'
SOURCES = ['replay/first_run.py', 'replay/ui.py', 'replay/ui_logic.py', 'replay/recordings.py',
           'replay/client_install.py', 'launcher_panel.py', 'preview.py']


def extract_expected(text):
    """Return [(line_number, span)] for backticked spans after the 'Expected:' marker on each line."""
    out = []
    for n, line in enumerate(text.splitlines(), 1):
        head, marker, tail = line.partition(MARKER)
        if marker:
            out.extend((n, s) for s in re.findall(r'`([^`]+)`', tail))
    return out


def source_text():
    files = [ROOT / p for p in SOURCES] + sorted((ROOT / 'rome_proxy').glob('*.c'))
    return '\n'.join(f.read_text(encoding='utf-8', errors='replace') for f in files if f.is_file())


def load_spans():
    if not CHECKLIST.is_file():
        raise AssertionError('docs/tester_checklist.md missing')
    return extract_expected(CHECKLIST.read_text(encoding='utf-8'))


class TesterChecklist(unittest.TestCase):
    def test_checklist_has_spans(self):
        self.assertGreaterEqual(len(load_spans()), 10)

    def test_every_span_in_sources(self):
        src = source_text()
        missing = ['line %d: %r' % (n, s) for n, s in load_spans() if s not in src]
        self.assertEqual(missing, [], 'expected texts not found in UI/DLL sources:\n' + '\n'.join(missing))

    def test_extractor(self):
        text = '`before` Expected: `a b` and `c`\nno marker `x`\n  - Expected: `d`\nExpected: none\n'
        self.assertEqual(extract_expected(text), [(1, 'a b'), (1, 'c'), (3, 'd')])


if __name__ == '__main__':
    unittest.main()
