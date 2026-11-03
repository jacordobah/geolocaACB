# Integración del controlador administrativo de seguimiento

**Estado del catálogo:** `policy_id` debe ser una identidad versionada que termine en `-vN`, con `revision: N` en el YAML. El archivo sintético incluido no autoriza su uso en recorridos reales. El cargador está implementado; falta aprobar políticas operativas y conectar un verificador de identidad que permita controlar la selección de cada ID. Los ejemplos históricos de esta guía con IDs sin versión son ilustrativos del JSON, pero no seleccionan ninguna entrada del catálogo actual. Véase [políticas YAML](politicas-yaml.md).

El componente que autoriza los recorridos todavía no está identificado. `ACB_Operation_Lo` recibe acontecimientos; no se le atribuye creación o cierre. Los handlers de control están implementados, pero el binario **no los registra** y `GPS_CONTROL_API_ENABLED=true` falla al iniciar. El siguiente contrato describe código probado con verificadores simulados, no una API disponible en despliegue operativo.

1. Obtener una credencial de servicio verificada por un futuro proveedor; audiencia `acb-gps-control`, identidad, vencimiento, capacidad `tracking:create` y alcance `trip_id`. El Bearer de observación no sirve para control. Acordar TLS y distribución de secretos fuera del repositorio.
2. Obtener del propietario administrativo autorización de inicio, `trip_id`, `guide_id`, `geo_route_id` y `policy_id` aprobado. GPS no comprueba reglas de negocio del recorrido; comprueba el alcance de la identidad y `OperationalReady` de la ruta.
3. Enviar `POST /api/v1/tracking-sessions` con `Authorization: Bearer <credencial-servicio>`, `Idempotency-Key: <clave-opaca-estable>`, `Content-Type: application/json` y cuerpo `{"trip_id":"trip-1","guide_id":"guide-1","geo_route_id":"route-1","policy_id":"policy-1"}`. La clave debe permanecer igual en todos los reintentos de la **misma** solicitud. El cuerpo máximo es 4096 bytes y no se admiten campos desconocidos.
4. Respuesta 201 nueva o 200 repetida: `tracking_id`, `session` y `credential_status`. `observation_credential` se entrega únicamente durante la ventana autorizada de emisión; `credential_status` puede ser `issued`, `reissue_required` o `revoked`. La respuesta incluye `Cache-Control: no-store`. Una respuesta perdida se reintenta con la misma clave; no se crea otra sesión. Si la ventana caducó, no se recupera el texto claro mediante esta API: requiere acordar rotación/autorización explícita antes de operación real.
5. Entregar la credencial de observación de forma segura a Presentación. No guardarla en logs, URL, métricas o mensajes. GPS conserva el hash SHA-256 de la credencial; la derivación original usa HMAC-SHA256 y clave externa de al menos 32 bytes. Perder/rotar esa clave impide reemitir la misma credencial; las sesiones activas que ya la poseen pueden continuar hasta el cierre. No hay mecanismo operativo de rotación implementado.
6. Consultar `GET /api/v1/tracking-sessions/{tracking_id}` con `tracking:read` y alcance de seguimiento o recorrido. Finalizar con `POST /api/v1/tracking-sessions/{tracking_id}/finish` y `tracking:finish`; cuerpo vacío. La repetición de cierre es idempotente. El cierre revoca observaciones, conserva historial sujeto a 24 h y genera `tracking_session_finished` interno.

Errores: 400 JSON/clave/cuerpo inválido; 401 sin identidad verificable; 403 capacidad ausente; 404 ruta o sesión ajena/inexistente; 409 misma clave con otra solicitud; 415 tipo de contenido; 422 ruta no apta; 503 dependencia o resultado de escritura incierto (`Retry-After: 1`). Una respuesta 503 **no** autoriza crear otra solicitud con una clave nueva. La unicidad administrativa de una sesión activa por recorrido aún requiere acuerdo.

Las reservas de idempotencia Redis no tienen TTL hasta acordar el horizonte máximo de reintentos y una política de tombstones. Esto evita duplicación por reutilización prematura de clave, pero exige dimensionar almacenamiento y mantenimiento futuros.

## Contrato HTTP completo de referencia

