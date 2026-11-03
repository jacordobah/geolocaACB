# ACB_GPS_Lo — Propuesta de políticas de seguimiento en YAML

**Fecha:** 3 de noviembre de 2026 · **Rama inspeccionada:** `feature/gpsACB` · **Revisión base:** `39d4fd5`, con cambios locales de adaptación al API Gateway aún sin commit.  
**Estado:** diseño para revisión; no existe cargador YAML, catálogo operativo ni archivo de políticas en el binario actual. Las rutas de control y lectura continúan sin registrarse en producción.

## 1. Resumen ejecutivo

El código ya separa las reglas geográficas (`tracking.GeographicPolicy`) de las de confirmación temporal (`tracking.TransitionPolicy`). `control.Policies.Resolve(context.Context, policy_id)` es la interfaz prevista para obtenerlas. Hoy sólo hay proveedores simulados en pruebas; el proceso `cmd/acb-gps` no construye `control.Service` ni registra la API administrativa. Los radios y umbrales de las pruebas son **sintéticos**, no decisiones operativas.

Se propone un catálogo YAML local, con identificadores de política inmutables y revisión explícita, validado íntegramente una vez al arranque. Al aprovisionar una sesión autorizada, GPS seleccionaría la política, validaría la ruta y guardaría los valores efectivos dentro de la instantánea Redis existente. Las observaciones seguirían usando la instantánea, nunca el YAML actual. La retención mínima de posiciones, los límites del algoritmo, las credenciales, las URL del Gateway y los ajustes de Redis permanecen fuera del catálogo.

**Separación clave:** el YAML sólo suministraría parámetros; no autoriza por sí mismo un recorrido, un consumidor ni una ruta. `OperationalReady` y un verificador administrativo real continúan siendo requisitos independientes. Referencias: `internal/control/control.go:31-79,132-180`, `internal/tracking/snapshot.go:18-61,143-177`, `cmd/acb-gps/main.go:44-100`.

## 2. Inventario de políticas reales

### 2.1 Parámetros geográficos y temporales de sesión

En la columna YAML, **directo** significa que el campo ya existe en el modelo y bastaría mapearlo y validarlo; **adaptación** significa que falta conectarlo a la selección por política. Los valores de prueba citados no son valores operativos.

