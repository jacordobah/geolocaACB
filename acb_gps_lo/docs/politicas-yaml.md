# Catálogo local de políticas YAML (versión 1)

El archivo `config/tracking-policies.yaml` es **sintético**. Sus números no son parámetros operativos aprobados. Este catálogo no autoriza recorridos, no sustituye `OperationalReady` ni habilita la API administrativa. Toda comunicación HTTP entre componentes sigue pasando por el API Gateway; el YAML es un archivo local de GPS.

## Esquema y correspondencia

El documento contiene exactamente `schema_version: 1` y una lista `policies` de 1 a 256 entradas. Cada entrada requiere `policy_id`, `revision`, `geographic` y `transitions`. El ID es exacto y termina en `-vN`, donde N coincide con `revision`; no existe elección automática de la revisión más reciente. Los diez valores se copian a los modelos actuales:

| YAML | Campo Go | Unidad |
| --- | --- | --- |
| `geographic.corridor_radius_meters` | `tracking.GeographicPolicy.CorridorRadiusMeters` | m |
| `geographic.point_radius_meters` | `tracking.GeographicPolicy.PointRadiusMeters` | m |
| `geographic.max_accuracy_meters` | `tracking.GeographicPolicy.MaxAccuracyMeters` | m |
| `transitions.arrival_observations` | `tracking.TransitionPolicy.ArrivalObservations` | muestras |
| `transitions.arrival_dwell` | `tracking.TransitionPolicy.ArrivalDwell` | duración explícita |
| `transitions.deviation_observations` | `tracking.TransitionPolicy.DeviationObservations` | muestras |
| `transitions.deviation_dwell` | `tracking.TransitionPolicy.DeviationDwell` | duración explícita |
| `transitions.recovery_observations` | `tracking.TransitionPolicy.RecoveryObservations` | muestras |
| `transitions.recovery_dwell` | `tracking.TransitionPolicy.RecoveryDwell` | duración explícita |
| `transitions.max_sample_gap` | `tracking.TransitionPolicy.MaxSampleGap` | duración explícita |

El parser rechaza documentos múltiples, claves/campos/IDs duplicados, ausencias, tipos incorrectos, nulos, anclas y alias, valores no finitos, unidades omitidas y valores fuera de los límites de `GeographicPolicy.Validate` y `TransitionPolicy.Validate`. No hay valores predeterminados de política. `GPS_MAX_OBSERVATION_AGE` y `GPS_FUTURE_SKEW` siguen siendo límites globales de entorno, fijados en cada sesión. Límites matemáticos, TTL, secretos, URLs Gateway y Outbox siguen fuera del YAML.

## Carga, selección e identidad

`GPS_POLICIES_FILE` es opcional. Cuando está vacío, Go arranca sin catálogo y conserva salud y recepción de sesiones ya aprovisionadas. Cuando señala un archivo, Go lo lee una vez al iniciar; si falta o es inválido, el arranque falla. El catálogo en memoria implementa `control.Policies.Resolve(ctx, policy_id)` y devuelve copias. No se relee el archivo durante una observación GPS ni hay hot reload.

Cada definición obtiene una huella SHA-256 canónica calculada sobre un objeto JSON de campos fijos con dominio `gps-policy-definition-v1`, ID, revisión y los diez parámetros efectivos. Al arrancar, GPS registra `gps:policy:definition:{base64url(policy_id)}` en Redis sin TTL. Un script Lua compara y escribe atómicamente por ID; un mismo ID con huella diferente provoca error de arranque. Una respuesta Redis incierta se consulta antes de confirmar. Un registro parcial del catálogo puede repetirse: los IDs inscritos permanecen inmutables. Esta garantía histórica depende de conservar el volumen/AOF de Redis; si se pierde, tampoco puede demostrarse la historia anterior. El registro no altera sesiones ni reservas existentes.

En una futura creación administrativa, el controlador elige un `policy_id` exacto, tras autenticación/autorización verificable. Una política ausente produce error de validación; una política presente no concede permiso. `control.Service.Create` reserva la solicitud idempotente y la instantánea antes de aprovisionar. Las repeticiones consultan primero la reserva y conservan la política original, incluso si el catálogo cargado después es distinto.

Las instantáneas nuevas tienen `snapshot_format_version: 2` y `policy_reference` con `policy_id`, `revision` y `definition_hash`, además de los valores efectivos. Su `snapshot_hash` vincula contenido geográfico, política y referencia versionada. `geometry_fingerprint` sigue describiendo únicamente la geometría y asociaciones. Los registros antiguos sin versión siguen validándose con el cálculo original de `snapshot_hash`; no se migran ni reinterpretan. Las posiciones usan la instantánea persistida en Redis, no la versión actual del YAML. Un reinicio de Go vuelve a cargar el catálogo para sesiones nuevas y recupera instantáneas antiguas desde Redis; un reinicio de Redis recupera sólo lo que AOF haya persistido realmente.

## Publicar una versión nueva

1. Acordar parámetros y autorización administrativa fuera de GPS. No reutilizar `demo-school-v1` ni ningún ID histórico con valores distintos.
2. Agregar otra entrada con nuevo ID terminado en `-vN` y `revision: N`; conservar la anterior mientras pueda ser solicitada.
3. Validar YAML y pruebas; montar el archivo de solo lectura en el contenedor y establecer `GPS_POLICIES_FILE=/etc/acb-gps/tracking-policies.yaml`.
4. Reiniciar **sólo Go** de forma controlada. Redis y su volumen permanecen. Una definición histórica alterada hará fallar el arranque.
5. Tras integrar identidad administrativa y registrar los handlers en un incremento futuro, probar selección explícita de la nueva versión con sesión sintética autorizada. Verificar que una sesión previa conserva `policy_reference`, valores y hash originales.

El Compose del componente monta el ejemplo en modo `:ro`; `GPS_POLICIES_FILE` está vacío por defecto. Activar el archivo **no** cambia `GPS_CONTROL_API_ENABLED=false`, no registra control/lectura ni habilita entregas a Operaciones. Nunca usar el ejemplo como política aprobada.

## Alcance y riesgos

Las pruebas y las herramientas internas aún pueden construir instantáneas sintéticas con el formato anterior; el formato 2 corresponde al aprovisionamiento mediante `control.Service.Create` con una política de catálogo.

- La clave de registro sin TTL crece una vez por definición publicada; se necesita gestión operacional y respaldo del volumen. `noeviction` evita expulsión anticipada por Redis, pero no garantiza disponibilidad si se agota la memoria.
- El control administrativo permanece deshabilitado por falta de verificador de identidad y contrato del controlador. La ruta real debe cumplir `OperationalReady`; Python todavía no ofrece todos los campos necesarios.
- El conflicto conocido de `accuracy_m=0` queda fuera de este incremento.
- La garantía de durabilidad de Redis standalone no cubre pérdida del nodo/volumen ni todos los cortes de energía. AOF `appendfsync always` reduce la ventana bajo las condiciones de despliegue, pero no equivale a respaldo o alta disponibilidad.

Código: `internal/policies/catalog.go`, `internal/tracking/snapshot.go`, `internal/control/control.go`, `internal/redisstore/policies.go`, `cmd/acb-gps/main.go`.
