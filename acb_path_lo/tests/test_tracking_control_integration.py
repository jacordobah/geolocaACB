"""Opt-in HTTP/Mongo checks against a disposable acb-phase53-mongo database."""
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime
import hashlib
import json
import os
import secrets
import subprocess
import sys
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
from uuid import uuid4

from bson import ObjectId, json_util
from pymongo import MongoClient

from acb_path_lo.infrastructure.security.tracking_control import generate_service_credential


@unittest.skipUnless(os.getenv("ACB_PHASE53_INTEGRATION") == "1",
                     "Requires disposable MongoDB and explicit opt-in")
class TrackingControlIntegration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if (os.getenv("MONGO_URI") != "mongodb://acb-phase53-mongo:27017"
                or os.getenv("MONGO_DATABASE") != "acb_phase53_tmp"):
            raise RuntimeError("Refusing integration writes outside disposable MongoDB")
        cls.client = MongoClient(os.environ["MONGO_URI"], serverSelectionTimeoutMS=5000)
        cls.addClassCleanup(cls.client.close)
        cls.db = cls.client[os.environ["MONGO_DATABASE"]]
        if cls.db.list_collection_names():
            raise RuntimeError("Disposable database must start empty")
        cls.route_id, cls.point_id = ObjectId(), ObjectId()
        cls.db.routes.insert_one({"_id": cls.route_id, "name": "synthetic route",
            "import_id": "phase53-synthetic", "first_meeting_point_id": cls.point_id,
            "geometry": {"type": "LineString", "coordinates": [[-74.0, 4.0], [-74.001, 4.001]]}})
        cls.db.meeting_points.insert_one({"_id": cls.point_id, "name": "PE01",
            "import_id": "phase53-synthetic", "location": {"type": "Point",
            "coordinates": [-74.0, 4.0]}})
        cls.service, service_hash = generate_service_credential()
        issuance_key = secrets.token_urlsafe(32)
        cls.api_env = {**os.environ,
            "ACB_TRACKING_SERVICE_TOKEN_SHA256": service_hash,
            "ACB_TRACKING_ISSUANCE_KEY": issuance_key,
            "ACB_FIRST_PE_RADIUS_M": "30",
            "ACB_FIRST_PE_MAX_ACCURACY_M": "10",
            "ACB_GPS_MAX_AGE_S": "30",
            "ACB_GPS_CLOCK_SKEW_S": "5",
            "ACB_GPS_HISTORY_CUTOFF_BOGOTA": "08:00"}
        cls.base = "http://127.0.0.1:18053"
        cls.start_api()
        cls.addClassCleanup(cls.stop_api)
        cls.before = cls.snapshot_originals()
        cls.addClassCleanup(cls.check_originals)

    @classmethod
    def start_api(cls):
        cls.api = subprocess.Popen([sys.executable, "-B", "-m", "uvicorn",
            "acb_path_lo.main:app", "--host", "127.0.0.1", "--port", "18053",
            "--no-access-log"], env=cls.api_env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(100):
            if cls.api.poll() is not None:
                raise RuntimeError("Temporary API exited")
            try:
                with urlopen(cls.base + "/openapi.json", timeout=1) as result:
                    if result.status == 200:
                        return
            except (URLError, TimeoutError):
                time.sleep(.1)
        raise RuntimeError("Temporary API unavailable")

    @classmethod
    def stop_api(cls):
        cls.api.terminate()
        try:
            cls.api.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.api.kill()
            cls.api.wait(timeout=5)

    @classmethod
    def snapshot_originals(cls):
        return {name: hashlib.sha256(json_util.dumps({
            "docs": list(cls.db[name].find().sort("_id", 1)),
            "indexes": list(cls.db[name].list_indexes())}, sort_keys=True).encode()).hexdigest()
            for name in ("routes", "meeting_points", "route_status")}

    @classmethod
    def check_originals(cls):
        if cls.snapshot_originals() != cls.before:
            raise AssertionError("Synthetic geographic collections changed after startup")

    @classmethod
    def http(cls, method, path, payload=None, credential=None, key=None):
        headers = {"Content-Type": "application/json"}
        if credential is not None:
            headers["Authorization"] = "Bearer " + credential
        if key is not None:
            headers["Idempotency-Key"] = key
        data = json.dumps(payload).encode() if payload is not None else None
        request = Request(cls.base + path, data=data, headers=headers, method=method)
        try:
            response = urlopen(request, timeout=15)
        except HTTPError as error:
            response = error
        with response:
            return response.status, json.load(response)

    def test_http_mongo_control_replay_restart_and_concurrency(self):
        path = "/api/v1/tracking-sessions"
        payload = {"guide_id": "phase53-synthetic-guide", "route_id": str(self.route_id)}
        key = str(uuid4())
        def create():
            return self.http("POST", path, payload, self.service, key)
        with ThreadPoolExecutor(max_workers=2) as pool:
            first, second = list(pool.map(lambda _: create(), range(2)))
        self.assertEqual(sorted([first[0], second[0]]), [200, 201])
        self.assertEqual(first[1], second[1])
        created = first[1]
        tracking_id = created["tracking_id"]
        session = self.db.tracking_sessions.find_one({"_id": tracking_id})
        self.assertIsNotNone(session)
        self.assertEqual(self.db.tracking_sessions.count_documents(
            {"issuance.key_hash": session["issuance"]["key_hash"]}), 1)
        self.assertNotIn(created["session_credential"], json_util.dumps(session))
        index = self.db.tracking_sessions.index_information()["unique_issuance_key"]
        self.assertTrue(index["unique"])
        self.assertEqual(self.http("POST", path,
            {**payload, "guide_id": "different"}, self.service, key)[0], 409)
        control = path + "/" + tracking_id + "/control"
        gps = path + "/" + tracking_id
        before_read = self.db.tracking_sessions.find_one({"_id": tracking_id})
        self.assertEqual(self.http("GET", control, credential=self.service)[1]["state"], "enabled")
        self.assertEqual(self.db.tracking_sessions.find_one({"_id": tracking_id}), before_read)
        self.assertEqual(self.http("GET", control,
            credential=created["session_credential"])[0], 401)
        self.assertEqual(self.http("GET", gps,
            credential=self.service)[0], 404)
        self.assertEqual(self.http("GET", gps,
            credential=created["session_credential"])[0], 200)
        observation = {"sequence": 1, "latitude": 4.0, "longitude": -74.0,
            "accuracy_m": 5.0, "captured_at": datetime.now(UTC).isoformat()}
        self.assertEqual(self.http("POST", gps + "/observations", observation,
            created["session_credential"])[1]["status"], "inside")
        self.assertEqual(self.http("GET", control, credential=self.service)[1]["first_arrival"]["sequence"], 1)
        disabled = self.http("POST", gps + "/disable", credential=self.service)
        self.assertEqual((disabled[0], disabled[1]["state"]), (200, "disabled"))
        self.assertEqual(self.http("POST", gps + "/disable", credential=self.service), disabled)
        self.assertEqual(create(), (200, created))
        self.stop_api()
        self.start_api()
        self.assertEqual(create(), (200, created))
        self.assertEqual(self.db.tracking_sessions.count_documents(
            {"issuance.key_hash": session["issuance"]["key_hash"]}), 1)
        self.assertEqual(self.db.gps_positions.count_documents({"tracking_id": tracking_id}), 1)
        self.assertEqual(self.db.route_status.count_documents({}), 0)
        print("PHASE53_HTTP", json.dumps({"create": 201, "concurrent_replay": 200,
            "conflict": 409, "control": 200, "observation": 200,
            "disable": 200, "restart_replay": 200, "session_count": 1,
            "position_count": 1}))

    def test_disable_observation_race(self):
        path = "/api/v1/tracking-sessions"
        payload = {"guide_id": "phase53-race-guide", "route_id": str(self.route_id)}
        status, created = self.http("POST", path, payload, self.service, str(uuid4()))
        self.assertEqual(status, 201)
        gps = path + "/" + created["tracking_id"]
        observation = {"sequence": 1, "latitude": 4.0, "longitude": -74.0,
            "accuracy_m": 5.0, "captured_at": datetime.now(UTC).isoformat()}
        with ThreadPoolExecutor(max_workers=2) as pool:
            receive = pool.submit(self.http, "POST", gps + "/observations",
                                  observation, created["session_credential"])
            disable = pool.submit(self.http, "POST", gps + "/disable",
                                  None, self.service)
            received, disabled = receive.result(), disable.result()
        self.assertEqual((disabled[0], disabled[1]["state"]), (200, "disabled"))
        view = self.http("GET", gps + "/control", credential=self.service)[1]
        self.assertIn(received[0], (200, 409))
        if received[0] == 200:
            self.assertEqual(view["first_arrival"]["sequence"], 1)
        else:
            self.assertIsNone(view["first_arrival"])
        print("PHASE53_RACE", json.dumps({"receive_http": received[0],
                                          "disable_http": disabled[0]}))
