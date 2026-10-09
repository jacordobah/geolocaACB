from fastapi import Request

from ...application.use_cases.import_kmz import ImportKmz
from ..kmz.kmz_reader_impl import KmzReaderImpl


def get_import_kmz(request: Request) -> ImportKmz:
    return ImportKmz(
        KmzReaderImpl(),
        request.app.state.map_repository,
    )