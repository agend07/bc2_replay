"""r226: neutralize the HUD 'vignette' looks (death grey/blur, low-health desaturation).
Always on: clean screen applies it on every enable (replay/clean_screen.apply_extras); no opt-in.

Static evidence (exact build 3911fcc8..., offline disassembly, no live test yet):

* Reflection type `VignetteData` (size 0xD0, POD floats only): Scale vec2 @0x00,
  Color vec3 @0x10, ColorTransform (ColorTransformData: Saturation/Contrast/
  Brightness/Hue + modifiers/frequencies) @0x20..0xBF, BlurAmount @0xC0,
  Opacity @0xC4, Exponent @0xC8, Mode (enum VignetteMode) @0xCC.
  VignetteMode: VMOff 0, VMCriticalHealth 1, VMInGameMenu 2, VMPreRound 3,
  VMDeath 4, VMHitIndicator 5, VMShockWave 6, VMPreset1/2 7/8, VMHealth1..4 9..12,
  VMRevival 13, VMFlynnCrate 14, VMEndOfRound 15, VMPlayerOnFire 16.
* HUD object (vtable 0x142B7A0, singleton [0x15704A8]) owns a vignette manager at
  HUD+0x27810 ([mgr] = HUD, set by init 0x934780 called from HUD 0x776300).
  0x92F980 FindVignette(mode, out): walks the `Vignettes` array of the HUD config
  [[HUD]+0x9C]+0x94 (data pointer at +0x9C, stride 0xD0) comparing entry+0xCC and
  copies the entry with 0x900910. SetMode 0x9347B0 (from HUD callback 0x776C73),
  per-frame Update 0x934810 re-reads the table for the current mode (+4) into the
  target copy at mgr+0xE0 every tick and blends into mgr+0x1B0.
* 0x90C640 (end of HUD tick 0x786CB0) converts mgr+0x1B0 and calls
  DynamicPostProcess vtable +0x48 (0x5C9680 -> 0x5BEA00) which writes
  [0x156D4F8]+0x4D0..+0x53F every frame. The render setup 0x5DE2A4 / 0x5DE5C4
  reads that block through getter +0x4C (0x5BEA60); +0x4F0 (BlurAmount) is added
  to the blur amount. The DEAD values recorded in mem_findings 9m
  (+0x4D0 = 2.4,2.4,1.6 ; +0x4E0 a=0.45 ; +0x4F0 = 1.0 ; desaturating matrix)
  are exactly Scale/Exponent, Color/Opacity, BlurAmount, ColorTransform.
  Holding the +0x4D0.. output (9o/9p) lost the race with this per-frame writer;
  this module changes the SOURCE table instead.

Mechanism (data only, no code, no vtable): copy the VMOff entry's float fields
over the entries of SUPPRESS_MODES (Mode field untouched). Original bytes are
saved to a recovery file BEFORE the first write; disable writes them back only
where our bytes are still present. Not scoped per frame: it lasts while enabled
(clean_screen enables it with free camera, disables it on camera restore).
"""
import json
import math
import struct

from preview_config import APP_ROOT as ROOT

STATE = ROOT / 'runtime' / 'private_clean_vignette_session.json'

HUD_SINGLETON = 0x15704A8
HUD_VTABLE = 0x142B7A0
HUD_SELF_REF = 0x27460          # [hud+0x27460] == hud (seek_guard validation)
HUD_VIGNETTE_MANAGER = 0x27810  # [mgr+0] == hud
HUD_CONFIG = 0x9C
CONFIG_VIGNETTES = 0x94         # embedded array object; element data pointer at +8
ARRAY_DATA = 0x08              # begin
ARRAY_END = 0x0C               # end (count = (end-begin)/0xD0, native 0xB6F410)
ARRAY_VTABLE = 0x1473CAC
MGR_MODE = 0x04
MGR_TARGET = 0xE0

