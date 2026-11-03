# Pruebas reproducibles y límites medidos

## Receta de verificación por nivel

La responsabilidad de GPS es proveer fixtures y comprobar el contrato que implementa; la integración con los componentes reales sigue pendiente. Estas instrucciones usan datos sintéticos. **Nunca** señale `GPS_TEST_REDIS_ADDR` al Redis permanente: los tests aislados pueden ejecutar `FLUSHDB` en las bases lógicas de prueba 5 y 6.

**Gateway simulado:** `internal/pathshttp`, `internal/operationshttp` e `internal/gatewayurl` prueban origen y rutas absolutas configurables con `httptest`, preservación de JSON, Bearer e `Idempotency-Key`, validación del acuse y fallos HTTP. Las pruebas no abren comunicación directa con Python ni Operaciones. Para una prueba real posterior, acuerde origen y rutas con Gateway; configure `GPS_GATEWAY_BASE_URL`, `GPS_GATEWAY_ROUTES_PATH`, `GPS_GATEWAY_EVENTS_PATH` y los tokens de servicio necesarios en un entorno aislado. Verifique que el Gateway conserva el cuerpo geográfico y propaga el acuse durable; un 2xx sin `gps.ack.v1` exacto debe quedar pendiente. Los handlers entrantes de control y lectura permanecen fuera del mux operativo aunque Gateway pueda encaminarlos.

La consulta real de sólo lectura es optativa: configure `GPS_GATEWAY_BASE_URL`, `GPS_GATEWAY_ROUTES_PATH`, `GPS_PATHS_TEST_ROUTE_ID` y, si se acordó, `GPS_GATEWAY_ROUTES_TOKEN`; ejecute `go test -run TestGatewayIntegrationOptIn -v ./internal/pathshttp`. Use un identificador vigente verificado por el propietario geográfico. Esta prueba no crea sesiones ni modifica datos y no sustituye la validación de rutas `OperationalReady`. El envío real de eventos continúa deshabilitado.

1. **Unitarias y HTTP de contrato aislado:** desde `acb_gps_lo/`, sin variables `GPS_TEST_*`, ejecutar `go test ./...`, `go vet ./...`, `go mod verify` y `go test -race -p 1 ./...`. Para concentrar los contratos HTTP con `httptest`: `go test -v ./internal/httpapi ./internal/gatewayurl ./internal/pathshttp ./internal/operationshttp`. No se registra un verificador ficticio en producción.
2. **Redis de prueba y flujo entre simuladores:** ejecutar los comandos PowerShell siguientes con Docker activo. Elija otro puerto de loopback si 16379 está ocupado. El contenedor es efímero y no monta los volúmenes del Compose permanente.

   ```powershell
   $testRedis = docker run --rm -d -p 127.0.0.1:16379:6379 redis:8.2.9-bookworm redis-server --appendonly yes --appendfsync always --maxmemory 192mb --maxmemory-policy noeviction
   $env:GPS_TEST_REDIS_ADDR = '127.0.0.1:16379'
   $env:GPS_TEST_REDIS_ISOLATED = '1'
   go test -run TestIsolatedContractFlow -v -count=1 ./internal/control
   go test -p 1 -v -count=1 ./internal/redisstore ./internal/outbox ./internal/httpapi
   Remove-Item Env:GPS_TEST_REDIS_ADDR, Env:GPS_TEST_REDIS_ISOLATED
   docker stop $testRedis
   ```

   El flujo usa ruta sintética, verificador de prueba y receptor `httptest`: creación, credencial, GPS, lectura, evaluación, llegada, outbox, acuse y cierre con historial. Si se interrumpe la ejecución, detenga **sólo** el ID guardado en `$testRedis`. Las pruebas AOF por fases citadas abajo requieren volumen temporal propio y reiniciar ese Redis de prueba; no ejecutar `GPS_TEST_REDIS_ISOLATED=1` entre fases de escritura y lectura.
3. **Carga sintética optativa:** con Redis efímero, establecer también `$env:GPS_LOAD_TEST='1'` y `$env:GPS_LOAD_SESSIONS='100'`; ejecutar `go test -run TestSyntheticLoad -v -count=1 ./internal/loadtest`. Repetir 500/1000 sólo si recursos lo permiten. Limpiar variables y detener contenedor de prueba. Las cifras de esta página son de una ejecución local corta: **no son SLA** ni capacidad garantizada.
4. **Integración real pendiente:** requiere controlador administrativo, proveedor de identidad, ruta Python `OperationalReady`, entrega segura de credencial y receptor de Operaciones con acuse durable. Una prueba simulada no certifica esos componentes ni autoriza usar datos de personas reales.

El documento RF-09 local del servicio Python describe validación geográfica de la primera cercanía a PE y no fija un intervalo numérico de transmisión periódica. Los dos segundos pertenecen al escenario académico de carga sintética; la cadencia del dispositivo y su comportamiento sin red deben acordarse con Presentación. No hay evidencia local para atribuir un período concreto a RF-09.

