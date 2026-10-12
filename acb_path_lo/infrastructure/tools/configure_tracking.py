"""Local isolated prototype control; no HTTP administrative control is exposed."""
import argparse
import asyncio
from dataclasses import asdict
from datetime import UTC, datetime
import json
import os

from pymongo import MongoClient

from ...application.use_cases.disable_tracking import DisableTracking
from ...application.use_cases.enable_tracking import EnableTracking
from ...application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from ...domain.model.tracking_session import TrackingView
from ...domain.tracking_errors import TrackingError
from ..config.tracking import load_tracking_policy
from ..persistence.mongo_map_repository import MongoMapRepository
from ..persistence.mongo_tracking_repository import MongoTrackingRepository
from ..security.tracking_credentials import create_tracking_credentials


async def run(args, database):
    repository = MongoTrackingRepository(database)
    clock = lambda: datetime.now(UTC)
    if args.action == "enable":
        policy = load_tracking_policy()
        if policy is None:
            raise TrackingError("tracking_configuration_unavailable")
        tracking_id, credential, fingerprint = create_tracking_credentials()
        maps = MongoMapRepository(database, ensure_indexes=False)
        session = await EnableTracking(repository, GetFirstMeetingPoint(maps), policy, clock).execute(
            tracking_id, args.guide_id, args.route_id, fingerprint)
        result = asdict(TrackingView.from_session(session))
        result["credential"] = credential  # Shown only on successful creation.
        return result
    if args.action == "disable":
        return asdict(await DisableTracking(repository, clock).execute(args.tracking_id))
    session = await repository.get(args.tracking_id)
    if session is None:
        raise TrackingError("tracking_session_unavailable")
    return asdict(TrackingView.from_session(session))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--confirm-isolated", action="store_true", required=True)
    actions = parser.add_subparsers(dest="action", required=True)
    enable = actions.add_parser("enable")
    enable.add_argument("--guide-id", required=True)
    enable.add_argument("--route-id", required=True)
    for action in ("disable", "show"):
        actions.add_parser(action).add_argument("--tracking-id", required=True)
    args = parser.parse_args()
    if (os.getenv("ACB_TEST_PROJECT") != "acb-kmz-test-20261012"
            or os.getenv("MONGO_URI") != "mongodb://acb_mongo_db:27017"
            or os.getenv("MONGO_DATABASE") != "geolocaacb"):
        parser.error("Only the explicitly verified isolated prototype database is allowed")
    try:
        with MongoClient(os.environ["MONGO_URI"], serverSelectionTimeoutMS=5000) as client:
            client.admin.command("ping")
            result = asyncio.run(run(args, client[os.environ["MONGO_DATABASE"]]))
    except TrackingError as error:
        parser.exit(1, error.code + "\n")
    print(json.dumps(result, default=lambda value: value.isoformat()))


if __name__ == "__main__":
    main()
