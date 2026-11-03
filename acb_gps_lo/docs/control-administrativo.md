# Integración del controlador administrativo de seguimiento

**Estado del catálogo:** `policy_id` debe ser una identidad versionada que termine en `-vN`, con `revision: N` en el YAML. El archivo sintético incluido no autoriza su uso en recorridos reales. La selección de políticas y el contexto administrativo se autorizan en Gateway en el modelo confiable del Prototipo 2. Véase [políticas YAML](politicas-yaml.md) y el [contrato Gateway](contrato-gateway-gps.md).

El componente que autoriza los recorridos todavía no está identificado. `ACB_Operation_Lo` recibe acontecimientos; no se le atribuye creación o cierre. El binario **registra** los handlers de control si `GPS_CONTROL_API_ENABLED=true` y se configuran credencial interna, clave HMAC, catálogo y ruta geográfica Gateway válidos. Por defecto permanecen ausentes. La integración con el Gateway y Administración reales sigue pendiente.

1. Gateway autentica y autoriza al consumidor, recorrido y `policy_id`; después usa la credencial interna compartida con GPS. El verificador en Go comprueba esa credencial y asigna un sujeto de servicio estable con capacidades técnicas. El Bearer de observación no sirve para control. Acordar red privada, canal protegido y distribución de secretos fuera del repositorio.
2. Obtener del propietario administrativo autorización de inicio, `trip_id`, `guide_id`, `geo_route_id` y `policy_id` aprobado. Gateway comprueba el alcance contextual. GPS comprueba la credencial interna y `OperationalReady` de la ruta; no aplica reglas de negocio del recorrido.
3. Enviar `POST /api/v1/tracking-sessions` con `Authorization: Bearer <credencial-interna-Gateway>`, `Idempotency-Key: <clave-opaca-estable>`, `Content-Type: application/json` y cuerpo `{"trip_id":"trip-1","guide_id":"guide-1","geo_route_id":"route-1","policy_id":"demo-school-v1"}`. La clave debe permanecer igual en todos los reintentos de la **misma** solicitud. El cuerpo máximo es 4096 bytes y no se admiten campos desconocidos.
4. Respuesta 201 nueva o 200 repetida: `tracking_id`, `session` y `credential_status`. `observation_credential` se entrega únicamente durante la ventana autorizada de emisión; `credential_status` puede ser `issued`, `reissue_required` o `revoked`. La respuesta incluye `Cache-Control: no-store`. Una respuesta perdida se reintenta con la misma clave; no se crea otra sesión. Si la ventana caducó, no se recupera el texto claro mediante esta API: requiere acordar rotación/autorización explícita antes de operación real.
5. Entregar la credencial de observación de forma segura a Presentación. No guardarla en logs, URL, métricas o mensajes. GPS conserva el hash SHA-256 de la credencial; la derivación original usa HMAC-SHA256 y clave externa de al menos 32 bytes. Perder/rotar esa clave impide reemitir la misma credencial; las sesiones activas que ya la poseen pueden continuar hasta el cierre. No hay mecanismo operativo de rotación implementado.
6. Gateway autoriza la consulta del recorrido y envía `GET /api/v1/tracking-sessions/{tracking_id}` con su credencial interna. Para finalizar, primero autoriza el cierre y envía `POST /api/v1/tracking-sessions/{tracking_id}/finish` con cuerpo vacío. La repetición de cierre es idempotente. El cierre revoca observaciones, conserva historial sujeto a 24 h y genera `tracking_session_finished` interno.

Errores: 400 JSON/clave/cuerpo inválido; 401 sin credencial interna válida; 403 capacidad ausente en un verificador de capacidades restringidas; 404 ruta o sesión inexistente; 409 misma clave con otra solicitud; 415 tipo de contenido; 422 ruta no apta; 503 dependencia o resultado de escritura incierto (`Retry-After: 1`). En el modo Gateway confiable, GPS asigna las capacidades técnicas configuradas; las denegaciones de usuario, recorrido y política ocurren en Gateway antes de llegar a GPS. Una respuesta 503 **no** autoriza crear otra solicitud con una clave nueva. La unicidad administrativa de una sesión activa por recorrido aún requiere acuerdo.

