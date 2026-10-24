import unicodedata


def normalize_label(value: str) -> str:
    """Upper-case ASCII form used to compare KMZ folder and placemark labels."""
    decomposed = unicodedata.normalize("NFKD", value)
    without_accents = decomposed.encode("ascii", "ignore").decode("ascii")
    return " ".join(without_accents.replace(".", "").upper().split())