| Nombre real; ubicación | Tipo y unidad | Valor actual y origen | Función, validación e impacto | YAML |
| --- | --- | --- | --- | --- |
| `GeographicPolicy.CorridorRadiusMeters`; `internal/tracking/snapshot.go:20-30` | `float64`, m | Sin default operativo; pruebas: **30 m sintéticos** (`internal/tracking/transitions_test.go:15`) | Radio del corredor completo de ruta; `geo.GeofencePolicy.Validate` exige finito y `>0`; `GeographicSnapshot.evaluatePrepared` lo aplica a cada posición (`snapshot.go:183-198`). **No hay tope superior específico**, riesgo de política excesivamente amplia. | Directo; agregar límites operativos aprobados antes de producción. |
| `GeographicPolicy.PointRadiusMeters`; `snapshot.go:22,26-30` | `float64`, m | Sin default; pruebas: **20 m sintéticos** | Radio de cada PE; misma validación positiva y finita; afecta `EvaluateCircle` de todos los PE (`snapshot.go:199-207`). | Directo; tope operativo por acordar. |
| `GeographicPolicy.MaxAccuracyMeters`; `snapshot.go:23,26-30` | `float64`, m | Sin default; pruebas: **25 m sintéticos** | Umbral máximo de incertidumbre horizontal confiable, compartido por corredor y PE. Precisión mayor produce `indeterminate`, no `outside`; debe ser finita y `>0` (`internal/geo/geofence.go:31-33,45-93`). | Directo; tope operativo por acordar. |
| `TransitionPolicy.ArrivalObservations`; `internal/tracking/transitions.go:16-31` | `int`, observaciones | Sin default; pruebas: **2 sintéticas** | Mínimo de evidencias `inside` para confirmar primera llegada y posteriores (`transitions.go:241-263`). Rango `1..100`. | Directo. |
| `TransitionPolicy.ArrivalDwell`; `transitions.go:18,26-31` | `time.Duration`, tiempo | Sin default; pruebas: **1 s sintético** | Permanencia entre primera y última evidencia de PE; `0..24h`, múltiplo de 1 ms (`transitions.go:250-252`). | Directo; YAML como cadena de duración. |
| `TransitionPolicy.DeviationObservations`; `transitions.go:19,26-31` | `int`, observaciones | Sin default; pruebas: **2 sintéticas** | Evidencias `outside` para iniciar episodio; `2..100` (`transitions.go:268-279`). | Directo. |
| `TransitionPolicy.DeviationDwell`; `transitions.go:20,26-31` | `time.Duration`, tiempo | Sin default; pruebas: **1 s sintético** | Tiempo mínimo de evidencia exterior; `0..24h`, múltiplo de 1 ms. | Directo. |
| `TransitionPolicy.RecoveryObservations`; `transitions.go:21,26-31` | `int`, observaciones | Sin default; pruebas: **2 sintéticas** | Evidencias `inside` para resolver episodio; `2..100` (`transitions.go:280-289`). | Directo. |
| `TransitionPolicy.RecoveryDwell`; `transitions.go:22,26-31` | `time.Duration`, tiempo | Sin default; pruebas: **1 s sintético** | Tiempo mínimo de evidencia de regreso; `0..24h`, múltiplo de 1 ms. | Directo. |
| `TransitionPolicy.MaxSampleGap`; `transitions.go:23,26-31` | `time.Duration`, tiempo | Sin default; pruebas: **5 s sintéticos** | Si la separación entre capturas supera el máximo, reinicia candidato de evidencia; `>0..1h`, múltiplo de 1 ms (`transitions.go:222,250,264-266`). | Directo. |
| `Session.MaxObservationAge`; `internal/tracking/model.go:51`, `control/control.go:172-178` | `time.Duration`, tiempo | Global `GPS_MAX_OBSERVATION_AGE=15m` (`internal/config/config.go:143-146`), copiado al crear la sesión | Rechaza capturas anteriores a `now - edad`; se guarda en Redis (`internal/redisstore/store.go:100-105,135`) y se usa en `tracking.Service.Receive` (`service.go:249-259`). `>0..24h`, múltiplo de 1 ms. | **Adaptación:** elegir límite por política, sin exceder el techo de seguridad global. |
| `Session.FutureSkew`; `model.go:52`, `control/control.go:172-178` | `time.Duration`, tiempo | Global `GPS_FUTURE_SKEW=30s` (`config.go:147-150`), copiado al crear | Rechaza capturas posteriores a `now + desfase`; persistido con sesión y usado en `Receive`; `0..5m`, múltiplo de 1 ms. | **Adaptación:** elegir por política, sin exceder techo global. |
| `CreateRequest.PolicyID`; `internal/control/control.go:60-79,132-159` | `string`, ID opaco | Obligatorio en JSON de creación; sólo existe como dato de solicitud y reserva de idempotencia; no hay catálogo operativo | Selecciona `Policies.Resolve`; `tracking.ValidID` exige no vacío, máximo 256 bytes, sin espacios exteriores ni controles (`internal/tracking/model.go:117-129`). El contenido de la política queda en la instantánea; la ID no figura hoy en `Session` ni `GeographicSnapshot`. | Índice único del catálogo; versionado propuesto más abajo. |

**Semánticas fijas, sin interruptor YAML en esta fase:** la lista de PE de la ruta determina el orden; cada PE se confirma individualmente y se pueden confirmar PE posteriores antes de PE01. Una observación confirma como máximo un PE si es el **único** clasificado `inside`; geocercas superpuestas no generan llegada. `indeterminate` nunca confirma llegada y reinicia candidatura de desvío; dos capturas con el mismo instante no constituyen dos evidencias frescas. No existe política de orden estricto, obligación de visitar todos los PE, histéresis radial separada, ventana administrativa de tardanza ni frecuencia de transmisión acordada. Para ofrecer cualquiera de ellas habría que cambiar y probar el algoritmo; **no se agregan campos YAML inertes** (`internal/tracking/transitions.go:193-200,222-295`; `internal/tracking/transitions_test.go:45-101,170-181`).

