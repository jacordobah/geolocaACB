from io import BytesIO
import unittest
from zipfile import ZipFile

from acb_path_lo.domain.model.point_kind import PointKind
from acb_path_lo.infrastructure.kmz.kmz_reader_impl import KmzReaderImpl


def make_kmz(kml: str, extra: dict[str, str] | None = None) -> bytes:
    archive = BytesIO()
    with ZipFile(archive, "w") as kmz:
        kmz.writestr("doc.kml", kml)
        for name, content in (extra or {}).items():
            kmz.writestr(name, content)
    return archive.getvalue()


KML = """<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2">
  <Document>
    <Folder>
      <name>RUTAS AL COLEGIO EN BICI</name>
      <Folder>
        <name>MM01 – COLEGIO DEMO</name>
        <Folder>
          <name>RUTAS</name>
          <Folder>
            <name>MM01 – COLEGIO DEMO</name>
            <Placemark>
              <name>541 RUTA DEMO</name>
              <LineString>
                <coordinates>-74.1,4.6,0 -74.2,4.7,0</coordinates>
              </LineString>
            </Placemark>
            <Placemark>
              <name>542 RUTA DEMO</name>
              <LineString>
                <coordinates>-74.2,4.7,0 -74.1,4.6,0</coordinates>
              </LineString>
            </Placemark>
            <Placemark>
              <name>COLEGIO DEMO</name>
              <Point><coordinates>-74.2,4.7,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>ZONA VERDE</name>
              <Point><coordinates>-74.15,4.65,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>PUNTO DE INICIO</name>
              <Point><coordinates>-74.1,4.6,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>PUNTO FIN</name>
              <Point><coordinates>-74.2,4.7,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>TC01</name>
              <Point><coordinates>-74.15,4.65,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>PR01</name>
              <Point><coordinates>-74.15,4.65,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>12345678</name>
              <Point><coordinates>-74.15,4.65,0</coordinates></Point>
            </Placemark>
            <Placemark>
              <name>PARQUE</name>
              <Point><coordinates>-74.15,4.65,0</coordinates></Point>
            </Placemark>
            <Folder>
              <name>PUNTOS DE ENCUENTRO</name>
              <Folder>
                <name>PUNTOS DE ENCUENTRO 541</name>
                <Placemark>
                  <name>PE01</name>
                  <Point><coordinates>-74.1,4.6,0</coordinates></Point>
                </Placemark>
              </Folder>
              <Folder>
                <name>PUNTOS DE ENCUENTRO 542 B</name>
                <Placemark>
                  <name>PE04</name>
                  <Point><coordinates>-74.2,4.7,0</coordinates></Point>
                </Placemark>
              </Folder>
            </Folder>
          </Folder>
        </Folder>
      </Folder>
    </Folder>
  </Document>
</kml>"""


SINGULAR_KML = """<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2">
  <Folder>
    <name>RUTAS AL COLEGIO EN BICI</name>
    <Folder>
      <name>MM03 - COLEGIO DEMO</name>
      <Folder>
        <name>RUTA</name>
        <Placemark>
          <name>541 RUTA DEMO</name>
          <LineString>
            <coordinates>-74.1,4.6,0 -74.2,4.7,0</coordinates>
          </LineString>
        </Placemark>
        <Folder>
          <name>PUNTOS DE ENCUENTO</name>
          <Folder>
            <name>PUNTOS DE ENCUENTO 541</name>
            <Placemark>
              <name>PE01</name>
              <Point><coordinates>-74.1,4.6,0</coordinates></Point>
            </Placemark>
          </Folder>
        </Folder>
      </Folder>
    </Folder>
  </Folder>
</kml>"""


EDGE_KML = """<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2">
  <Document>
    <Placemark>
      <name>541 RUTA DEMO</name>
      <LineString>
        <coordinates>-74.1,4.6,0 -74.2,4.7,0</coordinates>
      </LineString>
    </Placemark>
    <Placemark>
      <name>RUTA INVALIDA</name>
      <LineString>
        <coordinates>-74.1,4.6,0</coordinates>
      </LineString>
    </Placemark>
  </Document>
</kml>"""


class KmzReaderTests(unittest.TestCase):
    def test_reader_preserves_folder_metadata_and_point_kinds(self):
        routes, points = KmzReaderImpl().read(make_kmz(KML))

        self.assertEqual(len(routes), 2)
        self.assertEqual([route.code for route in routes], ["541", "542"])
        self.assertEqual([route.route_code for route in routes], ["MM01", "MM01"])
        self.assertEqual(routes[0].folder_path[-1], "MM01 – COLEGIO DEMO")
        self.assertIn("RUTAS", routes[0].folder_path)

        by_name = {point.name: point for point in points}
        self.assertEqual(by_name["PE01"].kind, PointKind.MEETING_POINT)
        self.assertEqual(
            by_name["PE01"].folder_path[-1], "PUNTOS DE ENCUENTRO 541",
        )
        self.assertEqual(by_name["PE04"].kind, PointKind.MEETING_POINT)
        self.assertEqual(
            by_name["PE04"].folder_path[-1], "PUNTOS DE ENCUENTRO 542 B",
        )
        self.assertEqual(by_name["COLEGIO DEMO"].kind, PointKind.SCHOOL)
        self.assertEqual(by_name["ZONA VERDE"].kind, PointKind.SCHOOL)
        self.assertEqual(by_name["PUNTO DE INICIO"].kind, PointKind.EXPLICIT_START)
        self.assertEqual(by_name["PUNTO FIN"].kind, PointKind.EXPLICIT_END)
        self.assertEqual(by_name["TC01"].kind, PointKind.WALKING_SEGMENT)
        self.assertEqual(by_name["PR01"].kind, PointKind.RISK_POINT)
        self.assertEqual(by_name["12345678"].kind, PointKind.STUDENT)
        self.assertEqual(by_name["PARQUE"].kind, PointKind.SUPPORT)

    def test_reader_accepts_singular_ruta_and_meeting_point_folder_typo(self):
        routes, points = KmzReaderImpl().read(make_kmz(SINGULAR_KML))

        self.assertEqual(len(routes), 1)
        self.assertEqual(routes[0].route_code, "MM03")
        self.assertIn("RUTA", routes[0].folder_path)
        self.assertEqual(points[0].name, "PE01")
        self.assertEqual(points[0].kind, PointKind.MEETING_POINT)
        self.assertEqual(points[0].folder_path[-1], "PUNTOS DE ENCUENTO 541")

    def test_reader_ignores_short_linestrings_and_macosx_files(self):
        extra = {
            "more.kml": """<kml xmlns="http://www.opengis.net/kml/2.2">
              <Placemark>
                <name>PE99</name>
                <Point><coordinates>-74.1,4.6,0</coordinates></Point>
              </Placemark>
            </kml>""",
            "__MACOSX/ignored.kml": """<kml xmlns="http://www.opengis.net/kml/2.2">
              <Placemark>
                <name>PE98</name>
                <Point><coordinates>-74.1,4.6,0</coordinates></Point>
              </Placemark>
            </kml>""",
        }

        routes, points = KmzReaderImpl().read(make_kmz(EDGE_KML, extra))

        self.assertEqual([route.name for route in routes], ["541 RUTA DEMO"])
        self.assertEqual([point.name for point in points], ["PE99"])


if __name__ == "__main__":
    unittest.main()
