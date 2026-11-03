"""Local device policy. The controller never supplies Docker host paths."""

import json
import os
import re
import stat
from pathlib import Path

from .hardware import validate_devices

CDI = re.compile(r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+=[A-Za-z0-9_.:-]+$')


def manifest(path=None):
    path = path if path is not None else os.getenv('PWM_DEVICE_MANIFEST')
    if not path:
        return []
    result = json.loads(Path(path).read_text(encoding='utf-8'))
    if not isinstance(result, dict) or set(result) != {'devices'}:
        raise ValueError('Invalid worker device policy')
    devices = result['devices']
    if not isinstance(devices, list) or len(devices)>64:
        raise ValueError('Invalid worker device count')
    validate_devices({'devices': [{k:v for k,v in d.items() if k in ('id','kind','model','memory_mb')}
                                  for d in devices]})
    for device in devices:
        if set(device) - {'id','kind','model','memory_mb','cdi','paths'}:
            raise ValueError('Unknown device policy setting')
        cdi = device.get('cdi')
        paths = device.get('paths', [])
        if cdi and (not isinstance(cdi, str) or not CDI.fullmatch(cdi)):
            raise ValueError('Invalid CDI device name')
        if not isinstance(paths,list) or len(paths)>8 or any(not isinstance(p,str)
                or not p.startswith('/dev/') or '..' in p.split('/') or len(p)>255 for p in paths):
            raise ValueError('Invalid host device path')
        if not cdi and not paths:
            raise ValueError('Device policy needs CDI name or paths')
    return devices


def docker_args(requested, configured):
    """Fail closed if the requested physical device no longer matches local policy."""
    if not isinstance(requested,list) or len(requested)>8:
        raise ValueError('Invalid requested devices')
    by_id = {d['id']:d for d in configured}
    seen = set()
    args = []
    for entry in requested:
        if not isinstance(entry,dict) or set(entry)!={'id','kind'}:
            raise ValueError('Invalid device lease')
        id_ = entry['id']
        if id_ not in by_id or entry['kind'] != by_id[id_]['kind'] or id_ in seen:
            raise ValueError('Hardware lease does not match node device policy')
        seen.add(id_)
        device = by_id[id_]
        if device.get('cdi'):
            args += ['--device',device['cdi']]
        for path in device.get('paths',[]):
            # Do not follow symlinks and do not pass arbitrary regular files.
            info = os.lstat(path)
            if not stat.S_ISCHR(info.st_mode):
                raise ValueError('Configured device is not a character device')
            args += ['--device',path]
    return args