La precisión ausente da `indeterminate` en `geo.EvaluateDistance`, pero el endpoint HTTP exige `accuracy_m`; una precisión `0` pasa la comprobación no negativa de `Position.Validate` y después `geo.EvaluateDistance` la rechaza como no positiva cuando hay instantánea. Es una inconsistencia de validación observable que requiere revisión de código posterior; el YAML no debe ocultarla (`internal/httpapi/observations.go:33-95`, `internal/tracking/model.go:153-158`, `internal/geo/geofence.go:45-63`).

### 2.2 Parámetros técnicos y restricciones internas: fuera del YAML de políticas

| Grupo y ubicación | Valor o límite comprobado | Motivo para mantenerlo fuera |
| --- | --- | --- |
| Retención `GPS_POSITION_RETENTION`, margen `GPS_POSITION_SAFETY_MARGIN`; `config.go:135-142`, `internal/tracking/model.go:27`, `internal/redisstore/store.go:24-29` | 24 h mínimas, 365 días máximos; margen 0..24 h; defaults 24 h y 5 min | Garantía de almacenamiento y capacidad, no criterio de geocerca. La sesión finalizada no debe reducirla. Sigue en configuración de infraestructura. |
| Motor `MaxSupportedSegmentMeters`, `MaxSupportedRouteMeters`, `MaxSupportedAbsLatitude`, `MaxRouteVertices`, `MinSegmentMeters`; `internal/geo/route.go:13-17,38-68` | 5 km, 100 km, ±70°, 20 000 vértices, 0,01 m; cruce ambiguo del antimeridiano rechazado | Límites de validez matemática y calidad de geometría; no son radios operativos. Subirlos por YAML permitiría evaluar geometrías no verificadas. |
| WGS84, `EarthRadiusMeters`; `internal/geo/geometry.go:11-36`, `internal/geo/distance.go:5` | lon ±180°, lat ±90°; 6 371 000 m | Modelo matemático fijo de esta implementación. |
| Entrada GPS: `Position.Sequence`, marcas de tiempo y tamaño HTTP; `internal/tracking/model.go:131-155`, `internal/config/config.go:151-161` | secuencia 1..2^53−1, timestamps desde 2000 con ms; cuerpo 128..65 536 bytes, default 4096; timeout hasta 30 s, default 5 s | Integridad y protección del transporte. No forman parte de una política de recorrido. |
| Idempotencia, estado activo y reintentos internos; `internal/tracking/service.go:205-325` | huella de entrada; hasta 8 intentos de CAS de transición; sesión finalizada rechaza observaciones | Garantías de consistencia, no reglas configurables de seguimiento. |
| HTTP/Redis/Gateway, recuperación y outbox; `internal/config/config.go:63-132,163-263`, `cmd/acb-gps/main.go:44-100` | URLs, secretos, puertos, timeouts, 2 trabajadores y otros límites de despliegue | Configuración técnica o sensible; permanece en entorno/secretos. El Gateway obligatorio no se reemplaza por el archivo YAML. |
| `OperationalReady`; `internal/tracking/snapshot.go:158-177` | versión oficial, asociación verificada, extremos y primer PE consistentes | Regla de aptitud, no umbral regulable para aceptar rutas incompletas. |

## 3. Arquitectura actual y problemas

