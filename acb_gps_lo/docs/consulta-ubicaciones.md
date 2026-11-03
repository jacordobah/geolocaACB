# Consulta autorizada de ubicación e historial

**Estado real:** los handlers de lectura se registran sólo si `GPS_CONTROL_API_ENABLED=true`, `GPS_READ_API_ENABLED=true` y todas las dependencias de control son válidas. GPS exige la credencial interna del Gateway; el Gateway debe autorizar contextual y previamente la lectura de cada recorrido/sesión. El Bearer de observación no sirve para leer. El consumidor autorizado puede ser administrativo o de Presentación a través de Gateway, según acuerdo; GPS no decide el rol de negocio. La integración con Gateway real sigue pendiente.

## Última ubicación

```http
GET /api/v1/tracking-sessions/t-demo/latest-position HTTP/1.1
Host: gps.example.test
Authorization: Bearer <credencial-interna-Gateway>
```

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"tracking_id":"t-demo","tracking_state":"active","position":{"sequence":7,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":"2026-11-02T20:00:00Z","received_at":"2026-11-02T20:00:01Z","geographic_evaluation":"not_evaluated"},"age_ms":1000}
```

`age_ms` se calcula en el servidor desde `received_at` y nunca es negativo. La evaluación puede ser el objeto descrito en [presentacion.md](presentacion.md); las posiciones expiradas no se devuelven. Una sesión existente sin observaciones retorna **204 sin cuerpo**; si tuvo una última posición pero ya expiró conforme a su TTL, **410** `{"error":"position_expired"}`. Una ausencia inesperada antes del vencimiento se trata como 503, no como expiración normal. La lectura no renueva TTL.

## Historial paginado

```http
GET /api/v1/tracking-sessions/t-demo/positions?after_sequence=0&limit=2 HTTP/1.1
Host: gps.example.test
Authorization: Bearer <credencial-interna-Gateway>
```

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Cache-Control: no-store

{"tracking_id":"t-demo","tracking_state":"finished","positions":[{"sequence":1,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":"2026-11-02T20:00:00Z","received_at":"2026-11-02T20:00:01Z","geographic_evaluation":"not_evaluated"},{"sequence":3,"latitude":4.601,"longitude":-74.101,"accuracy_m":5,"captured_at":"2026-11-02T20:00:04Z","received_at":"2026-11-02T20:00:05Z","geographic_evaluation":"not_evaluated"}],"next_after_sequence":3,"has_more":false}
```

`after_sequence` es un cursor **exclusivo** entre 0 y 9007199254740991. `limit` debe ser 1..`GPS_READ_MAX_PAGE_LIMIT` (máximo 100); sin parámetro se usa el menor entre 50 y ese límite. Continuar con `next_after_sequence` mientras `has_more=true`; las secuencias no son consecutivas y el orden es por secuencia, no por hora capturada. La implementación omite claves de posición expiradas y acota las lecturas Redis. Si el JSON de la página excede `GPS_READ_MAX_RESPONSE_BYTES`, devuelve 422 `{"error":"page_too_large"}`; reducir `limit`. La consulta sigue permitida después del cierre mientras persistan datos y permisos.

## Fallos y política de reintento

Los handlers registrados responden 400 por ID, cursor, límite, query desconocida/duplicada o query en latest (`invalid_tracking_id`, `invalid_pagination`, `invalid_query`); 401 `unauthorized` por credencial ausente/inválida o audiencia errónea; 403 `forbidden` por capacidad global insuficiente; 404 `not_found` para sesión inexistente o fuera de alcance; 410 sólo en latest para expiración normal; 422 `page_too_large`; 503 `temporarily_unavailable` o `response_unavailable` ante Redis, timeout, integridad incierta o codificación de respuesta. Ejemplo fallido: `GET .../positions?limit=0` devuelve 400 `{"error":"invalid_pagination"}`; corregir el parámetro. Un Bearer de observación no debe superar el verificador de servicio y recibe 401. En 503 reintentar con espera y el mismo cursor; en 400/401/403/404/422 corregir causa o reducir página. Ningún error debe exponer coordenadas o secretos.

**Verificación:** ejecutar `go test ./internal/httpapi ./internal/control ./cmd/acb-gps` y la integración aislada de [pruebas-integracion.md](pruebas-integracion.md), comprobar 200/204/410, cursor sin duplicados, huecos y `Cache-Control: no-store`. Antes de uso real, Gateway debe demostrar permisos propios/ajenos y propagación de 401 para el token GPS. Si `GPS_READ_API_ENABLED=false`, el mux no registra estas rutas y devuelve 404 genérico.
