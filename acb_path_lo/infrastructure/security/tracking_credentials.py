import hashlib
import hmac
import secrets
from uuid import uuid4


def credential_fingerprint(credential: str) -> str:
    return hashlib.sha256(credential.encode("utf-8")).hexdigest()


def verify_credential(credential: str, expected_hash: str) -> bool:
    return hmac.compare_digest(credential_fingerprint(credential), expected_hash)


def create_tracking_credentials() -> tuple[str, str, str]:
    credential = secrets.token_urlsafe(32)  # 256 random bits; never stored as plaintext.
    return str(uuid4()), credential, credential_fingerprint(credential)