1. `control.CreateRequest` exige `policy_id`; `control.Service.Create` primero consulta una reserva existente y, si no la hay, obtiene la ruta y llama a `s.policies.Resolve` (`internal/control/control.go:132-180`). La interfaz `Policies` ya sirve como puerto del catálogo. **No hay implementación productiva**: las únicas implementaciones de `Resolve` están en pruebas (`internal/control/control_test.go`, `integration_flow_test.go`, `internal/httpapi/reads_integration_test.go`).
2. `GeographicPolicy.Validate` reutiliza la validación `geo.GeofencePolicy`; `TransitionPolicy.Validate` verifica contadores y duraciones (`internal/tracking/snapshot.go:26-34`, `internal/tracking/transitions.go:26-31`). `BuildSnapshotWithTransitions` valida la geometría y copia ruta y reglas, calcula huella de la geometría y `SnapshotHash` con ruta, política, motor, límites y transiciones (`snapshot.go:60-141`). `OperationalReady` aplica el guard estricto (`snapshot.go:158-177`).
3. `control.Service.Create` obtiene edad/desfase globales mediante `Tracking.ObservationLimits` y los coloca en `Session` (`control.go:170-179`, `tracking/service.go:60-73`). No forman parte del `SnapshotHash`; sí quedan guardados en el hash Redis de la sesión. Hay, por tanto, **dos fuentes de valores efectivos**: instantánea y sesión.
4. La reserva `gps:control:request:{hash}` guarda `PolicyID`, solicitud, sesión e instantánea, sin TTL para evitar duplicados (`internal/control/control.go:82-94,179-211`, `internal/redisstore/control.go:16-34`). Un reintento recupera esa reserva antes de resolver la política; el cambio posterior del YAML no debe alterar la respuesta reservada.
5. El aprovisionamiento conserva `gps:snapshot:{tracking_id}` como `SnapshotHash|JSON`; una lectura compara hash de sesión, prefijo y snapshot validado (`internal/redisstore/store.go:100-135,165-201,279-301`). Los planes de aprovisionamiento también guardan la instantánea para recuperación (`internal/redisstore/provision.go:21-25,47-71,116-168`). Al terminar, la prueba verifica que sigue accesible con TTL superior a 24 h (`internal/redisstore/snapshot_test.go:122-142`). En reinicio de Go, se recupera desde Redis y se prepara de nuevo la geometría (`internal/tracking/service.go:260-284`). Un reinicio de Redis sólo preserva lo realmente recuperado de su AOF/volumen: Redis standalone no garantiza pérdida cero del nodo.
6. `policy_id` carece de versión y huella de definición propias; sólo se conserva en la reserva de control. Dos definiciones diferentes con la misma ID podrían producir sesiones distintas después de cambiar el archivo. El `SnapshotHash` distingue sus valores, pero no acredita cuál versión nominal se aprobó. Tampoco hay catálogo ni límites operativos superiores para radios y precisión.
7. El proceso de producción crea `tracking.Service` y sólo registra salud/observaciones; no construye el cliente de rutas ni `control.Service`. `GPS_CONTROL_API_ENABLED=true` se rechaza (`cmd/acb-gps/main.go:44-100`, `internal/config/config.go:167-177`). Cargar un YAML no debe desbloquear esos handlers.

## 4. Esquema YAML propuesto

Se recomienda una **lista** `policies`: permite detectar explícitamente IDs repetidas, conservar orden editorial y rechazar versiones ambiguas. El orden **no** decide cuál política usar. Cada `policy_id` identifica una definición inmutable; una revisión cambia la ID (por ejemplo, `demo-school-v1` → `demo-school-v2`). `revision` es metadato propuesto, no un campo Go actual. `schema_version` versiona el formato del archivo, no la política ni la geometría. No habrá alias como `latest`, selección automática de revisión ni fallback a una política por defecto.

Ejemplo **sintético exclusivamente para pruebas**, con valores tomados de fixtures actuales. Los bloques `revision` y `observation_limits` son **extensiones propuestas**; todos los demás parámetros tienen correspondencia directa con estructuras Go existentes. No publicar este ejemplo como política operativa aprobada:

