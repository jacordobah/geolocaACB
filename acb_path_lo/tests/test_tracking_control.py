import asyncio
import json
import unittest
from dataclasses import replace
from datetime import UTC, datetime
from unittest.mock import patch
from uuid import uuid4

from acb_path_lo.application.use_cases.create_tracking_session import CreateTrackingSession
from acb_path_lo.application.use_cases.enable_tracking import EnableTracking
from acb_path_lo.application.use_cases.get_tracking_session_control import GetTrackingSessionControl
from acb_path_lo.domain.model.first_meeting_point import FirstMeetingPoint
from acb_path_lo.domain.model.gps_position import HistoryPolicy
from acb_path_lo.domain.model.tracking_session import TrackingPolicy
from acb_path_lo.domain.tracking_errors import TrackingError, TrackingStoreUnavailable
from acb_path_lo.domain.errors import FirstMeetingPointNotConfigured, RouteNotFound
from acb_path_lo.infrastructure.security.tracking_control import (
    decode_issuance_key, derive_gps_credential, generate_service_credential,
    new_nonce, verify_service_credential,
)
from acb_path_lo.main import app


ROUTE = "6accba5cac543a6bc5a5d113"
POINT = "6accba5cac543a6bc5a5d181"


class MemoryRepository:
    def __init__(self):
        self.sessions = {}
        self.writes = 0
        self.uncertain = False

    async def get(self, tracking_id):
        return self.sessions.get(tracking_id)

    async def get_by_issuance_key(self, key_hash):
        return next((s for s in self.sessions.values()
                     if s.issuance and s.issuance.key_hash == key_hash), None)

    async def create(self, session):
        if session.tracking_id in self.sessions or await self.get_by_issuance_key(
                session.issuance.key_hash if session.issuance else ""):
            raise TrackingError("tracking_session_conflict")
        self.sessions[session.tracking_id] = session
        self.writes += 1
        if self.uncertain:
            self.uncertain = False
            raise TrackingStoreUnavailable()

    async def disable(self, session, when):
        current = self.sessions[session.tracking_id]
        if current.version != session.version or current.state != "enabled":
            return False
        self.sessions[session.tracking_id] = replace(
            current, state="disabled", disabled_at=when, version=current.version + 1)
        self.writes += 1
        return True


class PointReader:
    async def get_first_meeting_point(self, route_id):
        return await self.execute(route_id)

    async def execute(self, route_id):
        if route_id != ROUTE:
            raise ValueError("fixture route mismatch")
        return FirstMeetingPoint(ROUTE, POINT, "PE01", -74.0, 4.0)


class MissingPointReader:
    async def get_first_meeting_point(self, route_id):
        if route_id == ROUTE:
            raise FirstMeetingPointNotConfigured()
        raise RouteNotFound()


async def http(method, path, payload=None, credential=None, key=None):
    messages = []
    body = json.dumps(payload).encode() if payload is not None else b""
    headers = [(b"content-type", b"application/json")]
    if credential is not None:
        headers.append((b"authorization", ("Bearer " + credential).encode()))
    if key is not None:
        headers.append((b"idempotency-key", key.encode()))

    async def receive():
        return {"type": "http.request", "body": body, "more_body": False}

    async def send(message):
        messages.append(message)

    await app({"type": "http", "asgi": {"version": "3.0"}, "http_version": "1.1",
               "method": method, "scheme": "http", "path": path, "root_path": "",
               "query_string": b"", "headers": headers, "server": ("test", 80),
               "client": ("test", 123)}, receive, send)
    start = next(m for m in messages if m["type"] == "http.response.start")
    content = b"".join(m.get("body", b"") for m in messages if m["type"] == "http.response.body")
    return start["status"], json.loads(content), dict(start["headers"])


class ControlTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.repo = MemoryRepository()
        self.now = datetime.now(UTC)
        self.key_bytes = b"k" * 32
        self.key_text = str(uuid4())
        self.service, self.service_hash = generate_service_credential()
        self.values = {
            "tracking_repository": self.repo,
            "map_repository": PointReader(),
            "tracking_policy": TrackingPolicy(30, 10, 30, 5),
            "tracking_history_policy": HistoryPolicy(8, 0),
            "tracking_service_hash": self.service_hash,
            "tracking_issuance_key": self.key_bytes,
            "tracking_control_index_ready": True,
        }
        self.old = {k: getattr(app.state, k, None) for k in self.values}
        for k, v in self.values.items():
            setattr(app.state, k, v)
        self.addAsyncCleanup(self.restore)

    async def restore(self):
        for k, v in self.old.items():
            setattr(app.state, k, v)

    def creator(self):
        enable = EnableTracking(self.repo, PointReader(), TrackingPolicy(30, 10, 30, 5),
                                lambda: self.now, HistoryPolicy(8, 0))
        return CreateTrackingSession(enable, self.repo,
            lambda identifier, nonce: derive_gps_credential(self.key_bytes, identifier, nonce),
            new_nonce)

    def body(self):
        return {"guide_id": "GUIDE-001", "route_id": ROUTE}

    async def create_http(self, key=None, credential=None, body=None):
        return await http("POST", "/api/v1/tracking-sessions",
                          self.body() if body is None else body,
                          self.service if credential is None else credential,
                          self.key_text if key is None else key)

    async def test_authorized_creation_replay_and_disabled_retry(self):
        status, created, headers = await self.create_http()
        self.assertEqual(status, 201)
        self.assertIn(b"location", headers)
        self.assertEqual(created["meeting_point_id"], POINT)
        identifier = created["tracking_id"]
        self.assertEqual(self.repo.writes, 1)
        status, replay, _ = await self.create_http()
        self.assertEqual((status, replay), (200, created))
        self.assertEqual(self.repo.writes, 1)
        path = f"/api/v1/tracking-sessions/{identifier}"
        self.assertEqual((await http("GET", path + "/control", credential=self.service))[1]["state"], "enabled")
        self.assertEqual((await http("POST", path + "/disable", credential=self.service))[1]["state"], "disabled")
        self.assertEqual((await http("POST", path + "/disable", credential=self.service))[0], 200)
        self.assertEqual((await self.create_http())[:2], (200, created))
        self.assertEqual(self.repo.writes, 2)
        self.assertNotIn("credential", (await http("GET", path + "/control", credential=self.service))[1])
        self.assertEqual((await http("GET", path, credential=self.service))[0], 404)
        self.assertEqual((await http("GET", path, credential=created["session_credential"]))[0], 200)

    async def test_credentials_and_configuration(self):
        self.assertEqual((await http("POST", "/api/v1/tracking-sessions", self.body(),
                                     key=self.key_text))[0], 401)
        self.assertEqual((await self.create_http(credential="invalid"))[0], 401)
        status, created, _ = await self.create_http()
        path = f"/api/v1/tracking-sessions/{created['tracking_id']}"
        self.assertEqual((await http("POST", path + "/disable",
                                     credential=created["session_credential"]))[0], 401)
        self.assertEqual((await http("GET", path + "/control",
                                     credential=created["session_credential"]))[0], 401)
        app.state.tracking_service_hash = None
        self.assertEqual((await self.create_http())[0], 503)
        self.assertEqual((await http("GET", path, credential=created["session_credential"]))[0], 200)
        app.state.tracking_service_hash = self.service_hash
        app.state.tracking_issuance_key = None
        self.assertEqual((await self.create_http())[0], 503)
        self.assertEqual((await http("GET", path + "/control", credential=self.service))[0], 200)

    async def test_idempotency_conflict_and_uncertain_write(self):
        self.repo.uncertain = True
        first = await self.create_http()
        self.assertEqual(first[0], 200)
        self.assertEqual(self.repo.writes, 1)
        different = {**self.body(), "guide_id": "GUIDE-002"}
        status, body, _ = await self.create_http(body=different)
        self.assertEqual((status, body["detail"]["code"]), (409, "idempotency_key_conflict"))
        self.assertEqual((await self.create_http(key=str(uuid4())))[0], 201)
        self.assertEqual(self.repo.writes, 2)

    async def test_concurrent_creation_and_restart_recovery(self):
        results = await asyncio.gather(self.create_http(), self.create_http())
        self.assertEqual(sorted(item[0] for item in results), [200, 201])
        self.assertEqual(results[0][1], results[1][1])
        self.assertEqual(self.repo.writes, 1)
        # A new use-case instance with the same key reconstructs the same credential.
        recovered = await self.creator().execute(self.key_text, "GUIDE-001", ROUTE)
        self.assertEqual(recovered.credential, results[0][1]["session_credential"])
        self.assertTrue(recovered.replayed)

    async def test_strict_input_and_openapi(self):
        for body in ({"guide_id": True, "route_id": ROUTE},
                     {"guide_id": " ", "route_id": ROUTE},
                     {"guide_id": "g", "route_id": "bad"},
                     {**self.body(), "radius_m": 30}):
            self.assertEqual((await self.create_http(key=str(uuid4()), body=body))[0], 422)
        self.assertEqual((await self.create_http(key="bad"))[0], 422)
        self.assertEqual(self.repo.writes, 0)
        paths = app.openapi()["paths"]
        self.assertEqual(paths["/api/v1/tracking-sessions"]["post"]["security"],
                         [{"TrackingServiceCredential": []}])
        for path, method in (("/api/v1/tracking-sessions/{tracking_id}/control", "get"),
                             ("/api/v1/tracking-sessions/{tracking_id}/disable", "post")):
            self.assertIn(method, paths[path])
        self.assertEqual(paths["/api/v1/tracking-sessions/{tracking_id}/observations"]["post"]["security"],
                         [{"TrackingSessionCredential": []}])

    async def test_geographic_and_policy_errors_do_not_create(self):
        app.state.map_repository = MissingPointReader()
        self.assertEqual((await self.create_http())[0], 409)
        self.assertEqual((await self.create_http(key=str(uuid4()), body={
            **self.body(), "route_id": "f" * 24}))[0], 404)
        app.state.map_repository = PointReader()
        app.state.tracking_policy = None
        self.assertEqual((await self.create_http())[0], 503)
        app.state.tracking_policy = TrackingPolicy(30, 10, 30, 5)
        app.state.tracking_history_policy = None
        self.assertEqual((await self.create_http())[0], 503)
        self.assertEqual(self.repo.writes, 0)

    async def test_uncertain_unconfirmed_write_returns_availability(self):
        async def unavailable(session):
            raise TrackingStoreUnavailable()
        with patch.object(self.repo, "create", unavailable):
            status, body, _ = await self.create_http()
        self.assertEqual((status, body["detail"]["code"]),
                         (503, "tracking_store_unavailable"))
        self.assertEqual(self.repo.writes, 0)

    async def test_changed_issuance_key_cannot_reconstruct_credential(self):
        self.assertEqual((await self.create_http())[0], 201)
        app.state.tracking_issuance_key = b"q" * 32
        status, body, _ = await self.create_http()
        self.assertEqual((status, body["detail"]["code"]),
                         (503, "tracking_issuance_unavailable"))
        self.assertEqual(self.repo.writes, 1)

    async def test_policy_change_blocks_new_session_but_not_replay(self):
        first = await self.create_http()
        app.state.tracking_policy = None
        app.state.tracking_history_policy = None
        self.assertEqual((await self.create_http())[:2], (200, first[1]))
        self.assertEqual((await self.create_http(key=str(uuid4())))[0], 503)
        self.assertEqual(self.repo.writes, 1)

    def test_secret_formats(self):
        self.assertTrue(verify_service_credential(self.service, self.service_hash))
        self.assertFalse(verify_service_credential("invalid", self.service_hash))
        self.assertEqual(len(self.service), 43)
        self.assertIsNone(decode_issuance_key("invalid"))
        text = __import__("base64").urlsafe_b64encode(self.key_bytes).rstrip(b"=").decode()
        self.assertEqual(decode_issuance_key(text), self.key_bytes)