Las reservas de idempotencia Redis no tienen TTL hasta acordar el horizonte máximo de reintentos y una política de tombstones. Esto evita duplicación por reutilización prematura de clave, pero exige dimensionar almacenamiento y mantenimiento futuros.

## Contrato HTTP completo de referencia

La responsabilidad del consumidor es autorizar el recorrido, proporcionar `trip_id`, `guide_id`, `geo_route_id` y un `policy_id` aprobado, conservar la misma `Idempotency-Key` ante reintentos y entregar de forma segura la credencial al dispositivo. Los cuatro campos son obligatorios y no se admiten campos desconocidos. Los ejemplos siguientes son **sintéticos** y sólo funcionan hoy con el handler conectado por un fixture de prueba:

```http
POST /api/v1/tracking-sessions HTTP/1.1
Host: gps.example.test
Authorization: Bearer <credencial-interna-Gateway>
Idempotency-Key: inicio-trip-demo-1
Content-Type: application/json

{"trip_id":"trip-demo","guide_id":"guia-demo","geo_route_id":"ruta-demo","policy_id":"demo-school-v1"}
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
Authorization: Bearer <credencial-interna-Gateway>
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
Authorization: Bearer <credencial-interna-Gateway>
Content-Length: 0
```

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"session":{"id":"t-demo","guide_id":"guia-demo","route_id":"ruta-demo","trip_id":"trip-demo","state":"finished","started_at":"2026-11-02T20:00:00Z","finished_at":"2026-11-02T21:00:00Z","last_sequence":0,"max_observation_age":900000000000,"future_skew":30000000000,"snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
```

Un reintento de creación con identidad, clave y contenido idénticos devuelve 200 y la misma sesión. Dentro de la ventana de emisión devuelve `credential_status: issued`; fuera, `observation_credential: ""` y `credential_status: reissue_required`; si la sesión terminó, `revoked`. No hay endpoint de rotación ni reemisión tardía. El cierre repetido devuelve 200 sin nuevo efecto. La credencial se deriva mediante HMAC-SHA256 de `GPS_CONTROL_CREDENTIAL_KEY` (32 a 64 bytes aleatorios, base64url); Redis conserva su hash SHA-256. La pérdida o rotación de la clave impide reemisión, aunque una credencial ya entregada sigue siendo válida hasta la revocación.

**Flujo fallido:** si se repite `inicio-trip-demo-1` con otro `guide_id`, el handler devuelve 409 `{"error":"idempotency_conflict"}` y no crea una segunda sesión. Si Python entrega una ruta consultable pero no `OperationalReady`, devuelve 422 `{"error":"route_ineligible"}`. Si Redis o el resultado de escritura son inciertos, devuelve 503 `{"error":"temporarily_unavailable"}` con `Retry-After: 1`: reintentar **la misma** solicitud y clave, nunca inventar otra clave. Una ruta inexistente devuelve 404; JSON/campos/ID inválidos 400; cuerpo >4096 bytes 413; media type distinto de JSON 415; credencial no verificada 401; capacidad insuficiente 403; sesión ajena 404. Una respuesta Python mal formada puede mapearse a 503, no necesariamente a 422.

**Verificación:** usar los tests `internal/control`, `internal/httpapi` y `cmd/acb-gps` con Redis aislado según [pruebas-integracion.md](pruebas-integracion.md). Con configuración completa, comprobar 201/200/409, protección 401, 422/503, revocación y `Cache-Control: no-store`. Con el flag apagado, el mux devuelve su 404 genérico; con el flag encendido y credencial interna válida registra los handlers. Los permisos contextuales y las políticas reales deben verificarse con el Gateway y el controlador administrativo antes del uso operativo.
