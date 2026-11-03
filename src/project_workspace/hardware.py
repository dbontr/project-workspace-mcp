"""Validate worker-advertised hardware and select exclusive resource sets."""

import re

DEVICE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$")
KINDS = frozenset({"gpu", "npu", "fpga", "usb"})


def validate_devices(capabilities):
    devices = capabilities.get("devices", [])
    if not isinstance(devices, list) or len(devices) > 64:
        raise ValueError("Invalid hardware inventory")
    used = set()
    for device in devices:
        if not isinstance(device, dict) or set(device) - {"id", "kind", "model", "memory_mb"}:
            raise ValueError("Invalid hardware descriptor")
        identifier, kind = device.get("id"), device.get("kind")
        if (not isinstance(identifier, str) or not DEVICE_ID.fullmatch(identifier)
                or kind not in KINDS or identifier in used):
            raise ValueError("Invalid or duplicate hardware ID")
        used.add(identifier)
        if "model" in device and (not isinstance(device["model"], str) or len(device["model"]) > 128):
            raise ValueError("Invalid hardware model")
        if "memory_mb" in device and (type(device["memory_mb"]) is not int
                                      or not 0 <= device["memory_mb"] <= 10_000_000):
            raise ValueError("Invalid hardware memory")
    return devices


def validate_request(request):
    if request is None:
        return {}
    if not isinstance(request, dict) or set(request) - KINDS:
        raise ValueError("Hardware requests must use gpu, npu, fpga or usb")
    if any(type(count) is not int or count < 0 or count > 8 for count in request.values()):
        raise ValueError("Invalid hardware request count")
    if sum(request.values()) > 8:
        raise ValueError("Hardware request exceeds eight devices")
    return {kind: count for kind, count in request.items() if count}


def choose_devices(inventory, busy, requested):
    """Allocate whole devices; never treat an occupied GPU as shareable."""
    selected = []
    for kind, count in sorted(requested.items()):
        matching = sorted((d for d in inventory if d["kind"] == kind and d["id"] not in busy),
                          key=lambda d: d["id"])
        if len(matching) < count:
            return None
        selected.extend(matching[:count])
    return selected
