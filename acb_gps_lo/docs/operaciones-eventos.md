# Eventos GPS → Operaciones (contrato provisional)

El despacho HTTP está implementado y probado con `httptest`, pero **deshabilitado por defecto**. La ruta receptora, autenticación y semántica del acuse deben acordarse con `ACB_Operation_Lo` antes de habilitarlo. GPS detecta y entrega acontecimientos; Operaciones decide reglas y notificaciones administrativas.

Solicitud propuesta: `POST` a `GPS_GATEWAY_BASE_URL` + `GPS_GATEWAY_EVENTS_PATH` (ruta absoluta completa acordada con Gateway), `Authorization: Bearer <secreto-servicio>`, `Idempotency-Key: <event_id>`, `Content-Type: application/json`, cuerpo `{"schema_version":"gps.event.v1","event":{...}}`. GPS sólo envía HTTP al Gateway; éste debe encaminar hacia Operaciones sin alterar el sobre. Se entrega **al menos una vez**: reintentos y pérdidas de respuesta pueden repetir el mismo `event_id`. El receptor debe persistir de manera atómica su deduplicación por `event_id` junto con el efecto y responder HTTP 200/201 JSON `{"schema_version":"gps.ack.v1","event_id":"...","status":"persisted"}` o `"already_persisted"`. Un 2xx del Gateway sin ese acuse exacto **no** confirma entrega. No hay garantía extremo a extremo de efecto único hasta verificar el receptor real.

Campos comunes obligatorios: `event_id` (SHA-256 estable), `event_type`, `tracking_id`, `trip_id`, `guide_id`, `geo_route_id`, `occurred_at`. Los eventos geográficos también llevan `sequence`, `captured_at`, `observed_from`, `evidence_count`, `geometry_fingerprint`, `snapshot_hash`, `longitude`, `latitude`, `accuracy_meters`, `route_distance_meters`; los de llegada llevan además `meeting_point_id`, `meeting_point_order`, `meeting_point_distance_meters`; los desvíos llevan `episode_start_sequence`. `geometry_version` es opcional cuando la ruta la provee oficialmente. Los eventos de ciclo técnico **no** llevan secuencia, captura, coordenadas ni evidencia GPS inventada; `snapshot_hash` es opcional cuando la sesión no tenía instantánea.

Ejemplos mínimos (IDs abreviados sólo para lectura; en producción `event_id` es hexadecimal de 64 caracteres):

```json
{"event_type":"tracking_session_activated","event_id":"<64 hex>","tracking_id":"t1","trip_id":"trip1","guide_id":"guide1","geo_route_id":"r1","occurred_at":"2026-11-02T20:00:00Z","snapshot_hash":"<64 hex>"}
{"event_type":"tracking_session_finished","event_id":"<64 hex>","tracking_id":"t1","trip_id":"trip1","guide_id":"guide1","geo_route_id":"r1","occurred_at":"2026-11-02T21:00:00Z"}
{"event_type":"first_meeting_point_arrival","event_id":"<64 hex>","tracking_id":"t1","trip_id":"trip1","guide_id":"guide1","geo_route_id":"r1","meeting_point_id":"PE01","meeting_point_order":1,"sequence":2,"captured_at":"2026-11-02T20:01:00Z","occurred_at":"2026-11-02T20:01:01Z","observed_from":"2026-11-02T20:00:59Z","evidence_count":2,"geometry_fingerprint":"<64 hex>","snapshot_hash":"<64 hex>","longitude":-74.1,"latitude":4.6,"accuracy_meters":5,"route_distance_meters":0,"meeting_point_distance_meters":1}
```

`meeting_point_arrival` usa la misma estructura que la primera llegada, con otro PE y orden. `route_deviation_started` y `route_deviation_resolved` usan la estructura geográfica sin campos de PE y con `episode_start_sequence`; el evento de resolución conserva la referencia al episodio. `tracking_session_activated` representa el aprovisionamiento técnico y **no** el inicio administrativo ni la primera posición. `tracking_session_finished` representa cierre técnico y revocación, no el final administrativo del recorrido.

Outbox Redis: `pending` → `in_flight` (reclamación con token y lease) → `delivered`, `pending` reprogramado o `blocked`. Un trabajador viejo no puede confirmar una reclamación vencida. Timeouts, conexión, 429 y 5xx reintentan con espera creciente y variación; errores de contrato o autorización bloquean sin borrar el sobre. Eventos pendientes y bloqueados no tienen TTL de historial GPS. Tras acuse, el sobre y la evidencia de entrega conservan un TTL técnico configurable (`GPS_OUTBOX_DELIVERED_RETENTION`, 168 h por defecto), pendiente de acuerdo. Al finalizar sesión o expirar posiciones, los eventos sin acuse siguen disponibles.

Gateway y Operaciones deben acordar: ruta del Gateway, transporte/TLS, credencial y rotación, preservación del sobre e `Idempotency-Key`, propagación del acuse durable, esquema definitivo, deduplicación, retención posterior y tratamiento de errores permanentes. La conexión HTTP directa GPS → Operaciones no está permitida. El envío real no debe activarse hasta completar estas verificaciones.

## Intercambio HTTP íntegro y ejemplos por tipo

**Estado:** contrato provisional implementado en el emisor y probado con receptor `httptest`; no aprobado ni implementado en Operaciones real. GPS sólo comunica acontecimientos; Operaciones debe persistir, deduplicar y gestionar sus propias decisiones/notificaciones. La ruta del Gateway es `GPS_GATEWAY_EVENTS_PATH`, cuyo valor se acordará. El cliente exige HTTP 200 o 201, `Content-Type: application/json` y acuse exacto sin campos extra.

