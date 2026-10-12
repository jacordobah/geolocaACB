import os
from contextlib import asynccontextmanager
from typing import AsyncGenerator

from fastapi import FastAPI
from pymongo import MongoClient

from .infrastructure.api.routes.imports import router
from .infrastructure.api.routes.maps import router as maps_router
from .infrastructure.api.routes.status import router as status_router
from .infrastructure.api.routes.routes import router as routes_router
from .infrastructure.persistence.mongo_map_repository import MongoMapRepository
from .infrastructure.persistence.mongo_route_status_repository import (
    MongoRouteStatusRepository,
)
from .infrastructure.config.geofence import load_geofence_policy
from .infrastructure.api.routes.tracking import router as tracking_router
from .infrastructure.persistence.mongo_tracking_repository import MongoTrackingRepository
from .infrastructure.persistence.mongo_position_repository import MongoPositionRepository
from .infrastructure.api.routes.tracking_control import router as tracking_control_router
from .infrastructure.config.tracking import load_tracking_policy
from .infrastructure.config.history import load_history_policy
from .infrastructure.security.tracking_control import valid_service_hash, decode_issuance_key
from .infrastructure.security.tracking_credentials import credential_fingerprint
from .domain.tracking_errors import TrackingStoreUnavailable


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    app.state.geofence_policy = load_geofence_policy()
    app.state.tracking_policy = load_tracking_policy()
    app.state.tracking_history_policy = load_history_policy()
    service_hash = os.getenv("ACB_TRACKING_SERVICE_TOKEN_SHA256")
    app.state.tracking_service_hash = service_hash if valid_service_hash(service_hash) else None
    issuance_value = os.getenv("ACB_TRACKING_ISSUANCE_KEY")
    app.state.tracking_issuance_key = decode_issuance_key(issuance_value)
    if (issuance_value is not None and app.state.tracking_service_hash is not None
            and credential_fingerprint(issuance_value) == app.state.tracking_service_hash):
        app.state.tracking_issuance_key = None
    client = MongoClient(
        os.getenv("MONGO_URI", "mongodb://localhost:27017"),
        serverSelectionTimeoutMS=5000,
    )
    try:
        client.admin.command("ping")
        app.state.map_repository = MongoMapRepository(
            client[os.getenv("MONGO_DATABASE", "geolocaacb")]
        )
        app.state.route_status_repository = MongoRouteStatusRepository(
            client[os.getenv("MONGO_DATABASE", "geolocaacb")]
        )
        app.state.tracking_repository = MongoTrackingRepository(
            client[os.getenv("MONGO_DATABASE", "geolocaacb")]
        )
        app.state.tracking_control_index_ready = False
        if app.state.tracking_service_hash is not None and app.state.tracking_issuance_key is not None:
            try:
                await app.state.tracking_repository.ensure_issuance_index()
                app.state.tracking_control_index_ready = True
            except TrackingStoreUnavailable:
                pass
        app.state.position_repository = MongoPositionRepository(
            client[os.getenv("MONGO_DATABASE", "geolocaacb")]
        )
        yield
    finally:
        client.close()


app = FastAPI(title="Geographic Routes API", lifespan=lifespan)

app.include_router(router, prefix="/api/v1")
app.include_router(maps_router, prefix="/api/v1")
app.include_router(status_router, prefix="/api/v1")
app.include_router(routes_router, prefix="/api/v1")
app.include_router(tracking_router, prefix="/api/v1")
app.include_router(tracking_control_router, prefix="/api/v1")
