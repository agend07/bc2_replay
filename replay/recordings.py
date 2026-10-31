"""Step 0: the 'Recordings' window - list client captures and build/publish a server demo from one.

The builder does the work: the release zip's <app root>\\builder\\builder.exe (the Go builder; auto-detected, or
preview.json "pov_builder": {"exe": ...}) (pov.resolve_builder, child env from pov.builder_env; optional
"capture_roots": [..] are passed as --root):
  builder.exe list [--root DIR]... --json   -> JSON array, one object per capture dir
  builder.exe build <dir> --publish [--round K]
      prints 'STEP i/n ...', free lines, 'ESTIMATE <seconds>', 'PROGRESS <phase> <i>/<k>' (live, per builder phase),
      'DEMO <stem>' (published) and 'RESULT: OK|FAIL ...'; exit 0 only on OK.
  builder.exe extract <file.bc2raw>
      raw capture of the recorder DLL (list entry kind "raw") -> capture dir; 'STEP ...', 'RESULT: OK <dir>|FAIL ...'.
      The window extracts settled raws itself (raws_to_extract), one at a time, never during a build.
  r287: a capture item has "rounds" [{round, lo, hi, packets, seconds, level}] and "default_round"; a build entry
      may be {"stem", "round"} (round null = built before r287 = the default round). The window lists one row per
      non-empty round (round_rows) and builds with --round K.
  builder.exe prune [--root DIR]... [--keep N] [--extracted] [--dry-run]
      deletes old raw files; per file 'DELETE|WOULD-DELETE|SKIP|KEEP <name> <bytes> <reason>', then 'RESULT: ...'.

Pure module (no tkinter / ctypes / config imports) so it can be tested offline. Never build while the panel runs a
replay server or BC2 client (busy_reason), one build at a time (the window and the builder's lock both enforce it).
"""
import fnmatch
import json
import os
import subprocess
from pathlib import Path

from replay import pov

LIST_TIMEOUT_S = 120
PRIVACY_NOTE = 'Captures contain every player name and chat; conversion never emits chat.'
NOWINDOW = getattr(subprocess, 'CREATE_NO_WINDOW', 0)


def tool(cfg=None, app_root=None):
    """([builder.exe], repo); RuntimeError (pov.builder_status text when nothing is found) otherwise."""
    r = pov.resolve_builder(cfg, app_root)
    if r['repo'] is None:
        raise RuntimeError(pov.builder_status(cfg, app_root)[1])
    if not r['exe'].is_file():
        raise RuntimeError(f'Builder not found: {r["exe"]} (expected builder\\builder.exe next to the panel exe, '
                           'or preview.json "pov_builder": {"exe": ...})')
    return [str(r['exe'])], r['repo']


def root_args(cfg=None):
    out = []
    for root in (cfg or {}).get('capture_roots') or []:
        if isinstance(root, str) and root.strip():
            out += ['--root', root]
    return out


def list_command(cfg=None, app_root=None):
    prefix, repo = tool(cfg, app_root)
    return prefix + ['list', '--json'] + root_args(cfg), repo


def list_captures(cfg=None, run=subprocess.run, app_root=None):
    """Captures reported by 'builder.exe list --json' (read-only: the builder only reads files)."""
    cmd, repo = list_command(cfg, app_root)
    res = run(cmd, cwd=str(repo), stdout=subprocess.PIPE, stderr=subprocess.PIPE, stdin=subprocess.DEVNULL,
              text=True, encoding='utf-8', errors='replace', timeout=LIST_TIMEOUT_S, creationflags=NOWINDOW,
              env=pov.builder_env(cfg, app_root))
    if res.returncode != 0:
        tail = (res.stderr or res.stdout or '').strip().splitlines()[-1:] or ['']
        raise RuntimeError(f'capture list failed (rc={res.returncode}): {tail[0]}')
    try:
        data = json.loads(res.stdout)
    except ValueError as exc:
        raise RuntimeError(f'capture list is not JSON: {exc}')
    if not isinstance(data, list):
        raise RuntimeError('capture list is not a JSON array')
    return [item for item in data if isinstance(item, dict)]


