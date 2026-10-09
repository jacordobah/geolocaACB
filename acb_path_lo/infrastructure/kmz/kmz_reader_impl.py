from io import BytesIO
from zipfile import ZipFile, BadZipFile
import xml.etree.ElementTree as ET

from ...domain.model.route import Route
from ...domain.model.meeting_point import MeetingPoint


NS = {"kml": "http://www.opengis.net/kml/2.2"}


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

                    for placemark in root.findall(
                        ".//kml:Placemark", NS
                    ):
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
                                    )
                                )

                return routes, meeting_points

        except BadZipFile as error:
            raise ValueError("The file is not a valid KMZ archive") from error

        except ET.ParseError as error:
            raise ValueError("The KML contains invalid XML") from error