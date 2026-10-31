import re

from .text_matching import normalize_label

SCHOOL_BRANCH = "RUTAS AL COLEGIO EN BICI"
ROUTE_GROUP_PATTERN = re.compile(r"^([A-Z]{2}\d{2})\b")


def route_group_code(folder_path: tuple[str, ...]) -> str | None:
    segments = tuple(normalize_label(segment) for segment in folder_path)
    try:
        branch_index = segments.index(SCHOOL_BRANCH)
    except ValueError:
        return None
    if branch_index + 1 >= len(segments):
        return None
    match = ROUTE_GROUP_PATTERN.match(segments[branch_index + 1])
    return match.group(1) if match else None


def day_shift(folder_path: tuple[str, ...]) -> str | None:
    for segment in folder_path:
        if normalize_label(segment).startswith("JORNADA"):
            return segment.strip()
    return None