def build_command(cfg, capture_dir, app_root=None, round_k=None):
    """[cmd], repo of a publish build. round_k: 1-based round (r287 --round K); None = the builder's default round."""
    prefix, repo = tool(cfg, app_root)
    cmd = prefix + ['build', str(capture_dir), '--publish']
    if round_k is not None:
        cmd += ['--round', str(int(round_k))]
    return cmd, repo


def parse_line(line):
    """Builder output line -> ('demo', stem) | ('result', ok, text) | ('step', text) | ('progress', phase, i, k) |
    ('estimate', seconds, {phase: seconds} or {}) | ('prune', kind, name, bytes, reason) | ('info', text).
    Malformed PROGRESS / ESTIMATE / prune lines are 'info'."""
    line = line.rstrip()
    if line.split(' ', 1)[0] in PRUNE_KINDS:
        parts = line.split(None, 3)
        if len(parts) >= 3 and parts[2].isdigit():
            return ('prune', parts[0], parts[1], int(parts[2]), parts[3].strip() if len(parts) > 3 else '')
    if line.startswith('DEMO '):
        return ('demo', line[5:].strip())
    if line.startswith('PROGRESS '):
        parts = line.split()
        if len(parts) == 3 and '/' in parts[2]:
            i, _, k = parts[2].partition('/')
            if i.isdigit() and k.isdigit() and 0 < int(i) <= int(k):
                return ('progress', parts[1], int(i), int(k))
    if line.startswith('ESTIMATE '):
        try:
            parts = line.split()
            seconds, phases = float(parts[1]), {}
            for part in parts[2:]:
                name, sep, value = part.partition('=')
                if not sep or not name or float(value) < 0:
                    raise ValueError(part)
                phases[name] = float(value)
            if seconds >= 0:
                return ('estimate', seconds, phases)
        except ValueError:
            pass
    if line.startswith('RESULT:'):
        rest = line[7:].strip()
        return ('result', rest.startswith('OK'), rest)
    if line.startswith('STEP '):
        return ('step', line[5:].strip())
    return ('info', line.strip())


def extract_command(cfg, raw_path, app_root=None):
    """[cmd], repo for 'builder.exe extract <file.bc2raw>' (raw capture -> capture dir in the builder's work dir)."""
    prefix, repo = tool(cfg, app_root)
    return prefix + ['extract', str(raw_path)], repo


def run_tool(cmd, repo, on_line, popen=subprocess.Popen, env=None):
    """Run a builder.exe command (env: pov.builder_env(cfg), None = inherit), on_line(parsed) per output line;
    returns (rc, RESULT tuple or None, DEMO stem or None, last non-empty text)."""
    proc = popen(cmd, cwd=str(repo), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                 text=True, encoding='utf-8', errors='replace', creationflags=NOWINDOW, env=env)
    stem, result, last = None, None, ''
    for line in proc.stdout:
        p = parse_line(line)
        if p[0] == 'demo':
            stem = p[1]
        elif p[0] == 'result':
            result = p
        if isinstance(p[-1], str) and p[-1]:
            last = p[-1]
        on_line(p)
    return proc.wait(), result, stem, last


def run_extract(cfg, raw_path, on_line, popen=subprocess.Popen, app_root=None):
    """Extract one raw capture; returns (ok, capture dir or None, text). OK needs RESULT: OK and exit 0."""
    cmd, repo = extract_command(cfg, raw_path, app_root)
    rc, result, _stem, last = run_tool(cmd, repo, on_line, popen, env=pov.builder_env(cfg, app_root))
    if result is None:
        return False, None, f'extract exited rc={rc} without RESULT ({last})'
    ok = bool(result[1] and rc == 0)
    if not ok:
        return False, None, result[2] if not result[1] else f'{result[2]} (rc={rc})'
    return True, result[2][2:].strip() or None, result[2]  # 'OK <dir>'


