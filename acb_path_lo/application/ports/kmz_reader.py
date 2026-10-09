from typing import Protocol
from ...domain.model.route import Route
from ...domain.model.meeting_point import MeetingPoint


class KmzReader(Protocol):
    def read(
        self, content: bytes
    ) -> tuple[list[Route], list[MeetingPoint]]:
        ...