ENTRY_SIZE = 0xD0
MODE_OFFSET = 0xCC
MODE_MAX = 16
MAX_ENTRIES = 64
VM_OFF = 0
MODE_NAMES = {0: 'Off', 1: 'CriticalHealth', 2: 'InGameMenu', 3: 'PreRound', 4: 'Death',
              5: 'HitIndicator', 6: 'ShockWave', 7: 'Preset1', 8: 'Preset2', 9: 'Health1',
              10: 'Health2', 11: 'Health3', 12: 'Health4', 13: 'Revival', 14: 'FlynnCrate',
              15: 'EndOfRound', 16: 'PlayerOnFire'}
# Player/effect looks; menus, pre-round and end-of-round stay native.
# r227: + Preset1/Preset2/FlynnCrate (7, 8, 14): blur/desaturation looks of unknown trigger.
SUPPRESS_MODES = (1, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 16)

# Exactly the dwords copied by native 0x900910 (+ 0x8FCD50 for ColorTransform), minus Mode.
_CT = (0x00, 0x04, 0x08, 0x10, 0x14, 0x18, 0x20, 0x24, 0x28, 0x30, 0x34, 0x38, 0x40, 0x44, 0x48,
       0x50, 0x54, 0x58, 0x60, 0x64, 0x68, 0x70, 0x74, 0x78, 0x80, 0x84, 0x88, 0x90)
COPY_FIELDS = (0x00, 0x04, 0x10, 0x14, 0x18) + tuple(0x20 + o for o in _CT) + (0xC0, 0xC4, 0xC8)
WRITE_SIZE = MODE_OFFSET        # bytes 0..0xCB; Mode at 0xCC is never written


# ---------------------------------------------------------------------------
# Pure helpers (offline-testable)
# ---------------------------------------------------------------------------
def entry_mode(entry):
    return struct.unpack_from('<I', entry, MODE_OFFSET)[0]


def fields(entry):
    return tuple(struct.unpack_from('<I', entry, o)[0] for o in COPY_FIELDS)


def finite(entry):
    return all(math.isfinite(struct.unpack_from('<f', entry, o)[0]) for o in COPY_FIELDS)


def parse_table(blob, count):
    """Split `count` 0xD0 entries; raise ValueError unless it is a plausible Vignettes table."""
    if not 1 <= count <= MAX_ENTRIES or len(blob) < count * ENTRY_SIZE:
        raise ValueError(f'implausible vignette count {count}')
    entries = [bytes(blob[i * ENTRY_SIZE:(i + 1) * ENTRY_SIZE]) for i in range(count)]
    modes = [entry_mode(e) for e in entries]
    if any(m > MODE_MAX for m in modes) or len(set(modes)) != len(modes):
        raise ValueError(f'invalid/duplicate vignette modes {modes}')
    if VM_OFF not in modes:
        raise ValueError('no VMOff entry (native init would have failed)')
    if not all(finite(e) for e in entries):
        raise ValueError('non-finite float in vignette table')
    return entries


def neutral_image(target, off_entry):
    """Target bytes 0..0xCB with every copied float taken from VMOff; padding/Mode kept."""
    out = bytearray(target[:WRITE_SIZE])
    for o in COPY_FIELDS:
        out[o:o + 4] = off_entry[o:o + 4]
    return bytes(out)


def plan(entries, suppress=SUPPRESS_MODES):
    """[(index, mode, original_0xCC, new_0xCC)] for entries that would change."""
    off = next(e for e in entries if entry_mode(e) == VM_OFF)
    out = []
    for index, entry in enumerate(entries):
        mode = entry_mode(entry)
        if mode in suppress:
            new = neutral_image(entry, off)
            if new != entry[:WRITE_SIZE]:
                out.append((index, mode, entry[:WRITE_SIZE], new))
    return out


def restore_action(current, original, written):
    if current == written:
        return 'restore'
    if current == original:
        return 'native'
    return 'changed'