def run_build(cfg, capture_dir, on_line, popen=subprocess.Popen, app_root=None, round_k=None):
    """Run one publish build, on_line(parsed) per output line; returns (ok, published stem or None, text).
    OK only with RESULT: OK, exit 0 and a DEMO line."""
    cmd, repo = build_command(cfg, capture_dir, app_root=app_root, round_k=round_k)
    rc, result, stem, last = run_tool(cmd, repo, on_line, popen, env=pov.builder_env(cfg, app_root))
    if result is None:
        return False, None, f'builder exited rc={rc} without RESULT ({last})'
    ok = bool(result[1] and rc == 0 and stem)
    if result[1] and not ok:
        return False, stem, f'{result[2]} (rc={rc}' + (', no DEMO line)' if not stem else ')')
    return ok, stem, result[2]


# ---------- raw capture retention (prune) ----------
# preview.json "captures": {"keep_raws": N, "delete_extracted": bool}. Nothing is deleted without the user's Clean up
# and confirmation: the window runs 'builder.exe prune ... --dry-run' first and shows exactly what would go.

RETENTION_KEY = 'captures'
KEEP_MAX = 999
PRUNE_KINDS = ('DELETE', 'WOULD-DELETE', 'SKIP', 'KEEP')
CONFIRM_MAX_LINES = 12


def _keep_value(value):
    """int 0..KEEP_MAX from an int or a digit string; anything else (bool, junk, None) -> 0."""
    if isinstance(value, bool):
        return 0
    if isinstance(value, str):
        value = value.strip()
        if not value.lstrip('-').isdigit():
            return 0
        value = int(value)
    if isinstance(value, float) and value.is_integer():
        value = int(value)
    if not isinstance(value, int):
        return 0
    return max(0, min(KEEP_MAX, value))


def retention(cfg=None):
    """{'keep_raws': 0..999 (0 = keep all), 'delete_extracted': bool} from preview.json "captures"."""
    raw = (cfg or {}).get(RETENTION_KEY) if isinstance(cfg, dict) else None
    raw = raw if isinstance(raw, dict) else {}
    return {'keep_raws': _keep_value(raw.get('keep_raws')), 'delete_extracted': raw.get('delete_extracted') is True}


def retention_update(cfg, keep_raws, delete_extracted):
    """{RETENTION_KEY: {...}} to merge into preview.json (other keys of "captures" kept, values clamped)."""
    current = (cfg or {}).get(RETENTION_KEY) if isinstance(cfg, dict) else None
    out = dict(current) if isinstance(current, dict) else {}
    out.update(keep_raws=_keep_value(keep_raws), delete_extracted=bool(delete_extracted))
    return {RETENTION_KEY: out}


def prune_enabled(ret):
    """True when the retention policy would delete anything at all."""
    ret = ret or {}
    return _keep_value(ret.get('keep_raws')) >= 1 or bool(ret.get('delete_extracted'))


def prune_command(cfg, keep_raws, delete_extracted, dry_run, app_root=None):
    """[cmd], repo for 'builder.exe prune [--root DIR]... [--keep N] [--extracted] [--dry-run]'."""
    prefix, repo = tool(cfg, app_root)
    cmd = prefix + ['prune'] + root_args(cfg)
    keep = _keep_value(keep_raws)
    if keep >= 1:
        cmd += ['--keep', str(keep)]
    if delete_extracted:
        cmd.append('--extracted')
    if dry_run:
        cmd.append('--dry-run')
    return cmd, repo


