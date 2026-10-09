import os
from contextlib import asynccontextmanager
from typing import AsyncGenerator

from fastapi import FastAPI
from pymongo import MongoClient

from .infrastructure.api.routes.imports import router
from .infrastructure.persistence.mongo_map_repository import MongoMapRepository


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    client = MongoClient(
        os.getenv("MONGO_URI", "mongodb://localhost:27017"),
        serverSelectionTimeoutMS=5000,
    )
    try:
        client.admin.command("ping")
        app.state.map_repository = MongoMapRepository(
            client[os.getenv("MONGO_DATABASE", "geolocaacb")]
        )
        yield
    finally:
        client.close()


app = FastAPI(title="Geographic Routes API", lifespan=lifespan)

app.include_router(router, prefix="/api/v1")