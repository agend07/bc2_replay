"""Read-only roster, reciprocal soldier links and validated transforms."""
import math
import struct

MANAGER_TABLE = 0x14320F0
MANAGER_INTERFACE = 0x14320D4
PLAYER_TABLE = 0x1431D3C
SOLDIER_TABLE = 0x1437950
SOLDIER_INTERFACE = 0x14378F0


def soldier_link(client, player):
    """Fail closed on transient/missing links; never retain an old address."""
    try:
        soldier = client.u32(player + 0xc68)
        if not soldier:
            return {'status': 'unavailable'}
        if (client.u32(soldier) != SOLDIER_TABLE or
                client.u32(soldier + 0x4c) != SOLDIER_INTERFACE or
                client.u32(soldier + 0x220) != player):
            return {'status': 'unavailable'}
        if (client.u32(player + 0xc68) != soldier or
                client.u32(player) != PLAYER_TABLE or
                client.u32(soldier) != SOLDIER_TABLE or
                client.u32(soldier + 0x220) != player):
            return {'status': 'unavailable'}
        return {'status': 'validated_reciprocal_reference', 'address': hex(soldier)}
    except OSError:
        return {'status': 'unavailable'}


def manager_valid(client, address):
    try:
        return (client.u32(address) == MANAGER_TABLE and
                client.u32(address + 0x10) == MANAGER_INTERFACE)
    except OSError:
        return False


def find_manager(client):
    cached = getattr(client, '_roster_manager', None)
    if cached is not None and manager_valid(client, cached):
        return cached
    client._roster_manager = None
    hits, _ = client.scan({'manager': MANAGER_TABLE})
    managers = [address for address in hits['manager'] if manager_valid(client, address)]
    if len(managers) != 1:
        raise RuntimeError('Expected one validated player manager.')
    client._roster_manager = managers[0]
    return managers[0]


def roster(client):
    try:
        return read_roster(client, find_manager(client))
    except (OSError, RuntimeError):
        client._roster_manager = None
        raise


def read_roster(client, manager):
    begin, end, capacity = [client.u32(manager + offset) for offset in (0x94, 0x98, 0x9c)]
    if not (begin <= end <= capacity and (end-begin) % 4 == 0 and capacity-begin <= 256*4):
        raise RuntimeError('Unrecognized player collection bounds.')
    selected = client.u32(manager + 0xb4)
    players = []
    for offset in range(begin, end, 4):
        player = client.u32(offset)
        if not player or client.u32(player) != PLAYER_TABLE:
            raise RuntimeError('Unexpected player type; no speculative interpretation.')
        name_begin, name_end = client.u32(player+8), client.u32(player+12)
        if not name_begin or not 0 <= name_end-name_begin <= 128:
            raise RuntimeError('Unrecognized name string bounds.')
        name = client.read(name_begin, name_end-name_begin).decode('utf-8', 'replace')
        players.append({'name': name, 'selected_local_object': player == selected,
                        'soldier': soldier_link(client, player)})
    if (not manager_valid(client, manager) or
            selected != client.u32(manager + 0xb4) or
            (begin, end, capacity) != tuple(client.u32(manager+o) for o in (0x94,0x98,0x9c))):
        raise RuntimeError('Roster changed during read; retry.')
    return {'scope': 'current client player objects; NOT verified recording roster',
            'players': players}


def transform(c,a):
    if c.u32(a)!=SOLDIER_TABLE or c.u32(a+0x4c)!=SOLDIER_INTERFACE:
        raise RuntimeError('Soldier changed')
    # Native accessor 0x610A20, called by soldier main table slot +0x44.
    flags=c.read(a+0x26,1)[0]; pointer=c.u32(a+0x28)
    if not pointer:raise RuntimeError('No transform storage')
    index=c.read(pointer+0xe,1)[0] if flags&0x80 else None
    address=pointer+0x10+index*0x50 if index is not None else pointer
    rows=struct.unpack('<16f',c.read(address,64))
    if not all(math.isfinite(x) for x in rows):raise RuntimeError('Nonfinite transform')
    basis=[rows[i:i+3] for i in (0,4,8)]
    lengths=[math.sqrt(sum(x*x for x in r)) for r in basis]
    dots=[sum(basis[i][k]*basis[j][k] for k in range(3)) for i,j in ((0,1),(0,2),(1,2))]
    if not all(0.9<n<1.1 for n in lengths) or not all(abs(d)<0.1 for d in dots):
        raise RuntimeError('Not an approximately orthonormal transform')
    if c.read(a+0x26,1)[0]!=flags or c.u32(a+0x28)!=pointer or c.u32(a)!=SOLDIER_TABLE:
        raise RuntimeError('Transform storage changed')
    if index is not None and c.read(pointer+0xe,1)[0]!=index:
        raise RuntimeError('Transform index changed')
    return {'candidate_position':list(rows[12:15]),'basis_lengths':lengths,'basis_dot_products':dots}
