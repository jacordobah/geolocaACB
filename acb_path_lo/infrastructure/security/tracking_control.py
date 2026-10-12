"""Service authentication and recoverable GPS credential issuance."""
import base64
import hashlib
import hmac
import re
import secrets
from uuid import UUID

from .tracking_credentials import credential_fingerprint


TOKEN_PATTERN = re.compile(r"[A-Za-z0-9_-]{43}\Z")
HASH_PATTERN = re.compile(r"[0-9a-f]{64}\Z")
DOMAIN = b"ACB-TRACKING-GPS-CREDENTIAL-V1\0"


def _decode_token(value: str) -> bytes | None:
    if not isinstance(value, str) or TOKEN_PATTERN.fullmatch(value) is None:
        return None
    try:
        decoded = base64.urlsafe_b64decode(value + "=")
    except ValueError:
        return None
    if len(decoded) != 32 or base64.urlsafe_b64encode(decoded).rstrip(b"=").decode() != value:
        return None
    return decoded


def generate_service_credential() -> tuple[str, str]:
    """Provision outside the repository; the caller must keep the token private."""
    token = secrets.token_urlsafe(32)
    return token, credential_fingerprint(token)


def valid_service_hash(value: str | None) -> bool:
    return isinstance(value, str) and HASH_PATTERN.fullmatch(value) is not None


def verify_service_credential(value: str, expected_hash: str) -> bool:
    valid = _decode_token(value) is not None
    candidate = credential_fingerprint(value) if isinstance(value, str) else "0" * 64
    matched = hmac.compare_digest(candidate, expected_hash)
    return valid and matched


def decode_issuance_key(value: str | None) -> bytes | None:
    return _decode_token(value)


def new_nonce() -> str:
    return secrets.token_urlsafe(32)


def derive_gps_credential(key: bytes, tracking_id: str, nonce: str) -> str:
    nonce_bytes = _decode_token(nonce)
    if len(key) != 32 or nonce_bytes is None:
        raise ValueError("Invalid issuance material")
    digest = hmac.digest(key, DOMAIN + UUID(tracking_id).bytes + nonce_bytes, "sha256")
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")
