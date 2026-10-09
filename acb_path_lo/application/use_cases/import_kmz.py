from uuid import uuid4

from ..ports.kmz_reader import KmzReader
from ...domain.repositories.map_repository import MapRepository


class ImportKmz:

    def __init__(
        self,
        reader: KmzReader,
        repository: MapRepository,
    ):
        self.reader = reader
        self.repository = repository

    async def execute(self, content: bytes) -> dict:
        routes, points = self.reader.read(content)

        if not routes and not points:
            raise ValueError(
                "The file does not contain any valid routes or meeting points"
            )

        import_id = str(uuid4())

        await self.repository.save_import(
            import_id,
            routes,
            points,
        )

        return {
            "import_id": import_id,
            "saved_routes": len(routes),
            "saved_points": len(points),
        }