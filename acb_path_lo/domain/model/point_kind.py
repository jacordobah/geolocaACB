from enum import Enum


class PointKind(str, Enum):
    MEETING_POINT = "meeting_point"
    SCHOOL = "school"
    EXPLICIT_START = "explicit_start"
    EXPLICIT_END = "explicit_end"
    WALKING_SEGMENT = "walking_segment"
    RISK_POINT = "risk_point"
    STUDENT = "student"
    SUPPORT = "support"
    OTHER = "other"


STORED_POINT_KINDS = frozenset({
    PointKind.MEETING_POINT,
    PointKind.SCHOOL,
    PointKind.EXPLICIT_START,
    PointKind.EXPLICIT_END,
})
