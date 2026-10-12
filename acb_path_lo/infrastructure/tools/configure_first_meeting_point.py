"""Local prototype tool. Requires explicit confirmation and the isolated DB."""
import argparse
import asyncio
import os

from pymongo import MongoClient

from ...application.use_cases.set_first_meeting_point import SetFirstMeetingPoint
from ..persistence.mongo_map_repository import MongoMapRepository


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--route-id", required=True)
    parser.add_argument("--point-id", required=True)
    parser.add_argument("--confirm", action="store_true", required=True)
    args = parser.parse_args()
    if os.getenv("ACB_TEST_PROJECT") != "acb-kmz-test-20261012":
        parser.error("Only the explicitly verified isolated test project is allowed")
    if (os.getenv("MONGO_URI") != "mongodb://acb_mongo_db:27017"
            or os.getenv("MONGO_DATABASE") != "geolocaacb"):
        parser.error("Unexpected local test database configuration")
    with MongoClient(os.environ["MONGO_URI"], serverSelectionTimeoutMS=5000) as client:
        client.admin.command("ping")
        repository = MongoMapRepository(client[os.environ["MONGO_DATABASE"]])
        asyncio.run(SetFirstMeetingPoint(repository).execute(args.route_id, args.point_id))
    print("First meeting point association configured (or already identical)")


if __name__ == "__main__":
    main()