Ejemplo de solicitud de activación técnica (los IDs son sintéticos; el `event_id` de 64 hex corresponde al cálculo estable del código para `t-demo`):

```http
POST /ruta-provisional-de-eventos HTTP/1.1
Host: gateway.example.test
Authorization: Bearer <credencial-servicio-gps>
Idempotency-Key: 1c05dc3e9616ea862ab2579749c470b76db61f0ca600d9ea72a87795d015034c
Accept: application/json
Content-Type: application/json

{"schema_version":"gps.event.v1","event":{"event_id":"1c05dc3e9616ea862ab2579749c470b76db61f0ca600d9ea72a87795d015034c","event_type":"tracking_session_activated","tracking_id":"t-demo","trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","occurred_at":"2026-11-02T20:00:00Z","snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json

{"schema_version":"gps.ack.v1","event_id":"1c05dc3e9616ea862ab2579749c470b76db61f0ca600d9ea72a87795d015034c","status":"persisted"}
```

Eventos adicionales completos, cada línea es el objeto `event` dentro del mismo sobre `gps.event.v1`. `geometry_fingerprint` y `snapshot_hash` son huellas locales sintéticas de 64 hex, **no** versiones oficiales de Python. `geometry_version` sólo aparece si la proporciona el propietario geográfico; se omite en estos ejemplos.

```json
{"event_id":"167a669ffb7f555025638d217e57bd574dc31d9d1c3bb5b49fd3c997a56a19d5","event_type":"tracking_session_finished","tracking_id":"t-demo","trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","occurred_at":"2026-11-02T21:00:00Z","snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
{"event_id":"f168e05454e72bc13dadb9a4d98562973c20df73b49b9ab3d05a566b0b38f350","event_type":"first_meeting_point_arrival","tracking_id":"t-demo","trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","meeting_point_id":"PE01","meeting_point_order":1,"sequence":2,"occurred_at":"2026-11-02T20:01:01Z","captured_at":"2026-11-02T20:01:00Z","observed_from":"2026-11-02T20:00:58Z","evidence_count":2,"geometry_fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","longitude":-74.1,"latitude":4.6,"accuracy_meters":5,"route_distance_meters":2,"meeting_point_distance_meters":3}
{"event_id":"aa705542287dfcdd3850d71b34b10370f83ccc4fa511d9badefad9338c115c63","event_type":"meeting_point_arrival","tracking_id":"t-demo","trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","meeting_point_id":"PE02","meeting_point_order":2,"sequence":4,"occurred_at":"2026-11-02T20:03:01Z","captured_at":"2026-11-02T20:03:00Z","observed_from":"2026-11-02T20:02:58Z","evidence_count":2,"geometry_fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","longitude":-74.101,"latitude":4.601,"accuracy_meters":5,"route_distance_meters":3,"meeting_point_distance_meters":4}
{"event_id":"d4142297ceaa0c4e192545879f519b42a04748c5a6cc44a52ce2093e3f6e500e","event_type":"route_deviation_started","tracking_id":"t-demo","trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","sequence":6,"occurred_at":"2026-11-02T20:05:01Z","captured_at":"2026-11-02T20:05:00Z","observed_from":"2026-11-02T20:04:58Z","evidence_count":2,"geometry_fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","longitude":-74.12,"latitude":4.62,"accuracy_meters":5,"route_distance_meters":80,"episode_start_sequence":6}
{"event_id":"15a6c3f082b553f44834b65fb8eebce8e862fc49a6cbf8bed8a93f72c328dd7c","event_type":"route_deviation_resolved","tracking_id":"t-demo","trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","sequence":8,"occurred_at":"2026-11-02T20:07:01Z","captured_at":"2026-11-02T20:07:00Z","observed_from":"2026-11-02T20:06:58Z","evidence_count":2,"geometry_fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","longitude":-74.101,"latitude":4.601,"accuracy_meters":5,"route_distance_meters":2,"episode_start_sequence":6}
```

Los campos comunes obligatorios son `event_id`, `event_type`, `tracking_id`, `trip_id`, `guide_id`, `geo_route_id`, `occurred_at`. Los eventos técnicos omiten coordenadas, `sequence`, `captured_at` y evidencia; `snapshot_hash` es opcional para ellos. Los geográficos incluyen siempre secuencia, tiempos de captura y comienzo de evidencia, cantidad, huellas, coordenadas, precisión y distancia a ruta. Llegadas añaden PE, orden y distancia; desvíos añaden `episode_start_sequence`. El identificador `geometry_version` es opcional y no se inventa. Ninguna llegada técnica implica cumplimiento administrativo.

**Entrega repetida:** si Operaciones persistió el evento pero la respuesta se perdió, GPS reenvía el sobre original con el **mismo** `event_id` y `Idempotency-Key`. El receptor debe usar una restricción de unicidad durable por `event_id` y registrar el efecto de negocio junto con la deduplicación; entonces puede responder 200 `{"schema_version":"gps.ack.v1","event_id":"1c05dc3e9616ea862ab2579749c470b76db61f0ca600d9ea72a87795d015034c","status":"already_persisted"}`. Un 2xx sin JSON correcto, ID distinto o estado distinto queda `blocked`; 408/429/5xx y fallos de red se reintentan. No hay garantía de efecto único sin el receptor real.

**Verificación:** `go test ./internal/operationshttp ./internal/outbox`, y el flujo con receptor simulado `TestIsolatedContractFlow` según [pruebas-integracion.md](pruebas-integracion.md). Antes de `GPS_OPERATIONS_DELIVERY_ENABLED=true`, Operaciones debe demostrar persistencia y deduplicación durable, acuse exacto, credencial de servicio, TLS y tratamiento de repetidos; tener una URL y token no significa contrato aprobado.