def run_prune(cfg, keep_raws, delete_extracted, dry_run, on_line, popen=subprocess.Popen, app_root=None):
    """Run prune (dry run or real); returns (ok, [('prune', kind, name, bytes, reason), ...], text).
    OK needs RESULT: OK and exit 0."""
    cmd, repo = prune_command(cfg, keep_raws, delete_extracted, dry_run, app_root)
    entries = []

    def collect(p):
        if p[0] == 'prune':
            entries.append(p)
        on_line(p)
    rc, result, _stem, last = run_tool(cmd, repo, collect, popen, env=pov.builder_env(cfg, app_root))
    if result is None:
        return False, entries, f'prune exited rc={rc} without RESULT ({last})'
    if not (result[1] and rc == 0):
        return False, entries, result[2] if not result[1] else f'{result[2]} (rc={rc})'
    return True, entries, result[2]


def prune_summary(entries):
    """{'delete': n, 'delete_bytes': b, 'skip': .., 'skip_bytes': .., 'keep': .., 'keep_bytes': ..};
    DELETE and WOULD-DELETE both count as delete."""
    out = {'delete': 0, 'delete_bytes': 0, 'skip': 0, 'skip_bytes': 0, 'keep': 0, 'keep_bytes': 0}
    for e in entries or []:
        key = {'DELETE': 'delete', 'WOULD-DELETE': 'delete', 'SKIP': 'skip', 'KEEP': 'keep'}.get(e[1])
        if key:
            out[key] += 1
            out[key + '_bytes'] += e[3]
    return out


def policy_text(keep_raws, delete_extracted):
    keep = _keep_value(keep_raws)
    if keep and delete_extracted:
        return f'Policy: delete raw files already extracted and keep the newest {keep} unextracted raw file(s).'
    if keep:
        return f'Policy: keep the newest {keep} raw file(s), delete older ones.'
    if delete_extracted:
        return 'Policy: delete raw files already extracted.'
    return 'Policy: keep all raw files.'


def prune_confirm_text(entries, keep_raws, delete_extracted):
    """Confirmation text for a dry run's entries (policy, one line per file to delete, at most CONFIRM_MAX_LINES,
    skipped files summarized, total); None when nothing would be deleted."""
    doomed = [e for e in entries or [] if e[1] in ('WOULD-DELETE', 'DELETE')]
    if not doomed:
        return None
    out = [policy_text(keep_raws, delete_extracted), '']
    for e in doomed[:CONFIRM_MAX_LINES]:
        out.append(f'{e[2]}  ({format_size(e[3])})' + (f'  {e[4]}' if e[4] else ''))
    if len(doomed) > CONFIRM_MAX_LINES:
        out.append(f'… and {len(doomed) - CONFIRM_MAX_LINES} more')
    skipped = [e for e in entries if e[1] == 'SKIP']
    if skipped:
        reasons = list(dict.fromkeys(e[4] or 'no reason given' for e in skipped))
        out += ['', f'{len(skipped)} file(s) skipped: ' + '; '.join(reasons)]
    out += ['', f'Delete {len(doomed)} file(s), {format_size(sum(e[3] for e in doomed))}?']
    return '\n'.join(out)


# ---------- progress bar (pure) ----------

PHASE_WEIGHTS = {'build': 0.75, 'gate': 0.22, 'publish': 0.03}  # of the time after soldiers
SOLDIERS_WEIGHT = 0.35
PHASE_CAP = 0.97  # within a phase the bar stops at 97 % of it until the next PROGRESS line
MIN_PHASE_S = 0.5  # recorded per-phase estimates below this count as this


def phase_plan(k):
    """Phase names of a publish build announcing k phases: build, gate, publish, and soldiers first when k has room
    for it (the builder converts soldier rows only when they are missing)."""
    names = ['build', 'gate', 'publish']
    return (['soldiers'] + names) if k > len(names) else names


def phase_weights(names):
    """Fractions of the whole build per phase (same order, sum 1)."""
    rest = 1.0 - (SOLDIERS_WEIGHT if 'soldiers' in names else 0.0)
    raw = [SOLDIERS_WEIGHT if n == 'soldiers' else rest * PHASE_WEIGHTS.get(n, 0.03) for n in names]
    total = sum(raw) or 1.0
    return [w / total for w in raw]