```yaml
schema_version: 1
policies:
  - policy_id: demo-school-v1
    revision: 1
    geographic:
      corridor_radius_meters: 30
      point_radius_meters: 20
      max_accuracy_meters: 25
    transitions:
      arrival_observations: 2
      arrival_dwell: 1s
      deviation_observations: 2
      deviation_dwell: 1s
      recovery_observations: 2
      recovery_dwell: 1s
      max_sample_gap: 5s
    observation_limits:
      max_age: 15m
      future_skew: 30s
```

En v1 se recomienda exigir **todos** los campos de cada política, incluida `transitions`: si fuera `nil`, se calcularían distancias pero no se confirmarían llegadas ni desvíos (`internal/tracking/service.go:285-324`). Sólo una variante de política expresamente acordada y modelada después podría permitir ese comportamiento. Las duraciones YAML son cadenas con unidad (`1s`, `500ms`, `15m`); el parser las convierte a `time.Duration`. No decodificar directamente el JSON de snapshots: los `time.Duration` de Go se serializan allí como enteros de nanosegundos. Los metros son números finitos positivos, no cadenas con sufijo `m`. `policy_id` conserva el formato opaco de `tracking.ValidID`, con una convención adicional de nomenclatura que debe acordarse.

## 5. Selección, versiones e inmutabilidad

**Flujo futuro:** (1) un componente administrativo autorizado envía `policy_id` e `Idempotency-Key` por Gateway; (2) un verificador real acredita audiencia, `tracking:create` y alcance del recorrido; (3) se revisa la reserva existente antes de leer catálogo o ruta; (4) para una solicitud nueva, `Policies.Resolve` obtiene una copia de la definición validada; (5) Go consulta la ruta por Gateway y exige `OperationalReady`; (6) fija valores, referencia de política y límites de observación en sesión/instantánea, reserva y aprovisiona en Redis; (7) `Receive` usa exclusivamente los valores persistidos. La política no sustituye autorización administrativa de selección: hoy `Principal.Allows` sólo comprueba capacidad y alcance del viaje, **no** permiso por `policy_id` (`internal/control/control.go:47-58,132-180`).

| Situación | Comportamiento propuesto |
| --- | --- |
| ID inexistente o retirada para solicitudes nuevas | Rechazar como política desconocida; no usar una alternativa implícita. Distinguirla de error de lectura del catálogo. Acordar código HTTP antes de habilitar control. |
| Varias revisiones | Cada una lleva ID única e inmutable. La solicitud exige esa ID exacta; no elegir por fecha, orden en YAML o número mayor. Rechazar ID repetida o `revision` incompatible con la convención de la ID. Si se acuerda un nombre de familia separado, rechazar también la pareja familia/revisión duplicada. |
| Cambio de valores bajo la misma ID | Rechazar el catálogo o la habilitación de esa ID. Una huella SHA-256 canónica de la definición detecta el cambio **si existe una referencia persistente previa**. La comparación entre reinicios requiere registro durable de `policy_id → huella` en Redis o un manifiesto externo inmutable; el archivo nuevo por sí solo no prueba la definición antigua. Esto se propone para una fase posterior, no existe hoy. |
| Política eliminada mientras hay sesiones | Las sesiones, posiciones y reservas existentes conservan valores efectivos y pueden continuar/reintentarse; nuevas sesiones con esa ID fallan. No borrar snapshots ni reservas por eliminar una entrada del archivo. |
| YAML modificado durante ejecución | No hay hot reload: el proceso sigue con el catálogo cargado. Tras reinicio, sólo nuevas sesiones usan nuevas ID/revisiones. Reiniciar Go no altera instantáneas Redis. |
| Consumidor sin autorización de seleccionar ID | Rechazar antes de consultar ruta o reservar. Hay que acordar con el controlador/identidad cómo representar el permiso sobre `policy_id`; no confiar en un campo de cabecera ni en la mera presencia de la ID. |

