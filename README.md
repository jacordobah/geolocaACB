# geolocaACB

Componentes de geolocalización de Al Colegio en Bici, divididos por responsabilidad:

| Componente             | Responsabilidad                                                                                   | Rutas                                                                                      |
| ---------------------- | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `acb_path_lo` (Python) | Rutas geográficas, puntos de encuentro, importación KMZ, validación de PE01 y reportes de estado. | `/api/v1/routes`, `/api/v1/meeting-points`, `/api/v1/imports/kmz`, `/api/v1/status/notify` |
| `acb_gps_lo` (Go)      | Tracking GPS: sesiones, observaciones, posiciones, control y cierre.                              | `/api/v1/tracking-sessions`                                                                |

## Tracking

Las rutas importadas publican `geometry_version` y `association_status` para
que GPS pueda evaluar `OperationalReady`. Las rutas 541 inician en su primer PE;
las 542 inician en el colegio y terminan en su último PE.

El tracking GPS pertenece **exclusivamente** al componente Go. Los routers
`tracking.py` y `tracking_control.py` de `acb_path_lo` son legado obsoleto: se
conservan solo para no romper pruebas históricas y no deben usarse desde el
gateway ni desde los clientes.

Todo el tráfico pasa por `api-gateway:5062`, que publica las rutas de paths bajo
`/api/v1/{routes,meeting-points,imports,status}` y las de tracking bajo
`/api/v1/tracking-sessions`.

El GPS resuelve la geometría de una ruta llamando al gateway
(`GPS_GATEWAY_BASE_URL` + `GPS_GATEWAY_ROUTES_PATH`), nunca a `acb_path_lo` de
forma directa.

## Variables compartidas

- `GPS_GATEWAY_INTERNAL_TOKEN` (Go) debe ser igual a `GPS_INTERNAL_TOKEN` en
  `gatewayACB`.
- `ACB_FIRST_PE_RADIUS_M` y `ACB_FIRST_PE_MAX_ACCURACY_M` deben estar definidos
  para que `POST /api/v1/routes/{route_id}/first-meeting-point/validate` no
  responda 503. Los valores de `.env.example` son solo para pruebas locales.
- `GPS_GATEWAY_INTERNAL_TOKEN` / `GPS_CONTROL_CREDENTIAL_KEY` se configuran en
  `.env.example` con valores de desarrollo; cámbialos en despliegues reales.
- `GPS_CONTROL_API_ENABLED=true` y `GPS_READ_API_ENABLED=true` habilitan control
  y lectura; sin ellos, el gateway recibe la respuesta del propio GPS indicando
  que la API está deshabilitada.
