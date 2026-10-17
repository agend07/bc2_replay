"""Inspect the verified BC2 ghost-demo container without launching the game.

Optional --player searches literal UTF-8 text at all eight bit alignments.
Name matches identify candidate player-related data, not a decoded player
object, spawn event or client-ready flag. Seconds assume the supplied rate.
"""
from pathlib import Path
import argparse
import hashlib
import json
import math
import struct


def inspect(base, player=None, fps=30):
    if not math.isfinite(fps) or fps <= 0:
        raise ValueError('FPS must be positive and finite.')
    path = Path(base)
    data = Path(str(path) + '.fbgd').read_bytes()
    index = Path(str(path) + '.fbgk').read_bytes()
    vendor = b'EA Digital Illusions CE AB\0'
    if not data.startswith(vendor):
        raise ValueError('Unsupported ghost recording header.')
    header_end = data.find(b'\0', len(vendor))
    if header_end < 0 or header_end > 256:
        raise ValueError('Invalid recording build string.')
    build = data[len(vendor):header_end].decode('ascii')
    offset = header_end + 1
    if len(index) < 16 or (len(index) - 4) % 12:
        raise ValueError('Unexpected keyframe index layout.')
    interval = struct.unpack_from('<I', index)[0]
    entries = [struct.unpack_from('<IfI', index, pos) for pos in range(4, len(index), 12)]
    indexed_offsets = {entry[2]: entry[0] for entry in entries}
    if len(indexed_offsets) != len(entries) or not interval:
        raise ValueError('Duplicate keyframe offsets or invalid interval.')
    needle = player.encode('utf-8') if player else None
    hits, keys = [], []
    delta_count = 0
    seen_initial_key = False
    while True:
        if offset >= len(data):
            raise ValueError('No recording terminator.')
        tag = data[offset]
        if tag == 0:
            if offset != len(data) - 1:
                raise ValueError('Unexpected data after recording terminator.')
            break
        if tag not in (1, 2) or offset + 9 > len(data):
            raise ValueError(f'Invalid record header at file offset {offset}.')
        _, size, bits = struct.unpack_from('<BII', data, offset)
        end = offset + 9 + size
        if end > len(data) or (bits + 7) // 8 != size:
            raise ValueError(f'Invalid payload length at file offset {offset}.')
        if tag == 1:
            if indexed_offsets.get(offset) != delta_count:
                raise ValueError('Stream keyframe disagrees with index.')
            if not seen_initial_key and delta_count != 0:
                raise ValueError('Expected an initial keyframe.')
            seen_initial_key = True
            keys.append({'frame': delta_count, 'seconds': delta_count / fps,
                         'file_offset': offset, 'payload_bytes': size})
        else:
            if not seen_initial_key:
                raise ValueError('Delta before initial keyframe.')
            delta_count += 1
        if needle:
            payload = data[offset+9:end]
            alignments = []
            for shift in range(8):
                shifted = payload if not shift else bytes(
                    ((a << shift) & 255) | (b >> (8-shift))
                    for a, b in zip(payload, payload[1:]))
                at = shifted.find(needle)
                # Ignore unused padding bits at the end of a payload.
                if at >= 0 and shift + (at + len(needle)) * 8 <= bits:
                    alignments.append(shift)
            if alignments:
                hits.append({'record': 'keyframe' if tag == 1 else 'delta',
                             'delta_frame': delta_count,
                             'seconds': delta_count / fps, 'file_offset': offset,
                             'bit_alignments': alignments})
        offset = end
    if not seen_initial_key or len(keys) != len(entries):
        raise ValueError('Missing indexed keyframes.')
    # In ordinary playback the initial keyframe and delta records advance
    # the counter; subsequent keyframes are skipped. Verified against the
    # native EOF counter for the supplied recording, not arbitrary builds.
    result = {'recording_base': str(path), 'ghost_file_sha256': hashlib.sha256(data).hexdigest(),
              'build_header': build, 'fps_assumed': fps,
              'keyframe_interval_updates': interval, 'keyframe_records': len(keys),
              'delta_records': delta_count, 'normal_playback_end_counter': delta_count + 1,
              'estimated_duration_seconds': (delta_count + 1) / fps,
              'all_index_offsets_verified': True,
              'player_search': player, 'first_player_text_match': hits[0] if hits else None,
              'player_text_matches': hits,
              'limits': ['Player text matches do not decode spawn, synchronization or camera readiness.',
                         'No automatic player-name discovery or universal viewer-ready time is implemented.',
                         'Frame-to-seconds conversion assumes the supplied update rate.']}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('base', help='Recording path without extension')
    parser.add_argument('--player', help='Known player name to search for')
    parser.add_argument('--fps', type=float, default=30)
    parser.add_argument('--output', type=Path, help='Write the JSON report to this file')
    args = parser.parse_args()
    result = inspect(args.base, args.player, args.fps)
    report = json.dumps(result, indent=2, ensure_ascii=False) + '\n'
    if args.output:
        if args.output.exists():
            raise SystemExit('Report already exists; choose another output filename.')
        with args.output.open('x', encoding='utf-8') as out:
            out.write(report)
        print('Report:', args.output)
        print('Estimated duration:', result['estimated_duration_seconds'])
        print('First player text match:', result['first_player_text_match'])
    else:
        print(report)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError) as exc:
        raise SystemExit(str(exc))
