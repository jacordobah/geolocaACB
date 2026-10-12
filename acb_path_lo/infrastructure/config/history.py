import os
import re

from ...domain.model.gps_position import HistoryPolicy


def load_history_policy(environment=None) -> HistoryPolicy | None:
    values = os.environ if environment is None else environment
    cutoff = values.get("ACB_GPS_HISTORY_CUTOFF_BOGOTA")
    if not isinstance(cutoff, str) or re.fullmatch(r"\d{2}:\d{2}", cutoff) is None:
        return None
    try:
        return HistoryPolicy(int(cutoff[:2]), int(cutoff[3:]))
    except ValueError:
        return None