# ---------------------------------------------------------------------------
# Live discovery (read-only)
# ---------------------------------------------------------------------------
def _writable(client, address, size):
    import ctypes as c
    from replay.client import MemoryInfo, k
    end = address + size
    while address < end:
        info = MemoryInfo()
        if not k.VirtualQueryEx(client.handle, address, c.byref(info), c.sizeof(info)):
            return False
        if info.State != 0x1000 or info.Protect not in (0x04, 0x08, 0x40, 0x80):
            return False
        address = info.BaseAddress + info.RegionSize
    return True


def _hud_vtable_ok(client, hud):
    vt = client.u32(hud)
    if vt == HUD_VTABLE:
        return True
    try:
        from replay import seek_guard
        return seek_guard.is_guard_clone(client, hud, vt)
    except Exception:
        return False


def _hud_and_manager(client):
    hud = client.u32(HUD_SINGLETON)
    if not hud or hud % 4 or not _hud_vtable_ok(client, hud) or client.u32(hud + HUD_SELF_REF) != hud:
        raise RuntimeError('HUD object missing/unvalidated; join the replay first')
    mgr = hud + HUD_VIGNETTE_MANAGER
    if client.u32(mgr) != hud:
        raise RuntimeError('HUD vignette manager not initialised')
    cfg = client.u32(hud + HUD_CONFIG)
    if not cfg:
        raise RuntimeError('HUD config missing')
    return hud, mgr, cfg


