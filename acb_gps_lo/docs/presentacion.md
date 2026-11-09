# Presentación: observaciones GPS

`POST /api/v1/tracking-sessions/{tracking_id}/observations` **está registrado**. Para uso real se necesita que el controlador autorizado aprovisione una sesión apta y entregue al dispositivo su credencial exclusiva. El identificador de sesión no autentica. La credencial Bearer sólo permite enviar observaciones a esa sesión; no crea/finaliza sesiones ni lee posiciones.

Enviar `Authorization: Bearer <credencial-observación>` y `Content-Type: application/json` con, por ejemplo:

```json
{"sequence":1,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":"2026-11-02T15:00:00-05:00"}
```

`sequence` es entero positivo por sesión, estrictamente creciente; puede haber huecos. `captured_at` debe incluir zona horaria explícita y precisión de milisegundos. La posición debe estar dentro de coordenadas válidas y tener precisión horizontal no negativa; la sesión fija antigüedad máxima y desfase futuro. El límite por defecto de cuerpo es 4096 bytes. La frecuencia de envío de dos segundos se usó en pruebas orientativas; **no está acordada como requisito de Presentación**.

HTTP 201 indica nueva observación aceptada; 200 `acceptance: replayed` devuelve el resultado y vencimiento originales para la misma secuencia y contenido. Respuesta: `tracking_id`, `sequence`, `acceptance`, `received_at`, `expires_at`, `geographic_evaluation`. Esta última es `not_evaluated` si no hubo cálculo, u objeto con `route_status`, `route_distance_meters`, `meeting_points`, razones y `snapshot_hash`. `inside`, `outside` e `indeterminate` son evaluaciones, no decisiones administrativas. El cliente debe conservar el mismo cuerpo y secuencia al reintentar un timeout o 503. Una misma secuencia con contenido diferente produce 409 `sequence_conflict`; una secuencia antigua produce 409 `stale_observation`. 400 indica entrada inválida, 401 token inexistente/incorrecto/sesión cerrada, 413 cuerpo grande, 415 media type y 503 fallo temporal o resultado incierto. No se debe considerar una observación almacenada después de un 503 sin consultar o reintentar idénticamente.

La credencial se revoca al cierre técnico. El flujo para renovarla o reasignarla aún debe acordarse con el controlador administrativo; no hay endpoint de rotación público. Los clientes no deben guardar la credencial en almacenamiento inseguro ni incluirla en diagnósticos. Las posiciones se conservan al menos 24 h desde captura en el Redis acordado, aun después del cierre técnico.

## Intercambio HTTP y responsabilidad del cliente

Presentación debe obtener la credencial por un canal acordado con el futuro controlador, protegerla, mantener una secuencia por sesión y conservar **exactamente** el cuerpo de cada observación hasta recibir confirmación. El token sólo tiene alcance de envío a su `tracking_id`; conocer el ID no autoriza nada. La API está registrada, pero los recorridos reales siguen bloqueados por aprovisionamiento administrativo e información geográfica `OperationalReady` pendientes.

```http
POST /api/v1/tracking-sessions/t-demo/observations HTTP/1.1
Host: gps.example.test
Authorization: Bearer <credencial-exclusiva-de-t-demo>
Content-Type: application/json

{"sequence":1,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":"2026-11-02T15:00:00.000-05:00"}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"tracking_id":"t-demo","sequence":1,"acceptance":"accepted","received_at":"2026-11-02T20:00:01Z","expires_at":"2026-11-03T20:05:01Z","geographic_evaluation":"not_evaluated"}
```

El ejemplo `not_evaluated` corresponde a una sesión sintética sin instantánea. Con instantánea apta, `geographic_evaluation` es un objeto; por ejemplo:

```json
{"snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","route_status":"inside","route_reason":"uncertainty_within_radius","route_distance_meters":2,"meeting_points":[{"id":"PE01","status":"inside","reason":"uncertainty_within_radius","distance_meters":3},{"id":"PE02","status":"outside","reason":"uncertainty_outside_radius","distance_meters":120}]}
```

Los valores de `reason` son ilustrativos; el cliente debe tratar el estado como clasificación geográfica, no como autorización administrativa. Reenviar cuerpo y secuencia idénticos produce HTTP 200 con `acceptance: "replayed"`, `received_at`, `expires_at` y evaluación originales. No asuma que un 503/timeout rechazó la escritura: reintente idénticamente y con espera. Una secuencia repetida con otro contenido produce HTTP 409 `{"error":"sequence_conflict"}`; una secuencia antigua, 409 `{"error":"stale_observation"}`. Token ausente, ajeno o revocado: 401 `unauthorized`; JSON o tiempo inválido: 400 `invalid_json`/`invalid_observation`; cuerpo demasiado grande: 413; tipo no JSON: 415; Redis o resultado incierto: 503 `storage_unavailable` y `Retry-After: 1`. Corrija 400/409/413/415; no los reintente sin cambios.

`captured_at` exige RFC3339 con zona y precisión hasta milisegundos; la validación de sesión impone antigüedad máxima y desfase futuro. `accuracy_m` es finito y no negativo; la política geográfica puede clasificar una precisión insuficiente como `indeterminate`, nunca automáticamente como desvío. `sequence` debe estar entre 1 y 9007199254740991, estrictamente creciente para nuevas observaciones; puede tener huecos. Campos extra, como IDs administrativos o radios, se rechazan.

**Frecuencia:** el documento RF-09 local del servicio Python describe la validación de primera cercanía a PE y no fija un intervalo numérico de envío GPS. El envío cada dos segundos se utilizó sólo para carga sintética; no es un requisito académico verificado ni un acuerdo de interfaz con Presentación. Deben acordarse cadencia, comportamiento sin red, precisión del proveedor GPS y presupuesto de datos antes del uso real.

**Verificación:** con sesión sintética y token de fixture, probar 201, repetición 200 sin otra posición, conflicto 409 y revocación 401 tras cierre mediante `TestIsolatedContractFlow`. Para integración real, acordar entrega de credencial y habilitación de control, comprobar que el dispositivo no registra tokens ni coordenadas en logs y que los reintentos conservan la misma secuencia y JSON.
