class TrackingError(Exception):
    def __init__(self, code: str):
        self.code = code
        super().__init__(code)


class TrackingStoreUnavailable(TrackingError):
    def __init__(self):
        super().__init__("tracking_store_unavailable")