def run_weights(names, phase_estimates=None):
    """(weights, estimate total or None) for the bar: the recorded per-phase seconds of the last build when they cover
    every phase of this run (weights = their shares, total = their sum), else (phase_weights(names), None) and the
    caller keeps the ESTIMATE total."""
    est = phase_estimates or {}
    if names and all(n in est for n in names):
        secs = [max(float(est[n]), MIN_PHASE_S) for n in names]  # a 0.0 s phase still gets a sliver of the bar
        total = sum(secs)
        return [s / total for s in secs], total
    return phase_weights(names), None


def progress_fraction(phase_i, phase_k, phase_elapsed, estimate_total, phases_weights, floor=0.0):
    """0..1 for the bar: phases before phase_i (1-based) complete, the current one advanced by phase_elapsed over its
    share of estimate_total (capped at PHASE_CAP of the phase). phases_weights: one weight per phase (phase_weights);
    equal weights when it does not match phase_k. Never below floor (the previous value: the bar never goes back)."""
    if not phase_i or not phase_k:
        return max(0.0, min(1.0, floor))
    weights = list(phases_weights or [])
    if len(weights) != phase_k or sum(weights) <= 0:
        weights = [1.0 / phase_k] * phase_k
    else:
        weights = [w / sum(weights) for w in weights]
    i = min(max(int(phase_i), 1), phase_k)
    start, w = sum(weights[:i - 1]), weights[i - 1]
    within = 0.0
    if estimate_total and estimate_total > 0 and w > 0:
        within = min(max(phase_elapsed, 0.0) / (estimate_total * w), PHASE_CAP)
    return max(floor, min(1.0, start + w * within))


def progress_label(phase, elapsed, estimate=None, failed=False):
    """'build 45 s / ~120 s' (elapsed = whole build); 'failed in gate after 50 s' on failure."""
    phase = phase or 'starting'
    if failed:
        return f'failed in {phase} after {int(elapsed)} s'
    return f'{phase} {int(elapsed)} s' + (f' / ~{int(round(estimate))} s' if estimate else '')


# ---------- list rows / play ----------

def _num(value):
    return '-' if value is None else str(value)


def format_recorded(value):
    """'2026-10-27T21:05:33' -> '2026-10-27 21:05'; '-' when missing."""
    if not value:
        return '-'
    text = str(value).replace('T', ' ')
    return text[:16] if len(text) >= 16 else text


def sort_newest(items):
    """Captures newest first by 'recorded' (ISO text sorts by time); unknown dates last, list order kept otherwise."""
    dated = sorted((i for i in items if i.get('recorded')), key=lambda i: str(i['recorded']), reverse=True)
    return dated + [i for i in items if not i.get('recorded')]


def play_stem(item, just_built=None):
    """Demo stem the Recordings window's play button plays for a capture: a build that just finished for it
    (just_built: {capture dir: stem}), else the newest of its built demos; None when there is none."""
    if not item:
        return None
    stem = (just_built or {}).get(str(item.get('dir')))
    if stem:
        return stem
    builds = [s for s, _r in build_entries(item)]
    return builds[-1] if builds else None


def play_event(stem):
    """Event the Recordings window posts to the main window: select the demo and start its replay (no confirmation)."""
    return ('recordings_play', stem)


RAW_STATUS = 'raw, not extracted'


def is_raw(item):
    """A raw .bc2raw file of the recorder DLL (list entry kind 'raw'): Extract first; no build, no play."""
    return bool(item) and item.get('kind') == 'raw'


def item_key(item):
    """Stable row id: capture dir, raw file path, else the name."""
    return str(item.get('dir') or item.get('path') or item.get('name'))


def format_size(n):
    """bytes -> '12.3 MB' / '512 KB' / '-'."""
    if not isinstance(n, (int, float)) or n < 0:
        return '-'
    if n >= 1 << 20:
        return f'{n / (1 << 20):.1f} MB'
    return f'{max(1, round(n / 1024))} KB' if n else '0 KB'


