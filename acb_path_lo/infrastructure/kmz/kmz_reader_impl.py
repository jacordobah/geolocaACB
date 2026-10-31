from io import BytesIO
from zipfile import ZipFile, BadZipFile
import re
import xml.etree.ElementTree as ET

from ...domain.model.route import Route
from ...domain.model.meeting_point import MeetingPoint
from ...domain.model.point_kind import PointKind
from ...domain.services.route_metadata import day_shift, route_group_code
from ...domain.services.text_matching import normalize_label

NS = {"kml": "http://www.opengis.net/kml/2.2"}

MEETING_POINT_PATTERN = re.compile(r"^PE\s*\d+$")
WALKING_SEGMENT_PATTERN = re.compile(r"^TC\s*\d+$")
RISK_POINT_PATTERN = re.compile(r"^PR\s*\d+$")
ROUTE_CODE_PATTERN = re.compile(r"^\s*(\d{3})\b")

STUDENT_FOLDER_MARKERS = (
    "GEOREFERENCIACION",
    "GEORREFERENCIACION",
    "GRADUADOS",
)
MEETING_POINT_FOLDER_MARKERS = (
    "PUNTOS DE ENCUENTRO",
    "PUNTOS DE ENCUENTO",
)
SCHOOL_MARKERS = (
    "COLEGIO",
    "LICEO",
    "SEDE",
    "INSTITUCION EDUCATIVA",
    "CENTRO EDUCATIVO",
)


def read_coordinates(element) -> list[tuple[float, float]]:
    text = element.findtext("kml:coordinates", namespaces=NS)

    if not text:
        return []

    coordinates = []

    for point in text.split():
        values = point.split(",")

        if len(values) < 2:
            raise ValueError("Invalid KML coordinate")

        longitude = float(values[0])
        latitude = float(values[1])

        if not (-180 <= longitude <= 180):
            raise ValueError("Longitude is out of range")

        if not (-90 <= latitude <= 90):
            raise ValueError("Latitude is out of range")

        coordinates.append((longitude, latitude))

    return coordinates


def classify_point(name: str, folder_path: tuple[str, ...]) -> PointKind:
    normalized_name = normalize_label(name)
    normalized_folder = " / ".join(normalize_label(segment) for segment in folder_path)
    tokens = normalized_name.replace("/", " ").split()

    if "INICIO" in tokens:
        return PointKind.EXPLICIT_START
    if "FIN" in tokens:
        return PointKind.EXPLICIT_END
    if MEETING_POINT_PATTERN.match(normalized_name) or any(
        marker in normalized_folder for marker in MEETING_POINT_FOLDER_MARKERS
    ):
        return PointKind.MEETING_POINT
    if WALKING_SEGMENT_PATTERN.match(normalized_name):
        return PointKind.WALKING_SEGMENT
    if RISK_POINT_PATTERN.match(normalized_name):
        return PointKind.RISK_POINT
    if normalized_name.isdigit() or any(
        marker in normalized_folder for marker in STUDENT_FOLDER_MARKERS
    ):
        return PointKind.STUDENT
    if (
        "FORTALECIMIENTO" in normalized_name
        or "PARQUE" in tokens
        or "PARQUEADERO" in tokens
        or "BANO" in normalized_name
        or re.fullmatch(r"PH\s*\d+", normalized_name)
    ):
        return PointKind.SUPPORT
    if "IED" in tokens or any(marker in normalized_name for marker in SCHOOL_MARKERS):
        return PointKind.SCHOOL
    return PointKind.OTHER


def route_code(name: str) -> str | None:
    match = ROUTE_CODE_PATTERN.match(name)
    return match.group(1) if match else None


def _is_school_route_folder(folder_path: tuple[str, ...]) -> bool:
    segments = tuple(normalize_label(segment) for segment in folder_path)
    return (
        "RUTAS AL COLEGIO EN BICI" in segments
        and ("RUTA" in segments or "RUTAS" in segments)
    )


def _placemarks(element, folder_path: tuple[str, ...]):
    for child in element:
        tag = child.tag.rsplit("}", 1)[-1]
        if tag == "Folder":
            name = child.findtext("kml:name", default="", namespaces=NS).strip()
            yield from _placemarks(child, folder_path + (name,))
        elif tag == "Document":
            yield from _placemarks(child, folder_path)
        elif tag == "Placemark":
            yield child, folder_path


class KmzReaderImpl:

    def read(
        self, content: bytes
    ) -> tuple[list[Route], list[MeetingPoint]]:

        try:
            with ZipFile(BytesIO(content)) as file:
                kml_files = [
                    name for name in file.namelist()
                    if name.lower().endswith(".kml")
                    and not name.startswith("__MACOSX/")
                ]

                if not kml_files:
                    raise ValueError("The KMZ does not contain any KML files")

                # Process every KML file in the archive.
                routes = []
                meeting_points = []

                for kml_name in kml_files:
                    root = ET.fromstring(file.read(kml_name))

                    for placemark, folder_path in _placemarks(root, ()):
                        name = placemark.findtext(
                            "kml:name",
                            default="Unnamed",
                            namespaces=NS,
                        )

                        description = placemark.findtext(
                            "kml:description",
                            namespaces=NS,
                        )

                        # Each LineString represents one route.
                        for line_string in placemark.findall(
                            ".//kml:LineString", NS
                        ):
                            coordinates = read_coordinates(line_string)

                            if len(coordinates) >= 2:
                                routes.append(
                                    Route(
                                        name=name,
                                        coordinates=coordinates,
                                        code=route_code(name),
                                        folder_path=folder_path,
                                        route_code=route_group_code(folder_path),
                                        day_shift=day_shift(folder_path),
                                    )
                                )

                        # Each Point represents one meeting point.
                        for point_element in placemark.findall(
                            ".//kml:Point", NS
                        ):
                            coordinates = read_coordinates(point_element)

                            if len(coordinates) == 1:
                                longitude, latitude = coordinates[0]

                                meeting_points.append(
                                    MeetingPoint(
                                        name=name,
                                        longitude=longitude,
                                        latitude=latitude,
                                        description=description,
                                        kind=classify_point(name, folder_path),
                                        folder_path=folder_path,
                                    )
                                )

                route_folders = {
                    route.folder_path for route in routes
                    if _is_school_route_folder(route.folder_path)
                }
                for point in meeting_points:
                    if point.kind is PointKind.OTHER and point.folder_path in route_folders:
                        point.kind = PointKind.SCHOOL

                return routes, meeting_points

        except BadZipFile as error:
            raise ValueError("The file is not a valid KMZ archive") from error

        except ET.ParseError as error:
            raise ValueError("The KML contains invalid XML") from error
