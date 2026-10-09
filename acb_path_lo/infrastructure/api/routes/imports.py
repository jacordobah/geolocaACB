
import logging

from fastapi import APIRouter, Depends, File, HTTPException, UploadFile
from pymongo.errors import PyMongoError

from ....application.use_cases.import_kmz import ImportKmz
from ..dependencies import get_import_kmz

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/imports", tags=["Imports"])

MAX_FILE_SIZE = 20 * 1024 * 1024  # 20 MB

@router.post("/kmz", status_code=201)
async def import_kmz(
    file: UploadFile = File(...),
    use_case: ImportKmz = Depends(get_import_kmz),
):
    if not file.filename or not file.filename.lower().endswith(".kmz"):
        raise HTTPException(
            status_code=400,
            detail="A KMZ file must be provided",
        )

    content = await file.read(MAX_FILE_SIZE + 1)
    await file.close()

    if len(content) > MAX_FILE_SIZE:
        raise HTTPException(
            status_code=413,
            detail="The file exceeds the 20 MB limit",
        )

    if not content:
        raise HTTPException(
            status_code=400,
            detail="The file is empty",
        )

    try:
        result = await use_case.execute(content)
        return {
            "message": "File processed successfully",
            **result,
        }
    except ValueError as error:
        raise HTTPException(
            status_code=422,
            detail=str(error),
        ) from error
    except PyMongoError as error:
        logger.exception("Could not save KMZ import to MongoDB")
        raise HTTPException(
            status_code=503,
            detail="The import could not be saved to MongoDB",
        ) from error