def status_text(item):
    if is_raw(item):
        reason = item.get('reason') or ''
        bad = '' if not reason or reason.startswith('raw capture') else f', {reason}'
        return f"{RAW_STATUS}, {format_size(item.get('bytes'))}{bad}"
    if not item.get('buildable'):
        return item.get('reason') or 'not buildable'
    builds = [s for s, _r in build_entries(item)]
    return 'ready, built: ' + ', '.join(builds) if builds else 'ready'


def format_capture(item):
    """Row values (name, recorded, level, length, packets, drops, status) for the captures list."""
    level = (item.get('level') or '-').replace('Levels/', '')
    name = str(item.get('name') or Path(str(item.get('dir') or item.get('path') or '')).name)
    length = pov.mmss(item['seconds']) if item.get('seconds') is not None else '-'
    return (name, format_recorded(item.get('recorded')), level, length, _num(item.get('packets')),
            _num(item.get('drops')), status_text(item))


def busy_reason(owned_state, building=False):
    """None when a build may start, else the refusal. owned_state: {'server': running?, 'client': running?} for the
    processes this panel started (truthy = still running)."""
    if building:
        return 'A build is already running; wait for it to finish.'
    running = [name for kind, name in (('server', 'replay server'), ('client', 'BC2 client'))
               if (owned_state or {}).get(kind)]
    if running:
        return ('The panel is running a ' + ' and a '.join(running) + '. Building next to a live test is not allowed: '
                'close the game and stop the replay (close and reopen the panel), then build.')
    return None


# ---------- r287: one row per round, auto-extract ----------

MIN_ROUND_S = 30  # shorter rounds are listed greyed as 'too short' (no play / build)
RAW_SETTLE_S = 60  # a raw written to within this many seconds may still be recording: not extracted yet
SHORT_STATUS = 'too short'


def build_entries(item):
    """[(stem, round or None)] of a list item's builds: plain stems (pre-r287 builder) or {"stem", "round"}."""
    out = []
    for b in (item or {}).get('builds') or []:
        stem, rnd = (b.get('stem'), b.get('round')) if isinstance(b, dict) else (b, None)
        if stem and isinstance(stem, str):
            out.append((stem, rnd if isinstance(rnd, int) and not isinstance(rnd, bool) else None))
    return out


def round_stems(item, round_k):
    """Built stems of one round, oldest first; a build with round null counts as the capture's default_round.
    round_k None (no rounds listed) = every build."""
    if round_k is None:
        return [s for s, _r in build_entries(item)]
    default = (item or {}).get('default_round')
    return [s for s, r in build_entries(item) if (r if r is not None else default) == round_k]


def row_key(item, round_k):
    """Tree iid of a round row: item_key, plus '#r<K>' for a round."""
    return item_key(item) + ('' if round_k is None else f'#r{round_k}')


def round_rows(item):
    """List item -> [row dict]: {key, item, round (int or None), name, level, seconds, packets, short}.
    Raws and items without "rounds" give one row (round None); rounds with 0 packets are hidden; ' · round K' is
    added only when more than one round has packets."""
    name = str(item.get('name') or Path(str(item.get('dir') or item.get('path') or '')).name)
    listed = item.get('rounds') if not is_raw(item) and isinstance(item.get('rounds'), list) else []
    rounds = [r for r in listed if isinstance(r, dict) and isinstance(r.get('round'), int)
              and (r.get('packets') or 0) > 0]
    if listed and not rounds:
        return []  # every round empty
    if not rounds:
        sec = item.get('seconds')
        return [dict(key=row_key(item, None), item=item, round=None, name=name, level=item.get('level'), seconds=sec,
                     packets=item.get('packets'), short=not is_raw(item) and sec is not None and sec < MIN_ROUND_S)]
    multi = len(rounds) > 1
    out = []
    for r in rounds:
        k, sec = r['round'], r.get('seconds')
        out.append(dict(key=row_key(item, k), item=item, round=k, name=f'{name} · round {k}' if multi else name,
                        level=r.get('level') or item.get('level'), seconds=sec, packets=r.get('packets'),
                        short=sec is not None and sec < MIN_ROUND_S))
    return out