La responsabilidad del consumidor es autorizar el recorrido, proporcionar `trip_id`, `guide_id`, `geo_route_id` y un `policy_id` aprobado, conservar la misma `Idempotency-Key` ante reintentos y entregar de forma segura la credencial al dispositivo. Los cuatro campos son obligatorios y no se admiten campos desconocidos. Los ejemplos siguientes son **sintéticos** y sólo funcionan hoy con el handler conectado por un fixture de prueba:

```http
POST /api/v1/tracking-sessions HTTP/1.1
Host: gps.example.test
Authorization: Bearer <credencial-de-servicio-verificada>
Idempotency-Key: inicio-trip-demo-1
Content-Type: application/json

{"trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","policy_id":"politica-demo"}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"tracking_id":"t-demo","session":{"id":"t-demo","guide_id":"guia-demo","route_id":"ruta-demo","trip_id":"trip-demo","state":"active","started_at":"2026-11-02T20:00:00Z","last_sequence":0,"max_observation_age":900000000000,"future_skew":30000000000,"snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"observation_credential":"<secreto-entregado-en-canal-seguro>","credential_status":"issued"}
```

El ID real de sesión es aleatorio y la huella se calcula; los valores se muestran para explicar la estructura. Las duraciones de `session` se serializan como nanosegundos enteros. El evento de activación es técnico y no prueba inicio administrativo ni primera posición.

```http
GET /api/v1/tracking-sessions/t-demo HTTP/1.1
Host: gps.example.test
Authorization: Bearer <servicio-con-tracking:read-y-alcance>
```

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"session":{"id":"t-demo","guide_id":"guia-demo","route_id":"ruta-demo","trip_id":"trip-demo","state":"active","started_at":"2026-11-02T20:00:00Z","last_sequence":0,"max_observation_age":900000000000,"future_skew":30000000000,"snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
```

```http
POST /api/v1/tracking-sessions/t-demo/finish HTTP/1.1
Host: gps.example.test
Authorization: Bearer <servicio-con-tracking:finish-y-alcance>
Content-Length: 0
```

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"session":{"id":"t-demo","guide_id":"guia-demo","route_id":"ruta-demo","trip_id":"trip-demo","state":"finished","started_at":"2026-11-02T20:00:00Z","finished_at":"2026-11-02T21:00:00Z","last_sequence":0,"max_observation_age":900000000000,"future_skew":30000000000,"snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
```

Un reintento de creación con identidad, clave y contenido idénticos devuelve 200 y la misma sesión. Dentro de la ventana de emisión devuelve `credential_status: issued`; fuera, `observation_credential: ""` y `credential_status: reissue_required`; si la sesión terminó, `revoked`. No hay endpoint de rotación ni reemisión tardía. El cierre repetido devuelve 200 sin nuevo efecto. La credencial se deriva mediante HMAC-SHA256 de una clave externa de al menos 32 bytes; Redis conserva su hash SHA-256. La pérdida o rotación de la clave impide reemisión, aunque una credencial ya entregada sigue siendo válida hasta la revocación. No existe todavía variable operativa que conecte esa clave al binario.

**Flujo fallido:** si se repite `inicio-trip-demo-1` con otro `guide_id`, el handler devuelve 409 `{"error":"idempotency_conflict"}` y no crea una segunda sesión. Si Python entrega una ruta consultable pero no `OperationalReady`, devuelve 422 `{"error":"route_ineligible"}`. Si Redis o el resultado de escritura son inciertos, devuelve 503 `{"error":"temporarily_unavailable"}` con `Retry-After: 1`: reintentar **la misma** solicitud y clave, nunca inventar otra clave. Una ruta inexistente devuelve 404; JSON/campos/ID inválidos 400; cuerpo >4096 bytes 413; media type distinto de JSON 415; credencial no verificada 401; capacidad insuficiente 403; sesión ajena 404. Una respuesta Python mal formada puede mapearse a 503, no necesariamente a 422.

**Verificación:** usar los tests `internal/control` y `internal/httpapi` con Redis aislado según [pruebas-integracion.md](pruebas-integracion.md). Tras integrar el proveedor real, comprobar 201/200/409, protección 401/403/404, 422/503, revocación y `Cache-Control: no-store`. El 404 del servidor operativo actual para estas rutas procede de `net/http` porque los handlers no están registrados; no debe interpretarse como el JSON `not_found` del contrato. `GPS_CONTROL_API_ENABLED=true` **no habilita** la API: actualmente impide el arranque. Hay que integrar verificador real, política autorizada y registrar handlers en la composición de Go.