**Persistencia propuesta, compatible con legado:** agregar a instantáneas *nuevas* una referencia opcional `{policy_id, revision, definition_hash}` y un `snapshot_format_version` nuevo. Incluirla en el cálculo de `SnapshotHash` de ese formato. Mantener validación separada del formato actual (`short-arc-v1`, sin referencia) para leer snapshots ya persistidos, sin reescribirlos ni reinterpretarlos según YAML. El `GeometryFingerprint` continúa representando sólo ruta y asociaciones; `definition_hash` representa sólo valores de política; `SnapshotHash` vincula ambos y la versión del motor. El identificador y límites efectivos de observación deben mantenerse recuperables junto a la sesión o instantánea. Si se elige guardar estos últimos sólo en `Session`, la confirmación de aprovisionamiento debe seguir comparándolos con la reserva, como hoy (`internal/redisstore/store.go:165-201`). **Esta extensión exige pruebas de serialización/hash y compatibilidad; todavía no está implementada.**

No basta con agregar campos JSON `omitempty` al snapshot: `GeographicSnapshot.Validate` reconstruye y compara la huella calculada por el formato actual; un metadato que no entre en ese cálculo podría modificarse sin detección (`internal/tracking/snapshot.go:143-156`). Los eventos y respuestas que hoy usan `SnapshotHash` deben conservar la semántica de ese hash; una nueva versión de snapshot debe pasar por las pruebas de outbox y recuperación. Las posiciones aceptadas no se recalculan al cambiar políticas: reintentos devuelven el resultado almacenado (`internal/tracking/service.go:231-247,326-355`).

## 6. Carga y validación estricta