def rows_of(items):
    """round_rows over the items, newest capture first, rounds in order; duplicate keys dropped."""
    out, seen = [], set()
    for item in sort_newest(items):
        for row in round_rows(item):
            if row['key'] not in seen:
                seen.add(row['key'])
                out.append(row)
    return out


def row_play_stem(row, just_built=None):
    """Demo the window's Play plays for a round row: a build that just finished for it (just_built:
    {row key: stem}), else the newest published build of that round; None -> Play builds it first."""
    if not row or is_raw(row.get('item')):
        return None
    stem = (just_built or {}).get(row['key'])
    if stem:
        return stem
    stems = round_stems(row['item'], row['round'])
    return stems[-1] if stems else None


def row_status(row, just_built=None):
    item = row['item']
    if is_raw(item):
        return status_text(item)
    if row['short']:
        return SHORT_STATUS
    if not item.get('buildable'):
        return item.get('reason') or 'not buildable'
    stems = round_stems(item, row['round'])
    extra = (just_built or {}).get(row['key'])
    if extra and extra not in stems:
        stems = stems + [extra]
    return 'ready, built: ' + ', '.join(stems) if stems else 'ready, not built yet'


def format_row(row, just_built=None):
    """Tree values (name, recorded, level, length, packets, drops, status) of a round row."""
    item = row['item']
    level = (row.get('level') or '-').replace('Levels/', '')
    length = pov.mmss(row['seconds']) if row.get('seconds') is not None else '-'
    return (row['name'], format_recorded(item.get('recorded')), level, length, _num(row.get('packets')),
            _num(item.get('drops')), row_status(row, just_built))


def can_build(row):
    """Play may build / Rebuild may run: a buildable capture's round of at least MIN_ROUND_S."""
    return bool(row) and not is_raw(row['item']) and not row['short'] and bool(row['item'].get('buildable'))


HIDE_DEFAULT = ('v3-trial-*',)  # maintainer regression captures (builder reference trials)


def hide_patterns(cfg):
    """preview.json "recordings_hide": capture / raw names (glob patterns, case-insensitive) left out of the Recordings
    window; default ['v3-trial-*'] when the key is absent, [] shows everything. Non-list / non-string entries ignored."""
    pats = cfg.get('recordings_hide') if isinstance(cfg, dict) else None
    if pats is None:
        return list(HIDE_DEFAULT)
    return [x for x in pats if isinstance(x, str) and x.strip()] if isinstance(pats, list) else list(HIDE_DEFAULT)


def visible_captures(items, cfg):
    """list_captures items minus the ones whose name matches a hide pattern (the files stay; only the window hides them)."""
    pats = [x.lower() for x in hide_patterns(cfg)]
    return [i for i in items if not any(fnmatch.fnmatchcase(str(i.get('name') or '').lower(), p) for p in pats)]


def _mtime(path):
    try:
        return os.path.getmtime(path)
    except (OSError, TypeError, ValueError):
        return None


def raws_to_extract(items, now, tried=(), mtime=_mtime):
    """Raw items the window extracts by itself, oldest first: a valid header (reason 'raw capture: ...'), a known mtime
    at least RAW_SETTLE_S before now (else it may still be recording), not tried yet in this window (tried: paths)."""
    out = []
    for item in items or []:
        if not is_raw(item) or not item.get('path') or item['path'] in tried:
            continue
        reason = item.get('reason') or ''
        if reason and not reason.startswith('raw capture'):
            continue  # bad .bc2raw: extracting it would only fail
        mt = mtime(item['path'])
        if mt is None or now - mt < RAW_SETTLE_S:
            continue
        out.append((mt, item))
    return [item for _mt, item in sorted(out, key=lambda t: t[0])]