def layout_candidates(client, arr):
    """[(name, data, count_or_None, note)] -- 'vector' is the statically derived one.

    r226b: FindVignette 0x92F980 calls [[arr]+0] for the count and indexes [arr+8].
    Vtable 0x1473CAC (VignetteData array; its +0x10 slot 0xB6F4C0 copies with
    0x900910 and steps 0xD0) has count 0xB6F410 = ([ecx+0xC] - [ecx+8]) / 0xD0,
    i.e. {vtable, typeinfo, begin, end}. Live run r226 showed array+4 = 0x1C06CFC
    (a typeinfo pointer), consistent with that.
    """
    out = []
    begin, end = client.u32(arr + ARRAY_DATA), client.u32(arr + ARRAY_END)
    span = end - begin
    vt = client.u32(arr)
    note = f'vtable {vt:#x}' + ('' if vt == ARRAY_VTABLE else ' (not 0x1473CAC)')
    if begin and end > begin and span % ENTRY_SIZE == 0:
        out.append(('vector', begin, span // ENTRY_SIZE, note))
    else:
        out.append(('vector', begin, None, f'{note}; begin {begin:#x} end {end:#x} not a 0xD0 span'))
    if begin:
        for name, count in (('data-4', _u32_or_none(client, begin - 4)), ('array+4', client.u32(arr + 4))):
            out.append((name, begin, count, 'legacy guess'))
    return out


def _u32_or_none(client, address):
    try:
        return client.u32(address)
    except OSError:
        return None


def check_layout(client, mgr, data, count, previous=None):
    """Return (entries, manager_match) or raise ValueError/OSError. manager_match: True/None."""
    if count is None or not 1 <= count <= MAX_ENTRIES:
        raise ValueError(f'count {count}')
    entries = parse_table(client.read(data, count * ENTRY_SIZE), count)
    current = client.u32(mgr + MGR_MODE)
    by_mode = {entry_mode(e): e for e in entries}
    if current not in by_mode:
        return entries, None
    accepted = {fields(by_mode[current])}
    if previous and current in previous:
        accepted.add(fields(previous[current]))
    for _ in range(3):
        if fields(client.read(mgr + MGR_TARGET, ENTRY_SIZE)) in accepted:
            return entries, True
    raise ValueError('manager copy mgr+0xE0 does not match the table entry of the current mode')


def locate(client, previous=None):
    """Return dict(hud, cfg, data, count, entries, current_mode); raise unless exactly one
    distinct table validates (the statically derived 'vector' layout must be among them).

    previous: {mode: original_entry_bytes} from our own session -- the native copy
    at mgr+0xE0 may still hold the pre-patch entry until the next HUD tick.
    """
    hud, mgr, cfg = _hud_and_manager(client)
    arr = cfg + CONFIG_VIGNETTES
    valid, errors = {}, []
    for name, data, count, _note in layout_candidates(client, arr):
        try:
            entries, match = check_layout(client, mgr, data, count, previous)
            valid.setdefault((data, count), []).append((name, entries, match))
        except (ValueError, OSError) as exc:
            errors.append(f'{name}: {exc}')
    if len(valid) != 1:
        raise RuntimeError(f'Vignettes table: {len(valid)} distinct layouts validate; nothing written. '
                           + '; '.join(errors))
    (data, count), hits = next(iter(valid.items()))
    names = [h[0] for h in hits]
    if 'vector' not in names:
        raise RuntimeError(f'only non-derived layout(s) {names} validate; nothing written')
    return dict(hud=hud, cfg=cfg, data=data, count=count, count_source='+'.join(names),
                entries=hits[0][1], current_mode=client.u32(mgr + MGR_MODE))


def _created(client):
    from replay.clean_screen import created
    return created(client)


def _load(client):
    if not STATE.exists():
        return None
    data = json.loads(STATE.read_text(encoding='utf-8'))
    if data.get('pid') != client.pid or data.get('created') != _created(client):
        STATE.unlink()          # that process is gone; nothing to restore
        return None
    return data


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------
def _with_client(client, write, fn):
    own = client is None
    if own:
        if write:
            from replay.clean_screen import WriteClient
            client = WriteClient()
        else:
            from replay.client import Client
            client = Client()
    try:
        return fn(client)
    finally:
        if own:
            client.close()


def enable(client=None):
    def run(client):
        session = _load(client)
        previous = None
        if session is not None:
            previous = {e['mode']: bytes.fromhex(e['original']) + struct.pack('<I', e['mode'])
                        for e in session['entries']}
        found = locate(client, previous)
        todo = plan(found['entries'])
        if session is not None and session.get('data') == found['data']:
            # Re-enable in the same process: only entries not already ours.
            done = {e['index'] for e in session['entries']}
            todo = [t for t in todo if t[0] not in done]
        elif session is not None:
            raise RuntimeError('Previous vignette session targets another table; disable first')
        else:
            session = dict(pid=client.pid, created=_created(client), hud=found['hud'],
                           cfg=found['cfg'], data=found['data'], count=found['count'], entries=[])
        for index, mode, original, new in todo:
            session['entries'].append(dict(index=index, mode=mode, address=found['data'] + index * ENTRY_SIZE,
                                           original=original.hex(), written=new.hex()))
        if todo:
            if not _writable(client, found['data'], found['count'] * ENTRY_SIZE):
                raise RuntimeError('Vignettes table is not in writable memory; nothing written')
            STATE.parent.mkdir(parents=True, exist_ok=True)
            STATE.write_text(json.dumps(session, indent=2), encoding='utf-8')   # recovery first
            for entry in session['entries']:
                if entry['index'] in {t[0] for t in todo}:
                    client.write(entry['address'], bytes.fromhex(entry['written']))
        return {'neutralized': [MODE_NAMES.get(e['mode'], e['mode']) for e in session['entries']],
                'newly_written': len(todo), 'table_entries': found['count'],
                'count_source': found['count_source'],
                'current_mode': MODE_NAMES.get(found['current_mode'], found['current_mode'])}
    return _with_client(client, True, run)


def disable(client=None):
    def run(client):
        session = _load(client)
        if session is None:
            return {'vignette': 'not installed'}
        result = {}
        for entry in session['entries']:
            name = MODE_NAMES.get(entry['mode'], entry['mode'])
            original, written = bytes.fromhex(entry['original']), bytes.fromhex(entry['written'])
            try:
                current = client.read(entry['address'], WRITE_SIZE)
            except OSError:
                result[name] = 'gone'
                continue
            action = restore_action(current, original, written)
            if action == 'restore':
                client.write(entry['address'], original)
                result[name] = 'restored'
            else:
                result[name] = 'native' if action == 'native' else 'changed by game (left)'
        STATE.unlink()
        return result
    return _with_client(client, True, run)


def status(client=None):
    def run(client):
        out = {}
        if not STATE.exists():
            out['installed'] = False
            return out
        session = json.loads(STATE.read_text(encoding='utf-8'))
        if session.get('pid') != client.pid:
            out.update(installed=False, note='stale session from another client process')
            return out
        active = {}
        for entry in session['entries']:
            try:
                current = client.read(entry['address'], WRITE_SIZE)
                active[MODE_NAMES.get(entry['mode'], entry['mode'])] = current.hex() == entry['written']
            except OSError:
                active[MODE_NAMES.get(entry['mode'], entry['mode'])] = None
        try:
            mode = client.u32(session['hud'] + HUD_VIGNETTE_MANAGER + MGR_MODE)
            out['current_mode'] = MODE_NAMES.get(mode, mode)
        except OSError:
            pass
        out.update(installed=True, neutralized=active)
        return out
    return _with_client(client, False, run)


def _hexdump(client, address, before=0x40, after=0x40):
    try:
        return {'from': hex(address - before), 'hex': client.read(address - before, before + after).hex()}
    except OSError as exc:
        return {'from': hex(address - before), 'error': str(exc)}


def _rows(entries):
    rows = []
    for e in entries:
        f = lambda o: round(struct.unpack_from('<f', e, o)[0], 4)
        rows.append({'mode': MODE_NAMES.get(entry_mode(e), entry_mode(e)), 'scale': [f(0), f(4)],
                     'color': [f(0x10), f(0x14), f(0x18)], 'blur': f(0xC0), 'opacity': f(0xC4),
                     'exponent': f(0xC8), 'saturation': [f(0x20), f(0x24), f(0x28)]})
    return rows


def inspect(client=None):
    """Read-only, never raises after the HUD step: dumps pointers, raw bytes, every layout tried."""
    def run(client):
        out = {}
        try:
            hud, mgr, cfg = _hud_and_manager(client)
        except Exception as exc:
            return {'error': str(exc)}
        arr = cfg + CONFIG_VIGNETTES
        out.update(hud=hex(hud), mgr=hex(mgr), cfg=hex(cfg), array=hex(arr),
                   array_words=[hex(v) if v is not None else None for v in (_u32_or_none(client, arr + 4 * i) for i in range(6))],
                   current_mode=MODE_NAMES.get(client.u32(mgr + MGR_MODE), client.u32(mgr + MGR_MODE)),
                   raw={'cfg+0x94': _hexdump(client, arr), 'mgr+0xE0': _hexdump(client, mgr + MGR_TARGET, 0, ENTRY_SIZE)})
        try:
            vt = client.u32(arr)
            out['array_vtable_slots'] = [hex(client.u32(vt + 4 * i)) for i in range(4)]
        except OSError as exc:
            out['array_vtable_slots'] = str(exc)
        layouts = []
        try:
            candidates = layout_candidates(client, arr)
        except OSError as exc:
            candidates = []
            layouts.append({'error': f'array object unreadable: {exc}'})
        for name, data, count, note in candidates:
            row = {'layout': name, 'data': hex(data), 'count': count, 'note': note}
            if data:
                row['raw_around_data'] = _hexdump(client, data)
            try:
                entries, match = check_layout(client, mgr, data, count)
                row.update(valid=True, manager_copy_match=match, table=_rows(entries))
            except (ValueError, OSError) as exc:
                row.update(valid=False, reason=str(exc))
            layouts.append(row)
        out['layouts'] = layouts
        try:
            out['enable_would_use'] = locate(client)['count_source']
        except Exception as exc:
            out['enable_would_use'] = f'REFUSE: {exc}'
        return out
    return _with_client(client, False, run)


PP_GLOBAL = 0x156D4F8
PP_WATCH = 0x1180                 # DynamicPostProcess bytes diffed per sample
PP_FIELDS = {'dof_b0': 0xB0, 'vig_scale_x': 0x4D0, 'vig_scale_y': 0x4D4, 'vig_exp': 0x4D8,
             'vig_opacity': 0x4EC, 'vig_blur': 0x4F0, 'mat00': 0x500, 'blood_mode': 0x1148,
             'blood_low': 0x1170}


def diff_offsets(base, cur, limit=48):
    """Dword offsets whose value differs (bounded list)."""
    out = []
    for o in range(0, min(len(base), len(cur)) - 3, 4):
        if base[o:o + 4] != cur[o:o + 4]:
            out.append(o)
            if len(out) >= limit:
                break
    return out


def watch(seconds=60.0, hz=20.0, path=None, client=None):
    """Read-only logger: vignette mode/blend, DynamicPostProcess fields + changed dwords
    (vs the first sample), camera FOV. One JSON line per sample."""
    import time
    from replay import free_cam_fov as fov
    path = path or (ROOT / 'runtime' / 'private_clean_watch.jsonl')

    def run(client):
        hud, mgr, _cfg = _hud_and_manager(client)
        pp = client.u32(PP_GLOBAL)
        base = client.read(pp, PP_WATCH) if pp else b''
        n, end = 0, time.time() + seconds
        with open(path, 'w', encoding='utf-8') as out:
            while time.time() < end:
                t = time.time()
                row = {'t': round(t, 3)}
                try:
                    mode = client.u32(mgr + MGR_MODE)
                    blend = client.read(mgr + 0x1B0, ENTRY_SIZE)
                    row.update(mode=MODE_NAMES.get(mode, mode),
                               blend_blur=round(struct.unpack_from('<f', blend, 0xC0)[0], 3),
                               blend_opacity=round(struct.unpack_from('<f', blend, 0xC4)[0], 3),
                               blend_sat=round(struct.unpack_from('<f', blend, 0x20)[0], 3))
                    if pp and client.u32(PP_GLOBAL) == pp:
                        cur = client.read(pp, PP_WATCH)
                        for name, o in PP_FIELDS.items():
                            v = struct.unpack_from('<I' if name == 'blood_mode' else '<f', cur, o)[0]
                            row[name] = v if name == 'blood_mode' else round(v, 4)
                        row['pp_changed'] = [hex(o) for o in diff_offsets(base, cur)]
                    active = client.u32(fov.CAMERA_MANAGER + fov.MGR_ACTIVE)
                    row['cam_free'] = bool(active) and client.u32(active) == fov.FREE_CAMERA_VTABLE
                    row['cam_fov74'] = round(struct.unpack('<f', client.read(active + 0x74, 4))[0], 3) if active else None
                    row['soldier_fov'] = fov.fallback_fov(client)
                except OSError as exc:
                    row['error'] = str(exc)
                out.write(json.dumps(row) + '\n')
                n += 1
                time.sleep(max(0.0, 1.0 / hz - (time.time() - t)))
        return {'samples': n, 'file': str(path)}
    return _with_client(client, False, run)


if __name__ == '__main__':
    import sys
    # Dev CLI, from the repo root: python -m replay.clean_vignette inspect|status|enable|disable|watch [s]
    action = sys.argv[1] if len(sys.argv) > 1 else 'status'
    if action == 'watch':   # python -m replay.clean_vignette watch [seconds]
        print(json.dumps(watch(float(sys.argv[2]) if len(sys.argv) > 2 else 60.0), indent=2))
        raise SystemExit(0)
    table = {'inspect': inspect, 'status': status, 'enable': enable, 'disable': disable}
    print(json.dumps(table[action](), indent=2))