Las pruebas unitarias no requieren Redis: desde `acb_gps_lo/`, `go test ./...`, `go vet ./...`, `go mod verify`, `go test -race -p 1 ./...`. La integración se habilita sólo para un Redis **temporal y aislado** con `GPS_TEST_REDIS_ADDR=<host:puerto>`. Para el paquete de pruebas que limpia bases de prueba, establezca además `GPS_TEST_REDIS_ISOLATED=1` **únicamente** contra ese servidor: los tests de integración ejecutan `FLUSHDB` en bases de prueba, jamás contra el Redis permanente. El test de flujo completo es `go test -run TestIsolatedContractFlow -v ./internal/control`; usa ruta sintética, verificador y receptor `httptest`, no activa autenticación simulada en el binario operativo. Demuestra creación, reintento, credencial, GPS, lectura, evaluación, llegada, outbox, acuse y cierre con historial retenido.

Para carga sintética optativa, use `GPS_LOAD_TEST=1 GPS_TEST_REDIS_ISOLATED=1 GPS_TEST_REDIS_ADDR=<Redis temporal> GPS_LOAD_SESSIONS=100` y `go test -run TestSyntheticLoad -v -count=1 ./internal/loadtest`. Repita con `500` y `1000` sólo si el equipo y el Redis temporal tienen recursos. El test usa la base lógica 6 y la limpia; un servidor Redis temporal está limitado por memoria y no publica datos personales. Mide dos observaciones por sesión separadas aproximadamente dos segundos, ruta y PE sintéticos evaluados, eventos de activación y llegada, despacho hacia receptor simulado, 10% de sesiones con lecturas internas de última posición e historial. La API de lectura operativa sigue deshabilitada. Los números de AOF son tamaño al comienzo/final; incluyen escrituras de aprovisionamiento previas a la fase de envío. `go_cpu_seconds_delta` mide el runtime de Go del proceso de prueba, no CPU Redis ni sólo el servidor HTTP.

En PowerShell, asigne las variables con `$env:GPS_LOAD_TEST="1"`, `$env:GPS_TEST_REDIS_ISOLATED="1"`, `$env:GPS_TEST_REDIS_ADDR="<Redis temporal>"` y `$env:GPS_LOAD_SESSIONS="100"` antes de `go test`; retire esas variables al terminar. Si Go sólo está disponible en Docker, ejecute el mismo comando dentro de una imagen Go temporal conectada a la red del Redis **temporal**, montando este directorio como `/work`. La bandera de aislamiento autoriza `FLUSHDB` de la base de prueba: nunca señale el Redis permanente.

| Sesiones | Objetivo orientativo | RPS observado | p50 / p95 / p99 HTTP GPS | Errores | Heap Go tras fase | Redis `used_memory` tras fase | Outbox restante / entregados | p95 entrega PE |
| ---: | ---: | ---: | --- | ---: | ---: | ---: | --- | ---: |
| 100 | 50/s | 50,2/s | 7,3 / 34,4 / 40,4 ms | 0 | 2,1 MB | 2,9 MB | 0 / 200 | 139 ms |
| 500 | 250/s | 250,0/s | 39,9 / 68,4 / 83,3 ms | 0 | 4,4 MB | 6,3 MB | 0 / 1000 | 1,36 s |
| 1000 | 500/s | 495,5/s | 32,9 / 65,0 / 75,4 ms | 0 | 8,7 MB | 10,6 MB | 0 / 2000 | 2,68 s |

Mediciones de una sola ejecución en Docker Desktop del equipo local: contenedor de prueba Go limitado a 4 CPU/1 GiB; Redis temporal limitado a 256 MiB, `maxmemory 192mb`, AOF `appendfsync always`, `noeviction`. Duración de envío ~4 s por escenario. No son capacidades garantizadas, ni representan red móvil, TLS, proveedor de identidad o Python/Operaciones reales. La latencia de entrega creció con el tamaño de la cola; debe medirse con carga sostenida, fallos y presupuestos de CPU/disco reales. `aof_delayed_fsync=0` en estas tres ejecuciones no garantiza durabilidad absoluta.

Durante cada ejecución el archivo AOF del Redis de prueba creció aproximadamente 1,22 MB (100 sesiones), 6,10 MB (500) y 12,19 MB (1.000), incluyendo el aprovisionamiento previo y la entrega de eventos. La métrica de CPU del runtime Go aumentó 15,10, 16,94 y 17,66 segundos, respectivamente, durante la fase medida; son cifras del proceso de pruebas y no del Redis ni una comparación de capacidad entre despliegues.

Para recuperación AOF, las pruebas `TestRedisRestartFixture`, `TestEventOutboxRestartProbe`, `TestLifecycleAOFRestartProbe`, `TestControlRestartPhase` y `TestReadRestartPhase` tienen fases de escritura/lectura activadas por variables `GPS_TEST_*_RESTART_PHASE`; se ejecutan alrededor de un reinicio **del Redis temporal**. `TestLifecycleAOFRestartProbe` comprobó sesión finalizada, ambos eventos técnicos, outbox y repetición de cierre tras reinicio. No use `GPS_TEST_REDIS_ISOLATED=1` entre fases, porque limpiaría el fixture. El reinicio comprueba lo que conserva el volumen/AOF del entorno de prueba, no una pérdida total del nodo. Las pruebas de fallos de Redis, reclamaciones vencidas, escritura incierta e idempotencia están en `internal/redisstore`, `internal/outbox`, `internal/httpapi` e `internal/control`.
