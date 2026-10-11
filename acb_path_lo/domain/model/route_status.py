from dataclasses import dataclass
from datetime import datetime
from enum import Enum

class RouteState(str, Enum):
    DISPONIBLE_PARA_SERVICIO = "58"
    INFORME_NOVEDADES = "512"
    UBICACION_ACTUAL = "520"
    HE_LLEGADO_AL_SITIO = "523"
    MISION_CUMPLIDA = "524"
    APOYE_POLICIA = "537"
    RUTA_CONFIANZA_HACIA_EL_COLEGIO = "541"
    RUTA_CONFIANZA_SALIDA_DEL_COLEGIO = "542"
    ACCIDENTE_GUIA_EN_BICICLETA = "550"

@dataclass
class RouteStatus:
    name: str
    route: str
    status: RouteState
    user_id: str
    notified_at: datetime | None = None