1. Introducir, en el incremento de implementación, una variable como `GPS_POLICIES_FILE` que señale un archivo local montado **sólo para lectura**. Es una propuesta; no existe hoy. No almacenar secretos, URLs, puertos o configuración Redis en ese archivo.
2. Leer una vez al arranque con límite de tamaño acordado; rechazar inexistencia, archivo vacío, múltiples documentos, sintaxis inválida y versión de esquema desconocida. Si las APIs de control continúan deshabilitadas, decidir explícitamente entre no exigir ruta o validar un archivo opcional; **nunca** crear una política por defecto silenciosa. Recomiendo no exigirlo en el servicio mínimo, pero fallar al arrancar si se proporcionó una ruta inválida. Antes de registrar control, exigir catálogo válido y no vacío.
3. Para Go, una dependencia acotada sería [`go.yaml.in/yaml/v3`](https://pkg.go.dev/go.yaml.in/yaml/v3): su API documenta `Decoder.KnownFields`. Elegir y fijar una versión concreta durante la implementación, tras revisar compatibilidad con `go.mod`; **hoy no está en dependencias**. Un DTO de configuración distinto de los modelos Redis permite duraciones textuales y campos obligatorios explícitos. Además de `KnownFields`, recorrer el árbol YAML para detectar claves duplicadas y rechazar alias/merge si no se necesitan; verificar EOF tras un documento. No asumir que `KnownFields` detecta todas las duplicaciones semánticas.
4. Validar cada campo presente, tipo, finitud, unidades, rangos actuales de `GeographicPolicy.Validate` y `TransitionPolicy.Validate`, IDs y revisiones únicas. Para `observation_limits`, aplicar los límites actuales `1ms..24h` y `0..5m`, y además no permitir superar los techos globales de proceso si se mantienen como barreras de seguridad. Rechazar `null`, conversiones implícitas ambiguas y desconocidos.
5. Construir un mapa inmutable `policy_id → definición validada`. `Resolve` devuelve valores por copia; copiar también `TransitionPolicy`, que hoy es puntero. No usar variables globales mutables ni leer el archivo por observación. Registrar sólo ruta/ID/huella del catálogo, nunca secretos ni datos GPS.
6. En Docker, proponer en un incremento posterior un bind mount de archivo con modo `ro` y la ruta del entorno. Ese mount no depende del Gateway; no se debe incorporar a la imagen ni alterar el Compose de otros componentes. Falta acordar ruta del host, propietario del archivo, revisión y procedimiento de despliegue. Un cambio exige reinicio controlado de Go y comprobación de readiness/catálogo; no hace falta reiniciar Redis.

## 7. Archivos previstos y riesgo de compatibilidad

| Área / archivo previsto | Cambio y dependencia | Riesgo y prueba necesaria |
| --- | --- | --- |
| Modelo `internal/tracking/snapshot.go`, `transitions.go` | Reutilizar `GeographicPolicy` y `TransitionPolicy`; incorporar metadatos de versión sólo en snapshots nuevos si se aprueba el esquema versionado. | Hash legado, `Validate`, eventos; comparar snapshots viejos y nuevos con fixtures. |
| Parser/catálogo nuevo, por ejemplo `internal/policies/catalog.go` y `catalog_test.go` | DTO YAML estricto e implementación de `control.Policies.Resolve`. Añadir una sola dependencia YAML en `go.mod`/`go.sum`. | Claves desconocidas/duplicadas, alias, duración, flotantes no finitos, copia inmutable; tests unitarios sin Redis. |
| Configuración `internal/config/config.go`, `.env.example` | Ruta `GPS_POLICIES_FILE`, validación de presencia según estado operativo; conservar variables técnicas y Gateway. | Arranque actual de salud/GPS, archivo ausente; pruebas de configuración. |
| Composición `cmd/acb-gps/main.go` | Cargar catálogo una vez e inyectarlo en `control.Service` **sólo cuando exista verificador real y fuente de autorización aprobada**. | No registrar handlers sólo por tener YAML; probar liveness/readiness y flag de control rechazado mientras falte identidad. |
| Selección `internal/control/control.go` | Resolver ID exacta, aplicar permiso de selección acordado, fijar referencia/revisión/huella y límites de observación propuestos. | Reintentos con reserva previa, cambios del YAML y errores HTTP; tests de idempotencia y alcance. |
| Persistencia `internal/redisstore/store.go`, `control.go`, `provision.go`; scripts únicamente si hace falta | Guardar/leer metadatos inmutables con snapshot y reserva; comprobar coherencia de sesión, hash e índice. Registro durable de huellas por ID si se aprueba detección entre reinicios. | Compatibilidad Redis y recuperación de escrituras inciertas; Redis aislado, AOF y concurrencia. No migrar ni borrar datos existentes. |
| Docker `acb_gps_lo/compose.yaml` | Montaje local `ro` y variable de ruta, sin Python ni Operaciones dentro del Compose. | Ausencia del archivo, permisos del usuario no privilegiado, despliegue independiente; validar Compose y build en entorno aislado. |
| Documentación `README.md`, `docs/*` pertinentes | Describir schema YAML, versiones, selección, errores y pasos de activación. | Evitar presentar ejemplos sintéticos como políticas aprobadas; revisar OpenAPI sólo si cambia contrato HTTP. |

## 8. Matriz de pruebas propuesta

| Nivel | Casos | Infraestructura |
| --- | --- | --- |
| Parser y catálogo | YAML válido/malformado/inexistente/vacío, múltiples documentos, clave desconocida o duplicada, alias, tipos, `null`, faltantes, ID/revisión duplicadas, campos negativos/NaN/Inf, duración sin unidad, versión de esquema incompatible, `Resolve` inexistente y copia inmutable. | Sin Redis; archivos temporales de test. |
| Validación de políticas | Bordes de los 3 radios/precisión, los 3 contadores, 4 duraciones, límites de edad/desfase y techo global; `TransitionPolicy=nil` no aceptada para política operativa. | Sin infraestructura; comparar con validadores Go existentes. |
| Control/HTTP aislado | ID exacta, permiso y alcance, solicitud con ID inexistente/no autorizada, reserva antes de catálogo, mismo `Idempotency-Key` tras cambio de archivo, conflicto de cuerpo, no habilitación con verificador simulado. | `httptest` con ruta por Gateway simulado; Redis simulado o temporal según el caso. |
| Instantánea/Redis | Guardado de versión y hash, cambio de YAML entre sesiones, lectura de snapshot antiguo sin metadata, reinicio Go, reinicio Redis con AOF, política eliminada, plan de aprovisionamiento incierto, sesiones concurrentes y primer arribo/desvío sin cambio semántico. | **Redis aislado** y datos sintéticos; nunca el Redis permanente. |
| Regresión y despliegue | `go test ./...`, `go vet ./...`, `go mod verify`, `go test -race -p 1 ./...`; Docker build, Compose config, mount `ro`, salud sin políticas cuando control esté apagado, Gateway sin cambios en rutas/JSON. | Unitarias sin servicios; tests Redis aislados con `GPS_TEST_REDIS_ADDR` y bandera de aislamiento sólo en ese servidor; Docker temporal. |

## 9. Riesgos y decisiones pendientes

| Decisión / riesgo | Responsable y condición previa |
| --- | --- |
| Radios, precisión y ventanas **operativos**; frecuencia prevista de GPS | Controlador administrativo, propietario geográfico y Presentación deben acordar valores y validarlos con rutas representativas. Los fixtures no son aprobación. |
| Nombre estable de `policy_id`, revisiones, permiso por política y eliminación | Controlador administrativo/identidad acuerda qué consumidor puede seleccionar cada ID; GPS aplica esa decisión verificable. |
| `observation_limits` por política o sólo globales | GPS y controlador deciden si la extensión es necesaria. Mantener límites actuales en sesión y techo de proceso; no ampliar ventanas por YAML sin revisión de seguridad. |
| Detección de reuso de ID con contenido cambiado entre reinicios | GPS debe diseñar registro durable de huellas o suministro de catálogo inmutable versionado. Sin ello, sólo se detectan diferencias por snapshot entre sesiones, no una alteración nominal histórica. |
| Compatibilidad de snapshots y motor | Mantener lector de formato viejo; no alterar ni borrar snapshots activos. Un cambio futuro de `EngineVersion` puede requerir coexistencia de evaluadores para sesiones antiguas. |
| Aptitud real de rutas | Python debe publicar versión oficial y asociaciones verificadas; `OperationalReady` permanece estricto. La discrepancia de rutas que inician en colegio requiere acuerdo aparte; YAML no la resuelve (`internal/tracking/snapshot.go:158-177`). |
| Credenciales y API administrativa | Proveedor de identidad verificable, control de capacidades/alcances y entrega segura de token pendientes. El YAML no debe habilitar `GPS_CONTROL_API_ENABLED`; actualmente `true` falla. |
| Durabilidad y recuperación | AOF/volumen Redis y reservas existentes; el archivo YAML no sustituye snapshots, outbox ni respaldo. Registrar una ID de política nueva no cambia observaciones históricas. |

## 10. Orden recomendado para implementar después de aprobar este diseño

1. Acordar semántica de `policy_id`, revisión, permisos por política, umbrales operativos y tratamiento de edad/desfase; decidir si se exige registro durable de huellas.
2. Especificar DTO YAML v1 y pruebas estrictas del parser/catálogo, sin conectar todavía control operativo.
3. Añadir ruta de archivo a configuración y carga única en proceso; probar arranque con control deshabilitado y fallo explícito si se configuró un archivo inválido.
4. Introducir referencia/hash de política en **nuevas** instantáneas con formato versionado y lector legado; verificar persistencia, reservas y recuperación Redis aislado.
5. Conectar `control.Policies` y selección exacta; si se aprueban `observation_limits` por política, fijarlos en la sesión dentro de los techos globales. Repetir pruebas de idempotencia antes y después de editar el YAML.
6. Agregar montaje Docker `ro`, actualizar documentación y ejecutar regresión, carreras, build y pruebas sintéticas. Mantener control/lectura sin registrar hasta aprobar identidad, autorización administrativa y rutas `OperationalReady` reales.

**Resultado de esta fase:** propuesta exclusivamente documental. No se modifica el cargador, el algoritmo, Redis, Lua, Docker, los contratos HTTP ni las políticas operativas actuales.
